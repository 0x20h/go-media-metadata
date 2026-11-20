package mediameta

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"strings"
	"time"
)

// FLV (Flash Video). Read only. The file is a header and a sequence of
// tags; the script tag "onMetaData", near the start, holds an AMF0 array
// of named values as the writer set them: duration (seconds), width,
// height, and sometimes title, creationdate (text) and metadatadate (an
// AMF date, when the metadata was written). ffmpeg writes its recording
// time as creation_time (ISO 8601).

// errFLVReadOnly is the error of a write to an FLV file.
var errFLVReadOnly = fmt.Errorf("%w: an FLV file is read only", ErrUnsupported)

// maxFLVScript caps the onMetaData tag read whole, and maxFLVScan how far
// into the file it's looked for.
const (
	maxFLVScript = 1 << 20
	maxFLVScan   = 64
)

func isFLV(r io.ReaderAt, size int64) bool {
	var h [9]byte
	if size < 13 {
		return false
	}
	if _, err := r.ReadAt(h[:], 0); err != nil {
		return false
	}
	return string(h[:3]) == "FLV" && h[3] == 1 && binary.BigEndian.Uint32(h[5:]) >= 9
}

// readFLVVideo reads an FLV file's onMetaData: the duration, size, title
// and date.
func readFLVVideo(r io.ReaderAt, size int64) (Video, error) {
	v := Video{Orientation: 1}
	if !isFLV(r, size) {
		return v, formatError("FLV")
	}
	var h [9]byte
	if _, err := r.ReadAt(h[:], 0); err != nil {
		return v, fmt.Errorf("%w: reading the FLV header: %v", ErrMalformed, err)
	}
	// Tags follow the header and a 4-byte previous tag size: type (1),
	// data size (3), timestamp (3 + 1), stream id (3), then the data.
	at := int64(binary.BigEndian.Uint32(h[5:])) + 4
	for range maxFLVScan {
		var th [11]byte
		if at+11 > size {
			break
		}
		if _, err := r.ReadAt(th[:], at); err != nil {
			return v, fmt.Errorf("%w: reading an FLV tag: %v", ErrMalformed, err)
		}
		n := int64(th[1])<<16 | int64(th[2])<<8 | int64(th[3])
		if th[0]&0x1F == 18 { // script data
			if n > maxFLVScript || at+11+n > size {
				break
			}
			b := make([]byte, n)
			if _, err := r.ReadAt(b, at+11); err != nil {
				return v, fmt.Errorf("%w: reading an FLV script tag: %v", ErrMalformed, err)
			}
			if readFLVMetaData(b, &v) {
				break
			}
		}
		at += 11 + n + 4
	}
	return v, nil
}

// readFLVMetaData reads an onMetaData script tag into v; false for
// another script tag.
func readFLVMetaData(b []byte, v *Video) bool {
	a := amf0{b: b}
	if name, ok := a.value(0).(string); !ok || name != "onMetaData" {
		return false
	}
	vals, _ := a.value(0).(map[string]any)
	num := func(k string) (float64, bool) {
		f, ok := vals[k].(float64)
		return f, ok && !math.IsNaN(f) && !math.IsInf(f, 0)
	}
	if d, ok := num("duration"); ok && d > 0 && d < 1e9 {
		v.Duration = time.Duration(d * float64(time.Second))
	}
	if w, ok := num("width"); ok {
		if h, ok := num("height"); ok && w >= 1 && h >= 1 && w <= 1<<16 && h <= 1<<16 {
			v.Width, v.Height = int(w), int(h)
		}
	}
	if s, ok := vals["title"].(string); ok {
		v.Title = strings.TrimSpace(s)
	}
	// The date: creationdate, as the writer gives it (ExifTool's
	// CreateDate), else ffmpeg's creation_time, else metadatadate.
	for _, k := range []string{"creationdate", "creation_time"} {
		if s, ok := vals[k].(string); ok && v.Created.IsZero() {
			v.Created, v.OffsetKnown = parseFLVDate(s)
		}
	}
	if t, ok := vals["metadatadate"].(time.Time); ok && v.Created.IsZero() {
		v.Created, v.OffsetKnown = t, true
	}
	return true
}

