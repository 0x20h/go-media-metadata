package mediameta

import (
	"encoding/binary"
	"math"
	"strconv"
	"strings"
	"time"
)

// Exif is what this package reads from an image's Exif metadata (CIPA
// DC-008): the fields a photo library sorts, places and shows images by.
// A field the file doesn't have, or has in a form this package doesn't
// accept, is left at its zero value.
type Exif struct {
	// Orientation is IFD0's Orientation tag, 0 without a valid one (1 to
	// 8). For a JPEG it's how the image is shown; a HEIF image is shown by
	// its irot and imir properties instead (see ReadOrientation).
	Orientation Orientation

	// DateTimeOriginal is when the photo was taken: the DateTimeOriginal
	// tag, with SubSecTimeOriginal's fraction of a second. It's the zero
	// time without one.
	//
	// With an OffsetTimeOriginal tag, OffsetKnown is true and the time is
	// in a fixed zone of that offset. Without one the camera's clock
	// time is all the file says: it's returned as that clock time in
	// time.UTC, with OffsetKnown false, and is not a UTC time.
	DateTimeOriginal time.Time
	OffsetKnown      bool

	// GPS is where the photo was taken, nil without a valid position.
	GPS *GPS
}

// GPS is a position from the Exif GPS IFD.
type GPS struct {
	// Latitude and Longitude are in decimal degrees: north and east are
	// positive.
	Latitude, Longitude float64
	// Altitude is in metres above sea level (negative below), when
	// HasAltitude.
	Altitude    float64
	HasAltitude bool
}

// Exif and TIFF tags read here.
const (
	tagExifIFD            = 0x8769
	tagGPSIFD             = 0x8825
	tagDateTimeOriginal   = 0x9003
	tagOffsetTimeOriginal = 0x9011
	tagSubSecTimeOriginal = 0x9291

	tagGPSLatitudeRef  = 1
	tagGPSLatitude     = 2
	tagGPSLongitudeRef = 3
	tagGPSLongitude    = 4
	tagGPSAltitudeRef  = 5
	tagGPSAltitude     = 6
)

// TIFF field types read here, and the size of one value of each.
const (
	typeByte     = 1
	typeASCII    = 2
	typeShort    = 3
	typeLong     = 4
	typeRational = 5
	typeUndef    = 7
)

func typeSize(t uint16) int {
	switch t {
	case typeByte, typeASCII, typeUndef:
		return 1
	case typeShort:
		return 2
	case typeLong:
		return 4
	case typeRational:
		return 8
	}
	return 0
}

// maxIFDEntries caps the entries read from one IFD; real ones have a few
// dozen.
const maxIFDEntries = 1000

// tiffReader reads IFDs from TIFF data (the TIFF header first): every
// offset is checked against the data, so hostile data reads as fields
// that aren't there, never out of bounds.
type tiffReader struct {
	data  []byte
	order binary.ByteOrder
}

// newTIFF reads the TIFF header; ok is false for data that doesn't start
// with one.
func newTIFF(data []byte) (tiffReader, uint32, bool) {
	if len(data) < 8 {
		return tiffReader{}, 0, false
	}
	var order binary.ByteOrder
	switch string(data[:4]) {
	case "II*\x00":
		order = binary.LittleEndian
	case "MM\x00*":
		order = binary.BigEndian
	default:
		return tiffReader{}, 0, false
	}
	return tiffReader{data: data, order: order}, order.Uint32(data[4:]), true
}

// field is one IFD entry whose value lies within the data.
type field struct {
	typ   uint16
	count int
	value []byte
}

// ifd reads the IFD at off into a map by tag; nil for an offset outside
// the data. Entries whose value doesn't fit are left out; the first of a
// repeated tag wins.
func (t tiffReader) ifd(off uint32) map[uint16]field {
	if off < 8 || uint64(off)+2 > uint64(len(t.data)) {
		return nil
	}
	n := int(t.order.Uint16(t.data[off:]))
	n = min(n, maxIFDEntries, (len(t.data)-int(off)-2)/12)
	out := make(map[uint16]field, n)
	for i := range n {
		e := t.data[int(off)+2+12*i:]
		tag, typ, count := t.order.Uint16(e), t.order.Uint16(e[2:]), t.order.Uint32(e[4:])
		if _, dup := out[tag]; dup {
			continue
		}
		size := uint64(typeSize(typ)) * uint64(count)
		if size == 0 {
			continue
		}
		var v []byte
		if size <= 4 {
			v = e[8 : 8+size]
		} else {
			at := uint64(t.order.Uint32(e[8:]))
			if at+size > uint64(len(t.data)) {
				continue
			}
			v = t.data[at : at+size]
		}
		out[tag] = field{typ: typ, count: int(count), value: v}
	}
	return out
}

// short is a SHORT field's first value.
func (t tiffReader) short(f field) (uint16, bool) {
	if f.typ != typeShort {
		return 0, false
	}
	return t.order.Uint16(f.value), true
}

// long is a LONG field's first value (or a SHORT's, which some writers
// use for IFD pointers).
func (t tiffReader) long(f field) (uint32, bool) {
	switch f.typ {
	case typeLong:
		return t.order.Uint32(f.value), true
	case typeShort:
		return uint32(t.order.Uint16(f.value)), true
	}
	return 0, false
}

