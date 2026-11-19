package mediameta

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"strings"
	"time"
	"unicode/utf16"
)

// ASF (Advanced Systems Format): WMV and WMA files, as Windows Media and
// many cameras and phones of the 2000s wrote them. Read only. The file
// starts with a Header Object holding:
//   - the File Properties Object: the creation date (a FILETIME, UTC),
//     the play duration and the preroll;
//   - a Stream Properties Object per stream: a video stream's size;
//   - the Content Description Object: the title and description;
//   - the Extended Content Description Object: named values, among them
//     WM/EncodingTime, the date the file was encoded, and the title and
//     description where a writer puts them here.
//
// The duration is the play duration less the preroll, as players show it
// and ffprobe gives it. ExifTool gives the play duration itself.

// errASFReadOnly is the error of a write to an ASF file.
var errASFReadOnly = fmt.Errorf("%w: an ASF (WMV, WMA) file is read only", ErrUnsupported)

// maxASFHeader caps the Header Object read whole.
const maxASFHeader = 4 << 20

// ASF object GUIDs, as they're stored: the first three fields little-endian.
var (
	asfHeaderID         = asfGUID("75B22630-668E-11CF-A6D9-00AA0062CE6C")
	asfFilePropertiesID = asfGUID("8CABDCA1-A947-11CF-8EE4-00C00C205365")
	asfStreamPropsID    = asfGUID("B7DC0791-A9B7-11CF-8EE6-00C00C205365")
	asfContentDescID    = asfGUID("75B22633-668E-11CF-A6D9-00AA0062CE6C")
	asfExtContentDescID = asfGUID("D2D0A440-E307-11D2-97F0-00A0C95EA850")
	asfVideoMediaID     = asfGUID("BC19EFC0-5B4D-11CF-A8FD-00805F5C442B")
)

// asfGUID is a GUID's 16 bytes as ASF stores them.
func asfGUID(s string) [16]byte {
	var g [16]byte
	h := strings.ReplaceAll(s, "-", "")
	var b [16]byte
	for i := range b {
		var v byte
		for _, c := range []byte(h[2*i : 2*i+2]) {
			v <<= 4
			switch {
			case c >= '0' && c <= '9':
				v |= c - '0'
			default:
				v |= c - 'A' + 10
			}
		}
		b[i] = v
	}
	// Data1 (4 bytes), Data2 and Data3 (2 each) little-endian; Data4 as is.
	g[0], g[1], g[2], g[3] = b[3], b[2], b[1], b[0]
	g[4], g[5] = b[5], b[4]
	g[6], g[7] = b[7], b[6]
	copy(g[8:], b[8:])
	return g
}

func isASF(r io.ReaderAt, size int64) bool {
	var h [16]byte
	if size < 30 {
		return false
	}
	if _, err := r.ReadAt(h[:], 0); err != nil {
		return false
	}
	return h == asfHeaderID
}

// filetimeEpoch is a FILETIME's zero: 1601-01-01 UTC, in Unix seconds.
const filetimeEpoch = -11644473600

// fromFILETIME is a FILETIME (100 ns since 1601, UTC) as a time; zero for
// 0 or a value past year 9999.
func fromFILETIME(v uint64) time.Time {
	if v == 0 || v > 2650467743999999999 {
		return time.Time{}
	}
	return time.Unix(filetimeEpoch+int64(v/1e7), int64(v%1e7)*100).UTC()
}

// readASFVideo reads an ASF file's recording date, duration, video size,
// title and description.
func readASFVideo(r io.ReaderAt, size int64) (Video, error) {
	v := Video{Orientation: 1}
	if !isASF(r, size) {
		return v, formatError("ASF")
	}
	var h [30]byte
	if _, err := r.ReadAt(h[:], 0); err != nil {
		return v, fmt.Errorf("%w: reading the ASF header: %v", ErrMalformed, err)
	}
	n := binary.LittleEndian.Uint64(h[16:])
	if n < 30 || n > uint64(size) {
		return v, fmt.Errorf("%w: an ASF header of %d bytes", ErrMalformed, n)
	}
	if n > maxASFHeader {
		return v, fmt.Errorf("%w: an ASF header of %d bytes", ErrUnsupported, n)
	}
	b := make([]byte, n)
	if _, err := r.ReadAt(b, 0); err != nil {
		return v, fmt.Errorf("%w: reading the ASF header: %v", ErrMalformed, err)
	}
	var ext asfExtended
	for at := 30; at+24 <= len(b); {
		var id [16]byte
		copy(id[:], b[at:])
		on := binary.LittleEndian.Uint64(b[at+16:])
		if on < 24 || on > uint64(len(b)-at) {
			break // a broken object ends the header's reading
		}
		body := b[at+24 : at+int(on)]
		switch id {
		case asfFilePropertiesID:
			readASFFileProperties(body, &v)
		case asfStreamPropsID:
			if v.Width == 0 {
				v.Width, v.Height = asfVideoSize(body)
			}
		case asfContentDescID:
			v.Title, v.Description = asfContentDescription(body)
		case asfExtContentDescID:
			ext = readASFExtended(body)
		}
		at += int(on)
	}
	if v.Created.IsZero() && !ext.encoded.IsZero() {
		v.Created, v.OffsetKnown = ext.encoded, true
	}
	v.Title, v.Description = first(v.Title, ext.title), first(v.Description, ext.description)
	return v, nil
}

