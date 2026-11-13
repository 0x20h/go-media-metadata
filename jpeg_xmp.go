package mediameta

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
)

// The APP1 segments of XMP in a JPEG (XMP Specification Part 3, 1.1.3):
// the standard packet after its namespace, and the extended part in
// chunks, each after its own namespace, the GUID, the full length and the
// chunk's offset.
var (
	xmpHeader    = []byte("http://ns.adobe.com/xap/1.0/\x00")
	xmpExtHeader = []byte("http://ns.adobe.com/xmp/extension/\x00")
)

// xmpExtHead is the size of an extended chunk's header: namespace, GUID,
// full length, offset.
var xmpExtHead = len(xmpExtHeader) + 32 + 4 + 4

// xmpChunk is the most extended XMP one segment holds. Writers use 65400
// or so, below the segment's limit; this is the same.
const xmpChunk = 65400

// jpegXMP are a JPEG's XMP segments: the first standard one (ok false
// without one) and every extended one.
type jpegXMP struct {
	seg      jpegSegment
	packet   []byte
	ok       bool
	extended []jpegSegment
}

func (f *jpegFile) xmpSegments(r io.ReaderAt) (jpegXMP, error) {
	var x jpegXMP
	head := make([]byte, len(xmpExtHeader))
	for _, s := range f.segs {
		if s.marker != markerAPP1 || s.bodyLen < int64(len(xmpHeader)) {
			continue
		}
		n := min(int64(len(head)), s.bodyLen)
		if _, err := r.ReadAt(head[:n], s.body); err != nil {
			return x, fmt.Errorf("%w: reading APP1: %v", ErrMalformed, err)
		}
		switch {
		case !x.ok && bytes.HasPrefix(head[:n], xmpHeader):
			body := make([]byte, s.bodyLen)
			if _, err := r.ReadAt(body, s.body); err != nil {
				return x, fmt.Errorf("%w: reading APP1: %v", ErrMalformed, err)
			}
			x.seg, x.ok = s, true
			// Writers pad the segment after the packet with NULs or
			// spaces; NULs aren't XML.
			x.packet = bytes.TrimRight(body[len(xmpHeader):], "\x00")
		case n == int64(len(xmpExtHeader)) && bytes.Equal(head, xmpExtHeader):
			x.extended = append(x.extended, s)
		}
	}
	return x, nil
}

// extendedPart assembles the extended XMP named guid from its chunks; ok
// is false when its chunks don't make the whole of it (it's ignored
// then, as ExifTool does).
func (x jpegXMP) extendedPart(r io.ReaderAt, guid string) ([]byte, bool, error) {
	var full []byte
	var covered []bool
	left := -1
	for _, s := range x.extended {
		if s.bodyLen < int64(xmpExtHead) {
			continue
		}
		body := make([]byte, s.bodyLen)
		if _, err := r.ReadAt(body, s.body); err != nil {
			return nil, false, fmt.Errorf("%w: reading APP1: %v", ErrMalformed, err)
		}
		h := len(xmpExtHeader)
		if string(body[h:h+32]) != guid {
			continue
		}
		total := int64(binary.BigEndian.Uint32(body[h+32:]))
		off := int64(binary.BigEndian.Uint32(body[h+36:]))
		chunk := body[xmpExtHead:]
		if full == nil {
			if total == 0 || total > maxXMP {
				return nil, false, nil
			}
			full, covered, left = make([]byte, total), make([]bool, total), int(total)
		}
		if total != int64(len(full)) || off+int64(len(chunk)) > total {
			return nil, false, nil
		}
		copy(full[off:], chunk)
		for i := range chunk {
			if !covered[off+int64(i)] {
				covered[off+int64(i)] = true
				left--
			}
		}
	}
	if full == nil || left != 0 {
		return nil, false, nil
	}
	return full, true, nil
}

