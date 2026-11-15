package mediameta

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"time"
)

// MPEG-2 transport streams (ISO/IEC 13818-1): .ts, and AVCHD's .mts and
// .m2ts, whose packets carry a 4-byte timestamp each. A camcorder puts
// what it records about the recording (the date and time, GPS, the make)
// in the H.264 video stream, as MDPM ("Modified Digital Video Pack
// Metadata") in an SEI message of unregistered user data. Read only: the
// metadata is inside the coded video, which a write would have to
// re-packetize.

const (
	tsPacket    = 188
	tsSync      = 0x47
	maxTSScan   = 16 << 20 // read from the start for the video headers and MDPM
	maxTSBack   = 512_000  // read from the end for the last PCR
	maxPESBytes = 1 << 20  // video stream bytes kept for the headers
)

// errTSReadOnly is the error of a write to a transport stream.
var errTSReadOnly = fmt.Errorf("%w: a transport stream (MTS, M2TS) is read only", ErrUnsupported)

// mdpmUUID is the user data UUID of MDPM, followed by "MDPM".
var mdpmUUID = []byte("\x17\xee\x8c\x60\xf8\x4d\x11\xd9\x8c\xd6\x08\x00\x20\x0c\x9a\x66MDPM")

// tsLayout tells whether r is a transport stream: its packet length (188,
// or 192 with a timestamp before each) and where the first packet's sync
// byte is. Three packets in a row must start with one.
func tsLayout(r io.ReaderAt, size int64) (stride, first int64, ok bool) {
	var b [3 * 192]byte
	if size < int64(len(b)) {
		return 0, 0, false
	}
	if _, err := r.ReadAt(b[:], 0); err != nil {
		return 0, 0, false
	}
	for _, l := range []struct{ stride, first int64 }{{192, 4}, {188, 0}} {
		if b[l.first] == tsSync && b[l.first+l.stride] == tsSync && b[l.first+2*l.stride] == tsSync {
			return l.stride, l.first, true
		}
	}
	return 0, 0, false
}

func isTS(r io.ReaderAt, size int64) bool {
	_, _, ok := tsLayout(r, size)
	return ok
}

// tsReader reads a transport stream's packets.
type tsReader struct {
	r             io.ReaderAt
	size          int64
	stride, first int64
}

// packet reads packet i's 188 bytes into p, false past the end or without
// its sync byte.
func (t *tsReader) packet(i int64, p []byte) bool {
	at := t.first + i*t.stride
	if at < 0 || at+tsPacket > t.size {
		return false
	}
	if _, err := t.r.ReadAt(p[:tsPacket], at); err != nil {
		return false
	}
	return p[0] == tsSync
}

func (t *tsReader) count() int64 { return (t.size - t.first + t.stride - tsPacket) / t.stride }

// tsFields are a packet's header fields (13818-1 2.4.3.2): the PID, the
// payload unit start flag, the payload, and the PCR when the adaptation
// field has one.
func tsFields(p []byte) (pid uint16, start bool, payload []byte, pcr int64, hasPCR bool) {
	pid = binary.BigEndian.Uint16(p[1:]) & 0x1FFF
	start = p[1]&0x40 != 0
	control := p[3] >> 4 & 3
	pos := 4
	if control&2 != 0 { // an adaptation field
		n := int(p[4])
		if 5+n > tsPacket {
			return pid, start, nil, 0, false
		}
		if n >= 7 && p[5]&0x10 != 0 {
			base := int64(binary.BigEndian.Uint32(p[6:]))<<1 | int64(p[10]>>7)
			ext := int64(binary.BigEndian.Uint16(p[10:]) & 0x1FF)
			pcr, hasPCR = base*300+ext, true
		}
		pos = 5 + n
	}
	if control&1 != 0 && pos < tsPacket {
		payload = p[pos:tsPacket]
	}
	return
}

