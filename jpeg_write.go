package mediameta

import (
	"encoding/binary"
	"fmt"
	"io"
)

// markerAPP0 is the JFIF (and JFXX) segment, which comes right after SOI.
const markerAPP0 = 0xE0

// jpegEdit is an orientation edit of a JPEG file: patches to the source's
// bytes, or a segment inserted at one offset. Either way every other byte,
// the image data among them, is copied as it is.
type jpegEdit struct {
	patches  []Patch
	insertAt int64
	insert   []byte
	// exif, when set, is the whole edit instead: the Exif segment
	// rewritten through EditExif (planJPEGExif), for Exif without the tag.
	exif *Plan
}

// delta is how much the edit grows the file.
func (e *jpegEdit) delta() int64 { return int64(len(e.insert)) }

// write streams the edited file to dst.
func (e *jpegEdit) write(src io.ReaderAt, size int64, dst io.Writer) (int64, error) {
	if e.exif != nil {
		return e.exif.WriteTo(dst)
	}
	if e.insert != nil {
		n, err := io.Copy(dst, io.NewSectionReader(src, 0, e.insertAt))
		if err != nil {
			return n, err
		}
		m, err := dst.Write(e.insert)
		n += int64(m)
		if err != nil {
			return n, err
		}
		k, err := io.Copy(dst, io.NewSectionReader(src, e.insertAt, size-e.insertAt))
		n += k
		if err == nil && n != size+e.delta() {
			err = fmt.Errorf("mediameta: wrote %d bytes, want %d", n, size+e.delta())
		}
		return n, err
	}
	ps := make([]patch, len(e.patches))
	for i, p := range e.patches {
		ps[i] = patch{at: p.Offset, data: p.Data}
	}
	w := &patchWriter{w: dst, patches: ps}
	n, err := io.Copy(w, io.NewSectionReader(src, 0, size))
	if err == nil && n != size {
		err = fmt.Errorf("mediameta: wrote %d bytes, want %d", n, size)
	}
	return n, err
}

// plan is the edit as a Plan: in place unless it inserts a segment.
func (e *jpegEdit) plan(src io.ReaderAt, size int64) *Plan {
	if e.exif != nil {
		return e.exif
	}
	p := &Plan{Size: size, NewSize: size + e.delta(), src: src, jpeg: e}
	if e.insert == nil {
		p.InPlace = true
		p.Patches = e.patches
	}
	return p
}

// planJPEGOrientation computes the edit that shows the JPEG file's image
// at the orientation target returns for the one it shows now (1 without
// a tag).
//
// An Orientation tag in the first Exif segment's IFD0 is changed in
// place, in the width its type gives it (BYTE, SHORT or LONG): the file
// keeps its size. A file without an Exif segment gets one holding only
// the tag, after SOI and any JFIF segments, unless the target is 1, which
// such a file shows already. An Exif segment without the tag (or with one
// whose value isn't in its entry) gets it through EditExif, which adds the
// entry to IFD0 and keeps every other byte's offset (see planJPEGExif):
// in place when the new TIFF data fits the segment, unless the target is 1,
// which such a file shows already.
func planJPEGOrientation(src io.ReaderAt, size int64, target func(Orientation) Orientation) (*jpegEdit, error) {
	f, err := parseJPEG(src, size)
	if err != nil {
		return nil, err
	}
	seg, payload, ok, err := f.exifSegment(src)
	if err != nil {
		return nil, err
	}
	if !ok {
		o := target(1)
		if o == 1 {
			return &jpegEdit{}, nil
		}
		return &jpegEdit{insertAt: f.afterJFIF(), insert: orientationSegment(o)}, nil
	}
	tiff := payload[len(exifHeader):]
	at, width, order, value, found := orientationEntry(tiff)
	cur := Orientation(1)
	if found && Orientation(value).Valid() {
		cur = Orientation(value)
	}
	o := target(cur)
	if !found {
		if o == 1 {
			return &jpegEdit{}, nil
		}
		p, err := planJPEGExif(src, size, func(old []byte) ([]byte, error) {
			return EditExif(old, ExifEdit{Orientation: o})
		})
		if err != nil {
			return nil, err
		}
		return &jpegEdit{exif: p}, nil
	}
	if value == uint32(o) {
		return &jpegEdit{}, nil
	}
	b := make([]byte, width)
	switch width {
	case 1:
		b[0] = byte(o)
	case 2:
		order.PutUint16(b, uint16(o))
	case 4:
		order.PutUint32(b, uint32(o))
	}
	off := seg.body + int64(len(exifHeader)) + int64(at)
	return &jpegEdit{patches: []Patch{{Offset: off, Data: b}}}, nil
}