// readJPEGXMP is a JPEG's XMP as one packet: the standard packet, with
// the extended part it names merged in where the file has all of it.
func readJPEGXMP(r io.ReaderAt, size int64) ([]byte, error) {
	f, err := parseJPEG(r, size)
	if err != nil {
		return nil, err
	}
	x, err := f.xmpSegments(r)
	if err != nil || !x.ok {
		return nil, err
	}
	d, err := parseXMPDoc(x.packet)
	if err != nil {
		// Not XMP this package reads: the packet as it is.
		return x.packet, nil
	}
	guid := d.extendedGUID()
	if guid == "" {
		return x.packet, nil
	}
	ext, ok, err := x.extendedPart(r, guid)
	if err != nil || !ok {
		return x.packet, err
	}
	merged, err := mergeXMP(x.packet, ext)
	if err != nil {
		return x.packet, nil
	}
	return merged, nil
}

// planJPEGXMP sets a JPEG's XMP to packet. A packet that fits in one
// segment goes there; a bigger one is split into a standard and an
// extended part (splitXMP), the extended part in chunks right after the
// standard segment. Any old extended segments are removed.
//
// A standard packet no longer than the old segment's fills it, padded
// with whitespace in its padding: in place when no extended segments
// change. A longer one replaces the segment. A JPEG without XMP gets it
// after its Exif segment, else after its JFIF header.
func planJPEGXMP(src io.ReaderAt, size int64, packet []byte) (*Plan, error) {
	f, err := parseJPEG(src, size)
	if err != nil {
		return nil, err
	}
	x, err := f.xmpSegments(src)
	if err != nil {
		return nil, err
	}
	standard, extended, err := splitXMP(packet, maxAPP1-len(xmpHeader))
	if err != nil {
		return nil, err
	}
	e := &spliceEdit{}
	var after int64 // where new extended segments go
	switch {
	case x.ok && len(xmpHeader)+len(standard) <= int(x.seg.bodyLen):
		body := append(append([]byte(nil), xmpHeader...), padXMP(standard, int(x.seg.bodyLen)-len(xmpHeader))...)
		e.add(splice{at: x.seg.body, del: x.seg.bodyLen, data: body})
		after = x.seg.body + x.seg.bodyLen
	case x.ok:
		e.add(splice{at: x.seg.offset, del: x.seg.body + x.seg.bodyLen - x.seg.offset, data: app1(append(append([]byte(nil), xmpHeader...), standard...))})
		after = x.seg.body + x.seg.bodyLen
	default:
		after = f.afterJFIF()
		if seg, _, found, err := f.exifSegment(src); err != nil {
			return nil, err
		} else if found {
			after = seg.body + seg.bodyLen
		}
		e.add(splice{at: after, data: app1(append(append([]byte(nil), xmpHeader...), standard...))})
	}
	// The new extended segments first: an old one may start where they
	// go, and an insert sorts before a removal at the same offset.
	if extended != nil {
		e.add(splice{at: after, data: extendedSegments(standard, extended)})
	}
	for _, s := range x.extended {
		e.add(splice{at: s.offset, del: s.body + s.bodyLen - s.offset})
	}
	if err := e.sorted(); err != nil {
		return nil, err
	}
	return e.plan(src, size), nil
}

// extendedSegments are the APP1 segments of an extended part, named by the
// GUID in standard.
func extendedSegments(standard, extended []byte) []byte {
	d, _ := parseXMPDoc(standard)
	guid := d.extendedGUID()
	var out []byte
	for off := 0; off < len(extended); off += xmpChunk {
		chunk := extended[off:min(off+xmpChunk, len(extended))]
		body := append([]byte(nil), xmpExtHeader...)
		body = append(body, guid...)
		body = binary.BigEndian.AppendUint32(body, uint32(len(extended)))
		body = binary.BigEndian.AppendUint32(body, uint32(off))
		out = append(out, app1(append(body, chunk...))...)
	}
	return out
}