// readTSVideo reads Video from a transport stream: from the first video
// stream's sequence parameter set the size, from its MDPM the recording
// date, GPS, make and model, and the duration from the first and last
// program clock reference.
func readTSVideo(r io.ReaderAt, size int64) (Video, error) {
	stride, first, ok := tsLayout(r, size)
	if !ok {
		return Video{}, formatError("transport stream")
	}
	t := &tsReader{r: r, size: size, stride: stride, first: first}
	v := Video{Orientation: 1}
	var p [tsPacket]byte
	pmtPID, videoPID := -1, -1
	var es []byte // the video stream, from its first PES packet
	var startPCR, lastPCR int64
	gotPCR, gotSPS, gotMDPM := false, false, false
	// MDPM comes with the first picture, before its first slice: once
	// that has gone by without one, there's none to find.
	pastSlice := false
	n := min(t.count(), maxTSScan/t.stride)
	var i int64
	done := func() bool { return gotSPS && (gotMDPM || pastSlice) && gotPCR }
	for ; i < n && !done(); i++ {
		if !t.packet(i, p[:]) {
			return Video{}, fmt.Errorf("%w: transport stream packet %d without its sync byte", ErrMalformed, i)
		}
		pid, start, payload, pcr, hasPCR := tsFields(p[:])
		if hasPCR {
			if !gotPCR {
				startPCR, gotPCR = pcr, true
			}
			lastPCR = pcr
		}
		switch {
		case pid == 0 && start && pmtPID < 0:
			pmtPID = patProgramMap(payload)
		case int(pid) == pmtPID && start && videoPID < 0:
			videoPID = pmtVideoPID(payload)
		case int(pid) == videoPID && payload != nil:
			if start {
				payload = pesPayload(payload)
			}
			if es == nil && !start {
				continue // the stream from its first PES packet on
			}
			if len(es) < maxPESBytes {
				es = append(es, payload...)
			}
			// The headers come first in the stream: read them once it holds
			// some.
			if !gotSPS || !gotMDPM && !pastSlice {
				gotSPS, gotMDPM, pastSlice = readH264Headers(es, &v, gotSPS, gotMDPM)
			}
		}
	}
	// The last PCR, near the end of the file.
	if gotPCR {
		from := max(i, t.count()-maxTSBack/t.stride)
		for j := t.count() - 1; j >= from && j > i; j-- {
			if !t.packet(j, p[:]) {
				continue
			}
			if _, _, _, pcr, ok := tsFields(p[:]); ok {
				lastPCR = pcr
				break
			}
		}
		if lastPCR < startPCR {
			lastPCR += 1 << 33 * 300 // the 33-bit base wrapped
		}
		// 27 MHz ticks to nanoseconds: ×1000/27. (Ticks × time.Second
		// overflows past about 341 s of stream.)
		v.Duration = time.Duration((lastPCR - startPCR) * 1000 / 27)
	}
	if videoPID < 0 {
		return v, fmt.Errorf("%w: a transport stream without an H.264 video stream", ErrUnsupported)
	}
	return v, nil
}

// patProgramMap reads a program association section (2.4.4.3) for the first
// program's map PID, -1 without one.
func patProgramMap(payload []byte) int {
	sec, ok := psiSection(payload)
	if !ok || sec[0] != 0x00 {
		return -1
	}
	for e := sec[8 : len(sec)-4]; len(e) >= 4; e = e[4:] {
		if program := binary.BigEndian.Uint16(e); program != 0 { // 0 is the network PID
			return int(binary.BigEndian.Uint16(e[2:]) & 0x1FFF)
		}
	}
	return -1
}

// pmtVideoPID reads a program map section (2.4.4.8) for its first H.264
// stream (stream type 0x1B), -1 without one.
func pmtVideoPID(payload []byte) int {
	sec, ok := psiSection(payload)
	if !ok || sec[0] != 0x02 || len(sec) < 16 {
		return -1
	}
	info := int(binary.BigEndian.Uint16(sec[10:]) & 0x0FFF)
	if 12+info > len(sec)-4 {
		return -1
	}
	for e := sec[12+info : len(sec)-4]; len(e) >= 5; {
		typ, pid := e[0], int(binary.BigEndian.Uint16(e[1:])&0x1FFF)
		l := 5 + int(binary.BigEndian.Uint16(e[3:])&0x0FFF)
		if typ == 0x1B {
			return pid
		}
		if l > len(e) {
			break
		}
		e = e[l:]
	}
	return -1
}

