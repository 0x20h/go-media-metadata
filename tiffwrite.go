package mediameta

import (
	"encoding/binary"
	"fmt"
	"math"
	"sort"
	"strconv"
	"time"
)

// ExifEdit is a change to Exif data. A field left at its zero value
// leaves that part of the data as it is.
type ExifEdit struct {
	// Orientation sets IFD0's Orientation tag (1 to 8).
	Orientation Orientation

	// DateTimeOriginal sets the DateTimeOriginal tag to its clock time,
	// and SubSecTimeOriginal to its fraction of a second (removed for a
	// whole second). With OffsetKnown, OffsetTimeOriginal is set to its
	// zone's offset; without, an OffsetTimeOriginal tag is removed, so no
	// stale offset is read with the new time.
	DateTimeOriginal *time.Time
	OffsetKnown      bool

	// GPS sets the position: latitude and longitude with their
	// references, and the altitude with its reference when HasAltitude
	// (an altitude without one is removed). RemoveGPS removes those tags
	// instead; the GPS IFD's other tags stay.
	GPS       *GPS
	RemoveGPS bool
}

// Exif and GPS tags written here, besides those read.
const (
	tagGPSVersionID = 0
	tagMakerNote    = 0x927C
	tagIFD1Thumb    = 0x0201 // JPEGInterchangeFormat
)