// orientationEntry finds IFD0's Orientation tag in TIFF data: where its
// (first) value is, how many bytes wide, and the value. Only an integer
// type with the value inside the entry is found: one that points
// elsewhere isn't patched.
func orientationEntry(data []byte) (at, width int, order binary.ByteOrder, value uint32, ok bool) {
	t, first, ok := newTIFF(data)
	if !ok || first < 8 || uint64(first)+2 > uint64(len(data)) {
		return 0, 0, nil, 0, false
	}
	n := min(int(t.order.Uint16(data[first:])), maxIFDEntries, (len(data)-int(first)-2)/12)
	for i := range n {
		e := int(first) + 2 + 12*i
		if t.order.Uint16(data[e:]) != tagOrientation {
			continue
		}
		typ, count := t.order.Uint16(data[e+2:]), t.order.Uint32(data[e+4:])
		width = typeSize(typ)
		if count == 0 || uint64(width)*uint64(count) > 4 {
			return 0, 0, nil, 0, false
		}
		v, ok := t.uint(field{typ: typ, count: int(count), value: data[e+8 : e+12]})
		if !ok {
			return 0, 0, nil, 0, false
		}
		return e + 8, width, t.order, v, true
	}
	return 0, 0, nil, 0, false
}

// afterJFIF is where a new segment goes: after SOI and the APP0 (JFIF,
// JFXX) segments that follow it, which readers expect first.
func (f *jpegFile) afterJFIF() int64 {
	at := int64(2)
	for _, s := range f.segs {
		if s.marker != markerAPP0 {
			break
		}
		at = s.body + s.bodyLen
	}
	return at
}

// tagYCbCrPositioning is a TIFF tag Exif requires in a JPEG's IFD0.
const tagYCbCrPositioning = 0x0213

// orientationSegment is an Exif APP1 segment whose TIFF data has IFD0's
// Orientation tag, big-endian, and the YCbCrPositioning tag Exif requires
// of a JPEG (1, centred, as JFIF specifies), so that ExifTool's
// validation passes it.
func orientationSegment(o Orientation) []byte {
	be := binary.BigEndian
	tiff := []byte("MM\x00*")
	tiff = be.AppendUint32(tiff, 8)
	tiff = be.AppendUint16(tiff, 2) // two entries, by tag
	for _, e := range [][2]uint16{{tagOrientation, uint16(o)}, {tagYCbCrPositioning, 1}} {
		tiff = be.AppendUint16(tiff, e[0])
		tiff = be.AppendUint16(tiff, typeShort)
		tiff = be.AppendUint32(tiff, 1)
		tiff = be.AppendUint16(tiff, e[1])
		tiff = be.AppendUint16(tiff, 0)
	}
	tiff = be.AppendUint32(tiff, 0) // no next IFD
	body := append(append([]byte(nil), exifHeader...), tiff...)
	seg := []byte{0xFF, markerAPP1}
	seg = be.AppendUint16(seg, uint16(len(body)+2))
	return append(seg, body...)
}

// jpegRotation is SetRotation's edit of a JPEG file: the orientation it
// shows, mirrored or not as it is, rotated degrees clockwise. ok is false
// for a file that isn't a JPEG; the HEIF path's error stands then. It is
// tried only once the HEIF parse has failed, so HEIF costs no more.
func jpegRotation(src io.ReaderAt, size int64, degrees int) (*jpegEdit, bool, error) {
	if degrees%90 != 0 || !isJPEG(src, size) {
		return nil, false, nil
	}
	e, err := planJPEGOrientation(src, size, func(cur Orientation) Orientation {
		return orientationFor(cur.Mirrored(), degrees)
	})
	return e, true, err
}

// jpegOrientation is SetOrientation's edit of a JPEG file.
func jpegOrientation(src io.ReaderAt, size int64, o Orientation) (*jpegEdit, bool, error) {
	if !o.Valid() || !isJPEG(src, size) {
		return nil, false, nil
	}
	e, err := planJPEGOrientation(src, size, func(Orientation) Orientation { return o })
	return e, true, err
}