// psiSection is the section that starts in a packet's payload: after the
// pointer field, as long as its section_length says, CRC included. Only a
// section within the one packet is read.
func psiSection(payload []byte) ([]byte, bool) {
	if len(payload) < 1 {
		return nil, false
	}
	at := 1 + int(payload[0])
	if at+3 > len(payload) {
		return nil, false
	}
	n := 3 + int(binary.BigEndian.Uint16(payload[at+1:])&0x0FFF)
	if n < 12 || at+n > len(payload) {
		return nil, false
	}
	return payload[at : at+n], true
}

// pesPayload is a PES packet's data after its header (2.4.3.6), nil for
// one without the start code.
func pesPayload(b []byte) []byte {
	if len(b) < 9 || b[0] != 0 || b[1] != 0 || b[2] != 1 {
		return nil
	}
	at := 9 + int(b[8])
	if at > len(b) {
		return nil
	}
	return b[at:]
}

// readH264Headers looks through an H.264 byte stream (Annex B) for the
// sequence parameter set and the MDPM, filling in v from those it hasn't
// got yet, and tells whether a slice (NAL unit type 1 or 5) has gone by.
// A NAL unit is read only once the next start code shows it whole.
func readH264Headers(es []byte, v *Video, gotSPS, gotMDPM bool) (sps, mdpm, slice bool) {
	for {
		i := bytes.Index(es, []byte{0, 0, 1})
		if i < 0 {
			return gotSPS, gotMDPM, slice
		}
		es = es[i+3:]
		end := bytes.Index(es, []byte{0, 0, 1})
		if end < 0 {
			return gotSPS, gotMDPM, slice // the last NAL unit, perhaps not all here yet
		}
		nal := bytes.TrimRight(es[:end], "\x00") // trailing zeros before a 4-byte start code
		if len(nal) == 0 {
			continue
		}
		switch nal[0] & 0x1F {
		case 1, 5:
			slice = true
		case 7:
			if !gotSPS {
				if w, h, ok := parseSPS(unescapeRBSP(nal[1:])); ok {
					v.Width, v.Height, gotSPS = w, h, true
				}
			}
		case 6:
			if !gotMDPM && readMDPM(unescapeRBSP(nal[1:]), v) {
				gotMDPM = true
			}
		}
	}
}

// unescapeRBSP removes the emulation prevention bytes (0x03 after two
// zeros) of a NAL unit (H.264 7.4.1).
func unescapeRBSP(b []byte) []byte {
	if !bytes.Contains(b, []byte{0, 0, 3}) {
		return b
	}
	out := make([]byte, 0, len(b))
	zeros := 0
	for _, c := range b {
		if zeros >= 2 && c == 3 {
			zeros = 0
			continue
		}
		if c == 0 {
			zeros++
		} else {
			zeros = 0
		}
		out = append(out, c)
	}
	return out
}

// readMDPM reads an SEI NAL unit's messages (H.264 7.3.2.3) for user data
// with the MDPM UUID, and its entries: a count, then each a tag and 4
// bytes, the tags ascending. False without MDPM.
func readMDPM(sei []byte, v *Video) bool {
	for len(sei) > 2 {
		typ, size := 0, 0
		for len(sei) > 0 && sei[0] == 0xFF {
			typ, sei = typ+255, sei[1:]
		}
		if len(sei) == 0 {
			return false
		}
		typ, sei = typ+int(sei[0]), sei[1:]
		for len(sei) > 0 && sei[0] == 0xFF {
			size, sei = size+255, sei[1:]
		}
		if len(sei) == 0 {
			return false
		}
		size, sei = size+int(sei[0]), sei[1:]
		if size > len(sei) {
			return false
		}
		msg := sei[:size]
		sei = sei[size:]
		if typ != 5 || len(msg) < len(mdpmUUID)+1 || !bytes.Equal(msg[:len(mdpmUUID)], mdpmUUID) {
			continue
		}
		entries := map[byte][4]byte{}
		n, e := int(msg[len(mdpmUUID)]), msg[len(mdpmUUID)+1:]
		last := -1
		for k := 0; k < n && len(e) >= 5; k++ {
			if int(e[0]) <= last {
				break // out of order: the rest isn't read
			}
			last = int(e[0])
			entries[e[0]] = [4]byte(e[1:5])
			e = e[5:]
		}
		applyMDPM(entries, v)
		return true
	}
	return false
}