// EditExif returns TIFF data (the TIFF header first, as in a JPEG's Exif
// APP1 segment after "Exif\0\0", or a HEIF Exif item after its offset)
// with edit applied. Every other byte keeps its offset: unknown tags, the
// thumbnail and the maker note, whose own offsets point into the data,
// stay valid. A value that fits where the old one was is written there;
// a longer one, and an IFD that needs a new entry, go at the end of the
// data, and what pointed to them is updated. A removed tag's entry is
// taken out of its IFD in place.
//
// With empty data, EditExif makes new TIFF data (big-endian) holding the
// edit. Data that isn't TIFF, or whose IFDs lie outside it, is refused
// with ErrMalformed rather than rewritten.
//
// The result can be longer than the input: a JPEG's APP1 segment holds at
// most 65533 bytes.
func EditExif(tiff []byte, edit ExifEdit) ([]byte, error) {
	if edit.Orientation != 0 && !edit.Orientation.Valid() {
		return nil, fmt.Errorf("mediameta: orientation %d: not 1 to 8", uint8(edit.Orientation))
	}
	if edit.GPS != nil && edit.RemoveGPS {
		return nil, fmt.Errorf("mediameta: GPS both set and removed")
	}
	if g := edit.GPS; g != nil {
		if !(math.Abs(g.Latitude) <= 90) || !(math.Abs(g.Longitude) <= 180) ||
			g.HasAltitude && !(math.Abs(g.Altitude) < 1e6) {
			return nil, fmt.Errorf("mediameta: GPS position %v, %v (%v m) out of range", g.Latitude, g.Longitude, g.Altitude)
		}
	}
	if d := edit.DateTimeOriginal; d != nil && (d.Year() < 0 || d.Year() > 9999) {
		return nil, fmt.Errorf("mediameta: date %v out of range", d)
	}
	if len(tiff) == 0 {
		tiff = []byte{'M', 'M', 0, 42, 0, 0, 0, 8, 0, 0, 0, 0, 0, 0}
	}
	w, err := newTIFFWriter(tiff)
	if err != nil {
		return nil, err
	}
	var exif, gps []tiffChange
	if d := edit.DateTimeOriginal; d != nil {
		exif = append(exif, w.asciiChange(tagDateTimeOriginal, d.Format(exifDateLayout)))
		if ns := d.Nanosecond(); ns != 0 {
			sub := strconv.FormatInt(int64(ns)+1e9, 10)[1:]
			for sub[len(sub)-1] == '0' {
				sub = sub[:len(sub)-1]
			}
			exif = append(exif, w.asciiChange(tagSubSecTimeOriginal, sub))
		} else {
			exif = append(exif, tiffChange{tag: tagSubSecTimeOriginal, remove: true})
		}
		if edit.OffsetKnown {
			exif = append(exif, w.asciiChange(tagOffsetTimeOriginal, d.Format("-07:00")))
		} else {
			exif = append(exif, tiffChange{tag: tagOffsetTimeOriginal, remove: true})
		}
	}
	if g := edit.GPS; g != nil {
		gps = append(gps,
			w.asciiChange(tagGPSLatitudeRef, pick(g.Latitude < 0, "S", "N")),
			w.rationalChange(tagGPSLatitude, dms(math.Abs(g.Latitude))),
			w.asciiChange(tagGPSLongitudeRef, pick(g.Longitude < 0, "W", "E")),
			w.rationalChange(tagGPSLongitude, dms(math.Abs(g.Longitude))),
		)
		if g.HasAltitude {
			gps = append(gps,
				tiffChange{tag: tagGPSAltitudeRef, typ: typeByte, count: 1, value: []byte{pickByte(g.Altitude < 0)}},
				w.rationalChange(tagGPSAltitude, [][2]uint32{{uint32(math.Round(math.Abs(g.Altitude) * 1000)), 1000}}),
			)
		} else {
			gps = append(gps, tiffChange{tag: tagGPSAltitudeRef, remove: true}, tiffChange{tag: tagGPSAltitude, remove: true})
		}
		gps = append(gps, tiffChange{tag: tagGPSVersionID, typ: typeByte, count: 4, value: []byte{2, 3, 0, 0}, keep: true})
	}
	if edit.RemoveGPS {
		for _, tag := range []uint16{tagGPSLatitudeRef, tagGPSLatitude, tagGPSLongitudeRef, tagGPSLongitude, tagGPSAltitudeRef, tagGPSAltitude} {
			gps = append(gps, tiffChange{tag: tag, remove: true})
		}
	}
	var ifd0 []tiffChange
	if edit.Orientation != 0 {
		ifd0 = append(ifd0, tiffChange{tag: tagOrientation, typ: typeShort, count: 1, value: w.order.AppendUint16(nil, uint16(edit.Orientation))})
	}
	// The sub-IFDs' pointers are checked against the data as it is,
	// before anything is appended: one that leads past its end would
	// otherwise land in what's appended.
	// One IFD can't be two: a sub-IFD that is IFD0 or the other sub-IFD
	// is refused.
	ifds := map[uint32]bool{w.first: true}
	for _, ptr := range []uint16{tagExifIFD, tagGPSIFD} {
		if off, has := w.pointer(ptr); has {
			if _, err := w.entries(off); err != nil {
				return nil, err
			}
			if ifds[off] {
				return nil, fmt.Errorf("%w: two IFDs at %d", ErrMalformed, off)
			}
			ifds[off] = true
		}
	}
	// The sub-IFDs first: where one moves or is made, IFD0's pointer to
	// it is one more change of IFD0.
	for _, sub := range []struct {
		ptr     uint16
		changes []tiffChange
	}{{tagExifIFD, exif}, {tagGPSIFD, gps}} {
		if len(sub.changes) == 0 {
			continue
		}
		off, has := w.pointer(sub.ptr)
		if !has && allRemovals(sub.changes) {
			continue
		}
		at, err := w.apply(off, has, sub.changes)
		if err != nil {
			return nil, err
		}
		if !has || at != off {
			ifd0 = append(ifd0, tiffChange{tag: sub.ptr, typ: typeLong, count: 1, value: w.order.AppendUint32(nil, at)})
		}
	}
	if len(ifd0) > 0 {
		at, err := w.apply(w.first, true, ifd0)
		if err != nil {
			return nil, err
		}
		w.order.PutUint32(w.data[4:], at)
	}
	return w.data, nil
}