// parseFLVDate reads a text date: ISO 8601 or the Exif layout, as XMP
// dates are read (to the second, at least), else asctime's layout
// ("Mon Jan 02 15:04:05 2006"), as Flash tools wrote it, without an
// offset. Zero for anything else.
func parseFLVDate(s string) (time.Time, bool) {
	if d, ok := parseXMPDate(s); ok && d.Precision >= PrecisionDay {
		return d.Time, d.OffsetKnown
	}
	if t, ok := parseAVIDate(s); ok {
		return t, false
	}
	return time.Time{}, false
}

// amf0 reads AMF0 values (Action Message Format 0) from b: numbers,
// booleans, strings, objects and ECMA arrays (as maps), dates (as UTC
// times) and the rest skipped as nil.
type amf0 struct {
	b   []byte
	at  int
	bad bool
}

// maxAMFDepth caps the nesting of objects and arrays read.
const maxAMFDepth = 8

func (a *amf0) need(n int) bool {
	if a.bad || n < 0 || len(a.b)-a.at < n {
		a.bad = true
		return false
	}
	return true
}

func (a *amf0) u16() int {
	if !a.need(2) {
		return 0
	}
	v := int(binary.BigEndian.Uint16(a.b[a.at:]))
	a.at += 2
	return v
}

func (a *amf0) str(n int) string {
	if !a.need(n) {
		return ""
	}
	s := string(a.b[a.at : a.at+n])
	a.at += n
	return s
}

// value reads the next value; nil for one that's skipped or doesn't read.
func (a *amf0) value(depth int) any {
	if !a.need(1) {
		return nil
	}
	typ := a.b[a.at]
	a.at++
	switch typ {
	case 0: // number
		if !a.need(8) {
			return nil
		}
		f := math.Float64frombits(binary.BigEndian.Uint64(a.b[a.at:]))
		a.at += 8
		return f
	case 1: // boolean
		if !a.need(1) {
			return nil
		}
		a.at++
		return a.b[a.at-1] != 0
	case 2: // string
		return a.str(a.u16())
	case 12: // long string
		if !a.need(4) {
			return nil
		}
		n := binary.BigEndian.Uint32(a.b[a.at:])
		a.at += 4
		if n > uint32(len(a.b)) {
			a.bad = true
			return nil
		}
		return a.str(int(n))
	case 3, 8: // object; ECMA array (a count first, then as an object)
		if typ == 8 {
			if !a.need(4) {
				return nil
			}
			a.at += 4
		}
		if depth >= maxAMFDepth {
			a.bad = true
			return nil
		}
		m := map[string]any{}
		for !a.bad {
			k := a.str(a.u16())
			if k == "" && a.need(1) && a.b[a.at] == 9 { // object end
				a.at++
				break
			}
			m[k] = a.value(depth + 1)
		}
		return m
	case 10: // strict array
		if !a.need(4) {
			return nil
		}
		n := binary.BigEndian.Uint32(a.b[a.at:])
		a.at += 4
		if depth >= maxAMFDepth || n > uint32(len(a.b)) {
			a.bad = true
			return nil
		}
		for i := uint32(0); i < n && !a.bad; i++ {
			a.value(depth + 1)
		}
		return nil
	case 11: // date: milliseconds since 1970 UTC, then a time zone (unused)
		if !a.need(10) {
			return nil
		}
		ms := math.Float64frombits(binary.BigEndian.Uint64(a.b[a.at:]))
		a.at += 10
		if math.IsNaN(ms) || math.Abs(ms) > 1e15 {
			return nil
		}
		return time.UnixMilli(int64(ms)).UTC()
	case 5, 6: // null, undefined
		return nil
	}
	a.bad = true // a type that can't be skipped
	return nil
}