// applyMDPM fills v from MDPM entries: 0x18 and 0x19, the recording date
// and time in BCD after a time zone byte; 0xb1 to 0xba, GPS; 0xe0 the
// make's code; 0xe4 to 0xe6 a Sony's model name.
func applyMDPM(e map[byte][4]byte, v *Video) {
	if a, ok := e[0x18]; ok {
		if b, ok := e[0x19]; ok {
			v.Created, v.OffsetKnown = mdpmTime(a, b)
		}
	}
	coord := func(ref, deg byte, neg byte) (float64, bool) {
		r, ok := e[ref]
		if !ok {
			return 0, false
		}
		var d float64
		for k, scale := range []float64{1, 60, 3600} {
			x, ok := e[deg+byte(k)]
			if !ok {
				return 0, false
			}
			num, den := binary.BigEndian.Uint16(x[:]), binary.BigEndian.Uint16(x[2:])
			if den == 0 {
				return 0, false
			}
			d += float64(num) / float64(den) / scale
		}
		if r[0] == neg {
			d = -d
		}
		return d, true
	}
	lat, ok1 := coord(0xb1, 0xb2, 'S')
	lon, ok2 := coord(0xb5, 0xb6, 'W')
	if ok1 && ok2 && lat >= -90 && lat <= 90 && lon >= -180 && lon <= 180 {
		g := &GPS{Latitude: lat, Longitude: lon}
		if x, ok := e[0xba]; ok {
			if num, den := binary.BigEndian.Uint16(x[:]), binary.BigEndian.Uint16(x[2:]); den != 0 {
				g.Altitude, g.HasAltitude = float64(num)/float64(den), true
				if ref, ok := e[0xb9]; ok && binary.BigEndian.Uint32(ref[:]) != 0 {
					g.Altitude = -g.Altitude
				}
			}
		}
		v.GPS = g
	}
	if mm, ok := e[0xe0]; ok {
		switch binary.BigEndian.Uint16(mm[:]) {
		case 0x0103:
			v.Make = "Panasonic"
		case 0x0108:
			v.Make = "Sony"
		case 0x1011:
			v.Make = "Canon"
		case 0x1104:
			v.Make = "JVC"
		}
	}
	if v.Make == "Sony" {
		var name []byte
		for tag := byte(0xe4); tag <= 0xe6; tag++ {
			x, ok := e[tag]
			if !ok {
				break
			}
			name = append(name, x[:]...)
		}
		if i := bytes.IndexByte(name, 0); i >= 0 {
			name = name[:i]
		}
		v.Model = string(bytes.TrimSpace(name))
	}
}