func pick(b bool, yes, no string) string {
	if b {
		return yes
	}
	return no
}

func pickByte(b bool) byte {
	if b {
		return 1
	}
	return 0
}

// dms is a coordinate in degrees as degrees, minutes and seconds, the
// seconds to a ten-thousandth.
func dms(deg float64) [][2]uint32 {
	total := int64(math.Round(deg * 3600 * 10000)) // ten-thousandths of a second
	d := total / (3600 * 10000)
	total -= d * 3600 * 10000
	m := total / (60 * 10000)
	s := total - m*60*10000
	return [][2]uint32{{uint32(d), 1}, {uint32(m), 1}, {uint32(s), 10000}}
}

func allRemovals(cs []tiffChange) bool {
	for _, c := range cs {
		if !c.remove {
			return false
		}
	}
	return true
}

// tiffChange sets or removes one tag of an IFD. keep sets it only where
// the IFD doesn't have it.
type tiffChange struct {
	tag    uint16
	typ    uint16
	count  uint32
	value  []byte
	remove bool
	keep   bool
}

// tiffOrder reads and appends in one byte order.
type tiffOrder interface {
	binary.ByteOrder
	binary.AppendByteOrder
}

// tiffWriter edits TIFF data in place, appending what doesn't fit. Bytes
// an edit leaves unused (an old value, an IFD that moved) are zeroed, so
// a removed value isn't left in the data to be found.
type tiffWriter struct {
	data  []byte
	order tiffOrder
	first uint32
	// spans are the IFDs read and the out-of-line values their entries
	// point at: bytes are overwritten or zeroed only where they belong to
	// one thing, never where a value overlaps an IFD or another value.
	spans []tiffSpan
}

type tiffSpan struct {
	start, end uint64
	ifd        bool
	// added is a span this edit appended: no old entry's pointer can
	// claim it, even one that led past the end of the data before.
	added bool
}

// exclusive reports whether [start, end) is exactly one span of its kind
// and overlaps no other.
func (w *tiffWriter) exclusive(start, end uint64, ifd bool) bool {
	if end > uint64(len(w.data)) || start >= end {
		return false
	}
	n := 0
	for _, s := range w.spans {
		if s.start >= end || start >= s.end {
			continue
		}
		if s.added || s.ifd != ifd || s.start != start || s.end != end {
			return false
		}
		n++
	}
	return n == 1
}

// drop forgets a span that was zeroed or replaced.
func (w *tiffWriter) drop(start, end uint64, ifd bool) {
	for i, s := range w.spans {
		if s.start == start && s.end == end && s.ifd == ifd {
			w.spans = append(w.spans[:i], w.spans[i+1:]...)
			return
		}
	}
}

// valueSpan is an entry's out-of-line value range; ok is false for a
// value in the entry.
func (w *tiffWriter) valueSpan(e tiffRawEntry) (start, end uint64, ok bool) {
	size := e.size()
	if size <= 4 {
		return 0, 0, false
	}
	at := uint64(w.order.Uint32(e.raw[8:]))
	return at, at + size, true
}

// release zeroes an entry's out-of-line value when it's that entry's
// alone.
func (w *tiffWriter) release(e tiffRawEntry) {
	start, end, ok := w.valueSpan(e)
	if !ok || !w.exclusive(start, end, false) {
		return
	}
	clear(w.data[start:end])
	w.drop(start, end, false)
}