// readASFFileProperties reads the File Properties Object's creation date
// and duration into v. With the broadcast flag set, they aren't valid.
func readASFFileProperties(b []byte, v *Video) {
	// File ID (16), file size (8), creation date (8), data packets (8),
	// play duration (8), send duration (8), preroll (8, ms), flags (4).
	if len(b) < 68 {
		return
	}
	if binary.LittleEndian.Uint32(b[64:])&1 != 0 {
		return // broadcast: the dates and durations aren't set
	}
	if t := fromFILETIME(binary.LittleEndian.Uint64(b[24:])); !t.IsZero() {
		v.Created, v.OffsetKnown = t, true
	}
	play := binary.LittleEndian.Uint64(b[40:])    // 100 ns
	preroll := binary.LittleEndian.Uint64(b[56:]) // ms
	// In 100 ns units; a duration past what a time.Duration holds isn't
	// one.
	if play/1e4 > preroll {
		if d := play - preroll*1e4; d <= math.MaxInt64/100 {
			v.Duration = time.Duration(d) * 100
		}
	}
}

// asfVideoSize is a video Stream Properties Object's encoded image size,
// 0, 0 for another stream.
func asfVideoSize(b []byte) (w, h int) {
	// Stream type (16), error correction type (16), time offset (8),
	// type-specific data length (4), error correction data length (4),
	// flags (2), reserved (4), then the type-specific data: for video,
	// the encoded width (4) and height (4).
	if len(b) < 54+8 || !bytes.Equal(b[:16], asfVideoMediaID[:]) {
		return 0, 0
	}
	if binary.LittleEndian.Uint32(b[40:]) < 8 {
		return 0, 0
	}
	w, h = int(binary.LittleEndian.Uint32(b[54:])), int(binary.LittleEndian.Uint32(b[58:]))
	if w <= 0 || h <= 0 || w > 1<<16 || h > 1<<16 {
		return 0, 0
	}
	return w, h
}

// asfContentDescription is the Content Description Object's title and
// description: five lengths (title, author, copyright, description,
// rating), then the strings, UTF-16LE.
func asfContentDescription(b []byte) (title, desc string) {
	if len(b) < 10 {
		return "", ""
	}
	var lens [5]int
	for i := range lens {
		lens[i] = int(binary.LittleEndian.Uint16(b[2*i:]))
	}
	at := 10
	var s [5]string
	for i, n := range lens {
		if at+n > len(b) {
			break
		}
		s[i] = utf16LEString(b[at : at+n])
		at += n
	}
	return s[0], s[3]
}

// asfExtended is what's read of the Extended Content Description
// Object's named values: WM/EncodingTime, the date the file was encoded
// (a FILETIME, a QWORD value), and Title and Description, which some
// writers (ffmpeg's among them) put here rather than in the Content
// Description Object.
type asfExtended struct {
	encoded            time.Time
	title, description string
}

// readASFExtended reads the Extended Content Description Object: a count,
// then each value's name (UTF-16LE), type and data.
func readASFExtended(b []byte) asfExtended {
	var e asfExtended
	if len(b) < 2 {
		return e
	}
	count := int(binary.LittleEndian.Uint16(b))
	at := 2
	for range count {
		if at+2 > len(b) {
			break
		}
		nn := int(binary.LittleEndian.Uint16(b[at:]))
		if at+2+nn+4 > len(b) {
			break
		}
		name := utf16LEString(b[at+2 : at+2+nn])
		typ := binary.LittleEndian.Uint16(b[at+2+nn:])
		vn := int(binary.LittleEndian.Uint16(b[at+4+nn:]))
		val := at + 6 + nn
		if val+vn > len(b) {
			break
		}
		data := b[val : val+vn]
		switch {
		case name == "WM/EncodingTime" && typ == 4 && vn == 8:
			e.encoded = fromFILETIME(binary.LittleEndian.Uint64(data))
		case name == "Title" && typ == 0:
			e.title = first(e.title, utf16LEString(data))
		case name == "Description" && typ == 0:
			e.description = first(e.description, utf16LEString(data))
		}
		at = val + vn
	}
	return e
}

// utf16LEString decodes UTF-16LE text, up to a NUL, trimmed.
func utf16LEString(b []byte) string {
	u := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		c := binary.LittleEndian.Uint16(b[i:])
		if c == 0 {
			break
		}
		u = append(u, c)
	}
	return strings.TrimSpace(string(utf16.Decode(u)))
}