// mdpmTime reads the recording time: a time zone byte (0x20 west of UTC,
// hours in 0x1e, 0x01 half an hour more, 0x40 daylight saving time), the
// year in two BCD bytes, the month; then the day, hours, minutes and
// seconds. The offset is as written: daylight saving time is a flag beside
// it, not added to it.
func mdpmTime(a, b [4]byte) (time.Time, bool) {
	bcd := func(c byte) (int, bool) {
		hi, lo := int(c>>4), int(c&0x0F)
		return hi*10 + lo, hi < 10 && lo < 10
	}
	var f [7]int
	for k, c := range []byte{a[1], a[2], a[3], b[0], b[1], b[2], b[3]} {
		var ok bool
		if f[k], ok = bcd(c); !ok {
			return time.Time{}, false
		}
	}
	year, month, day, hour, minute, second := f[0]*100+f[1], f[2], f[3], f[4], f[5], f[6]
	tz := a[0]
	offset := int(tz>>1&0x0F)*3600 + int(tz&1)*1800
	if tz&0x20 != 0 {
		offset = -offset
	}
	sign := '+'
	if offset < 0 {
		sign = '-'
	}
	name := fmt.Sprintf("%c%02d:%02d", sign, abs(offset)/3600, abs(offset)%3600/60)
	at := time.Date(year, time.Month(month), day, hour, minute, second, 0, time.FixedZone(name, offset))
	if year == 0 || at.Year() != year || int(at.Month()) != month || at.Day() != day || at.Hour() != hour ||
		at.Minute() != minute || at.Second() != second {
		return time.Time{}, false
	}
	return at, true
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// parseSPS reads the picture size from a sequence parameter set's RBSP
// (H.264 7.3.2.1.1), after cropping (7.4.2.1.1).
func parseSPS(b []byte) (width, height int, ok bool) {
	r := &expGolomb{b: b}
	profile := r.bits(8)
	r.bits(16) // constraint flags, reserved bits, level
	r.ue()     // seq_parameter_set_id
	chroma := uint32(1)
	switch profile {
	case 100, 110, 122, 244, 44, 83, 86, 118, 128, 138, 139, 134, 135:
		if chroma = r.ue(); chroma == 3 {
			r.bits(1) // separate_colour_plane_flag
		}
		r.ue()              // bit_depth_luma_minus8
		r.ue()              // bit_depth_chroma_minus8
		r.bits(1)           // qpprime_y_zero_transform_bypass_flag
		if r.bits(1) == 1 { // seq_scaling_matrix_present_flag
			lists := 8
			if chroma == 3 {
				lists = 12
			}
			for k := 0; k < lists; k++ {
				if r.bits(1) == 1 {
					size := 16
					if k >= 6 {
						size = 64
					}
					last, next := int32(8), int32(8)
					for j := 0; j < size && !r.bad; j++ {
						if next != 0 {
							next = (last + r.se() + 256) % 256
						}
						if next != 0 {
							last = next
						}
					}
				}
			}
		}
	}
	r.ue()          // log2_max_frame_num_minus4
	switch r.ue() { // pic_order_cnt_type
	case 0:
		r.ue() // log2_max_pic_order_cnt_lsb_minus4
	case 1:
		r.bits(1) // delta_pic_order_always_zero_flag
		r.se()
		r.se()
		cycle := r.ue()
		for k := uint32(0); k < cycle && k < 256 && !r.bad; k++ {
			r.se()
		}
	}
	r.ue()    // max_num_ref_frames
	r.bits(1) // gaps_in_frame_num_value_allowed_flag
	wMBs := r.ue() + 1
	hMapUnits := r.ue() + 1
	frameMBsOnly := r.bits(1)
	if frameMBsOnly == 0 {
		r.bits(1) // mb_adaptive_frame_field_flag
	}
	r.bits(1) // direct_8x8_inference_flag
	var cl, cr, ct, cb uint32
	if r.bits(1) == 1 { // frame_cropping_flag
		cl, cr, ct, cb = r.ue(), r.ue(), r.ue(), r.ue()
	}
	if r.bad || wMBs > 1<<12 || hMapUnits > 1<<12 {
		return 0, 0, false
	}
	// The crop unit (7-19 to 7-22).
	unitX, unitY := uint32(1), 2-frameMBsOnly
	switch chroma {
	case 1:
		unitX, unitY = 2, 2*(2-frameMBsOnly)
	case 2:
		unitX, unitY = 2, 2-frameMBsOnly
	}
	w := int64(wMBs)*16 - int64(unitX)*(int64(cl)+int64(cr))
	h := int64(2-frameMBsOnly)*int64(hMapUnits)*16 - int64(unitY)*(int64(ct)+int64(cb))
	if w <= 0 || h <= 0 {
		return 0, 0, false
	}
	return int(w), int(h), true
}

// expGolomb reads bits, most significant first, and Exp-Golomb codes
// (H.264 9.1).
type expGolomb struct {
	b   []byte
	pos int
	bad bool
}

func (r *expGolomb) bits(n int) uint32 {
	var v uint32
	for k := 0; k < n; k++ {
		if r.pos >= 8*len(r.b) {
			r.bad = true
			return 0
		}
		v = v<<1 | uint32(r.b[r.pos/8]>>(7-r.pos%8)&1)
		r.pos++
	}
	return v
}

func (r *expGolomb) ue() uint32 {
	zeros := 0
	for r.bits(1) == 0 {
		if r.bad || zeros > 31 {
			r.bad = true
			return 0
		}
		zeros++
	}
	return 1<<zeros - 1 + r.bits(zeros)
}

func (r *expGolomb) se() int32 {
	k := r.ue()
	if k%2 == 1 {
		return int32((k + 1) / 2)
	}
	return -int32(k / 2)
}