func newTIFFWriter(tiff []byte) (*tiffWriter, error) {
	t, first, ok := newTIFF(tiff)
	if !ok {
		return nil, fmt.Errorf("%w: Exif data that isn't TIFF", ErrMalformed)
	}
	w := &tiffWriter{data: append([]byte(nil), tiff...), order: t.order.(tiffOrder), first: first}
	if _, err := w.entries(first); err != nil {
		return nil, err
	}
	// Record the header, and the IFDs this package knows of with their
	// values. The header counts as structure: nothing is written over it.
	w.spans = append(w.spans, tiffSpan{start: 0, end: 8, ifd: true})
	seen := map[uint32]bool{}
	var walk func(off uint32, depth int)
	walk = func(off uint32, depth int) {
		if seen[off] || depth > 4 {
			return
		}
		seen[off] = true
		es, err := w.entries(off)
		if err != nil {
			return
		}
		w.spans = append(w.spans, tiffSpan{start: uint64(off), end: uint64(off) + 2 + 12*uint64(len(es)) + 4, ifd: true})
		for _, e := range es {
			if start, end, ok := w.valueSpan(e); ok && end <= uint64(len(w.data)) {
				w.spans = append(w.spans, tiffSpan{start: start, end: end})
			}
			if e.tag == tagExifIFD || e.tag == tagGPSIFD || e.tag == 0xA005 {
				walk(w.order.Uint32(e.raw[8:]), depth+1)
			}
		}
		if next, ok := w.next(off, len(es)); ok && next != 0 {
			walk(next, depth+1)
		}
	}
	walk(first, 0)
	return w, nil
}

// tiffRawEntry is one IFD entry as it is in the data.
type tiffRawEntry struct {
	tag, typ uint16
	count    uint32
	raw      [12]byte
}

func (e tiffRawEntry) size() uint64 { return uint64(typeSize(e.typ)) * uint64(e.count) }

// entries reads the IFD at off, which must lie inside the data.
func (w *tiffWriter) entries(off uint32) ([]tiffRawEntry, error) {
	if off < 8 || uint64(off)+2 > uint64(len(w.data)) {
		return nil, fmt.Errorf("%w: an IFD at %d, outside the Exif data", ErrMalformed, off)
	}
	n := int(w.order.Uint16(w.data[off:]))
	if n > maxIFDEntries || uint64(off)+2+12*uint64(n)+4 > uint64(len(w.data)) {
		return nil, fmt.Errorf("%w: an IFD of %d entries at %d", ErrMalformed, n, off)
	}
	out := make([]tiffRawEntry, n)
	for i := range n {
		e := &out[i]
		copy(e.raw[:], w.data[int(off)+2+12*i:])
		e.tag, e.typ, e.count = w.order.Uint16(e.raw[:]), w.order.Uint16(e.raw[2:]), w.order.Uint32(e.raw[4:])
	}
	return out, nil
}

// next is the offset of the IFD after the one at off, of n entries.
func (w *tiffWriter) next(off uint32, n int) (uint32, bool) {
	at := uint64(off) + 2 + 12*uint64(n)
	if at+4 > uint64(len(w.data)) {
		return 0, false
	}
	return w.order.Uint32(w.data[at:]), true
}

// pointer is IFD0's pointer to a sub-IFD; has is false without one. A
// pointer that leads outside the data is refused when the IFD is edited.
func (w *tiffWriter) pointer(tag uint16) (uint32, bool) {
	es, _ := w.entries(w.first)
	for _, e := range es {
		if e.tag == tag && (e.typ == typeLong || e.typ == 13) && e.count == 1 {
			return w.order.Uint32(e.raw[8:]), true
		}
	}
	return 0, false
}

func (w *tiffWriter) asciiChange(tag uint16, s string) tiffChange {
	return tiffChange{tag: tag, typ: typeASCII, count: uint32(len(s) + 1), value: append([]byte(s), 0)}
}

func (w *tiffWriter) rationalChange(tag uint16, vs [][2]uint32) tiffChange {
	var b []byte
	for _, v := range vs {
		b = w.order.AppendUint32(b, v[0])
		b = w.order.AppendUint32(b, v[1])
	}
	return tiffChange{tag: tag, typ: typeRational, count: uint32(len(vs)), value: b}
}