// ascii is an ASCII field's text, up to its first NUL, trimmed of spaces.
func ascii(f field) (string, bool) {
	if f.typ != typeASCII && f.typ != typeUndef {
		return "", false
	}
	s := string(f.value)
	if i := strings.IndexByte(s, 0); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s), true
}

// rationals are a RATIONAL field's values; ok is false for a zero
// denominator.
func (t tiffReader) rationals(f field) ([]float64, bool) {
	if f.typ != typeRational {
		return nil, false
	}
	out := make([]float64, f.count)
	for i := range out {
		num, den := t.order.Uint32(f.value[8*i:]), t.order.Uint32(f.value[8*i+4:])
		if den == 0 {
			return nil, false
		}
		out[i] = float64(num) / float64(den)
	}
	return out, true
}

// parseExif reads the fields of Exif from TIFF data. Data that isn't
// TIFF gives an empty Exif.
func parseExif(data []byte) Exif {
	var x Exif
	t, first, ok := newTIFF(data)
	if !ok {
		return x
	}
	ifd0 := t.ifd(first)
	if f, ok := ifd0[tagOrientation]; ok {
		if v, ok := t.short(f); ok && Orientation(v).Valid() {
			x.Orientation = Orientation(v)
		}
	}
	if f, ok := ifd0[tagExifIFD]; ok {
		if off, ok := t.long(f); ok && off != first {
			x.DateTimeOriginal, x.OffsetKnown = t.dateTimeOriginal(t.ifd(off))
		}
	}
	if f, ok := ifd0[tagGPSIFD]; ok {
		if off, ok := t.long(f); ok && off != first {
			x.GPS = t.gps(t.ifd(off))
		}
	}
	return x
}

// exifDateLayout is the layout of Exif date and time tags.
const exifDateLayout = "2006:01:02 15:04:05"

// dateTimeOriginal reads DateTimeOriginal, SubSecTimeOriginal and
// OffsetTimeOriginal from the Exif IFD.
func (t tiffReader) dateTimeOriginal(exif map[uint16]field) (time.Time, bool) {
	s, ok := ascii(exif[tagDateTimeOriginal])
	if !ok || len(s) < len(exifDateLayout) {
		return time.Time{}, false
	}
	at, err := time.ParseInLocation(exifDateLayout, s[:len(exifDateLayout)], time.UTC)
	if err != nil {
		return time.Time{}, false
	}
	if sub, ok := ascii(exif[tagSubSecTimeOriginal]); ok && sub != "" && len(sub) <= 9 {
		if n, err := strconv.ParseUint(sub, 10, 32); err == nil {
			at = at.Add(time.Duration(n) * time.Duration(math.Pow10(9-len(sub))))
		}
	}
	off, ok := ascii(exif[tagOffsetTimeOriginal])
	if !ok {
		return at, false
	}
	secs, ok := parseOffset(off)
	if !ok {
		return at, false
	}
	return time.Date(at.Year(), at.Month(), at.Day(), at.Hour(), at.Minute(), at.Second(), at.Nanosecond(),
		time.FixedZone(off, secs)), true
}

// parseOffset reads an Exif time offset, "+HH:MM" or "-HH:MM", as seconds
// east of UTC.
func parseOffset(s string) (int, bool) {
	if len(s) != 6 || (s[0] != '+' && s[0] != '-') || s[3] != ':' {
		return 0, false
	}
	h, err1 := strconv.Atoi(s[1:3])
	m, err2 := strconv.Atoi(s[4:6])
	if err1 != nil || err2 != nil || h > 14 || m > 59 || s[1] == '+' || s[1] == '-' || s[4] == '+' || s[4] == '-' {
		return 0, false
	}
	secs := h*3600 + m*60
	if s[0] == '-' {
		secs = -secs
	}
	return secs, true
}

// gps reads a position from the GPS IFD: latitude and longitude as
// degrees, minutes and seconds with their N/S and E/W references, and the
// altitude with its above/below sea level reference.
func (t tiffReader) gps(g map[uint16]field) *GPS {
	coord := func(refTag, valTag uint16, pos, neg string, limit float64) (float64, bool) {
		ref, ok := ascii(g[refTag])
		if !ok || (ref != pos && ref != neg) {
			return 0, false
		}
		v, ok := t.rationals(g[valTag])
		if !ok || len(v) != 3 {
			return 0, false
		}
		d := v[0] + v[1]/60 + v[2]/3600
		if d > limit {
			return 0, false
		}
		if ref == neg {
			d = -d
		}
		return d, true
	}
	lat, ok := coord(tagGPSLatitudeRef, tagGPSLatitude, "N", "S", 90)
	if !ok {
		return nil
	}
	lon, ok := coord(tagGPSLongitudeRef, tagGPSLongitude, "E", "W", 180)
	if !ok {
		return nil
	}
	p := &GPS{Latitude: lat, Longitude: lon}
	if v, ok := t.rationals(g[tagGPSAltitude]); ok && len(v) == 1 {
		p.Altitude, p.HasAltitude = v[0], true
		if f, ok := g[tagGPSAltitudeRef]; ok && f.typ == typeByte && f.value[0] == 1 {
			p.Altitude = -p.Altitude
		}
	}
	return p
}