// appendValue puts b at the end of the data, on a word boundary, and
// returns its offset.
func (w *tiffWriter) appendValue(b []byte) (uint32, error) {
	if len(w.data)%2 == 1 {
		w.data = append(w.data, 0)
	}
	at := len(w.data)
	if uint64(at)+uint64(len(b)) > math.MaxUint32 {
		return 0, fmt.Errorf("%w: Exif data past 4 GB", ErrUnsupported)
	}
	w.data = append(w.data, b...)
	return uint32(at), nil
}

// apply makes changes to the IFD at off (a new one when !has) and returns
// where the IFD now is: where it was, unless it needed more entries.
func (w *tiffWriter) apply(off uint32, has bool, changes []tiffChange) (uint32, error) {
	var es []tiffRawEntry
	if has {
		var err error
		if es, err = w.entries(off); err != nil {
			return 0, err
		}
	}
	oldN := len(es)
	ifdStart, ifdEnd := uint64(off), uint64(off)+2+12*uint64(oldN)+4
	if has && !w.exclusive(ifdStart, ifdEnd, true) {
		return 0, fmt.Errorf("%w: an IFD at %d that overlaps other data", ErrUnsupported, off)
	}
	// Entries go in ascending tag order (TIFF 6.0, section 2); data that
	// has them out of order gets them sorted, which moves no value.
	sort.SliceStable(es, func(i, j int) bool { return es[i].tag < es[j].tag })
	var next uint32
	if has {
		next, _ = w.next(off, oldN)
	}
	for _, c := range changes {
		i := sort.Search(len(es), func(i int) bool { return es[i].tag >= c.tag })
		found := i < len(es) && es[i].tag == c.tag
		switch {
		case c.remove:
			if found {
				w.release(es[i])
				es = append(es[:i], es[i+1:]...)
			}
			continue
		case found && c.keep:
			continue
		}
		e := tiffRawEntry{tag: c.tag, typ: c.typ, count: c.count}
		w.order.PutUint16(e.raw[:], c.tag)
		w.order.PutUint16(e.raw[2:], c.typ)
		w.order.PutUint32(e.raw[4:], c.count)
		if len(c.value) <= 4 {
			copy(e.raw[8:], c.value)
		} else {
			// Where the old value was, if it's at least as long and no
			// other entry's; else at the end.
			placed := false
			if found && es[i].size() > 4 && es[i].size() >= uint64(len(c.value)) {
				start, end, _ := w.valueSpan(es[i])
				if w.exclusive(start, end, false) {
					copy(w.data[start:], c.value)
					clear(w.data[start+uint64(len(c.value)) : end])
					w.order.PutUint32(e.raw[8:], uint32(start))
					placed = true
				}
			}
			if !placed {
				if found {
					w.release(es[i])
				}
				at, err := w.appendValue(c.value)
				if err != nil {
					return 0, err
				}
				w.order.PutUint32(e.raw[8:], at)
				w.spans = append(w.spans, tiffSpan{start: uint64(at), end: uint64(at) + uint64(len(c.value)), added: true})
			}
		}
		if found {
			es[i] = e
		} else {
			es = append(es[:i], append([]tiffRawEntry{e}, es[i:]...)...)
		}
	}
	ifd := w.order.AppendUint16(nil, uint16(len(es)))
	for _, e := range es {
		ifd = append(ifd, e.raw[:]...)
	}
	ifd = w.order.AppendUint32(ifd, next)
	if has && len(es) <= oldN {
		// In place; the entries no longer used are cleared.
		copy(w.data[off:], ifd)
		clear(w.data[ifdStart+uint64(len(ifd)) : ifdEnd])
		return off, nil
	}
	at, err := w.appendValue(ifd)
	if err != nil {
		return 0, err
	}
	w.spans = append(w.spans, tiffSpan{start: uint64(at), end: uint64(at) + uint64(len(ifd)), ifd: true, added: true})
	if has {
		clear(w.data[ifdStart:ifdEnd])
		w.drop(ifdStart, ifdEnd, true)
	}
	return at, nil
}
