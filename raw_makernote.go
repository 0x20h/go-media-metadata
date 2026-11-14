package mediameta

import (
	"bytes"
	"encoding/binary"
	"io"
)

// Maker-note tags that locate previews.
const (
	tagNikonPreviewIFD     = 0x0011 // Nikon: the PreviewIFD
	tagOlympusCamera       = 0x2020 // Olympus: the CameraSettings IFD
	tagOlympusPreviewStart = 0x0101 // in CameraSettings
	tagOlympusPreviewLen   = 0x0102
	tagOlympusThumbnail    = 0x0100 // Olympus: a JPEG thumbnail as the value
	tagOlympusOldStart     = 0x0088 // old Olympus maker notes, in their IFD
	tagOlympusOldLen       = 0x0089
)

// entry reads one entry of the IFD at off without reading its value: its
// type, count and 4-byte value field (the value itself, or where it is).
// ok is false for an IFD without the tag.
func (t *rawTIFF) entry(off uint32, tag uint16) (typ uint16, count, field uint32, ok bool) {
	var nb [2]byte
	if off < 8 || int64(off)+2 > t.size {
		return 0, 0, 0, false
	}
	if _, err := t.r.ReadAt(nb[:], int64(off)); err != nil {
		return 0, 0, 0, false
	}
	n := min(int(t.order.Uint16(nb[:])), maxIFDEntries, int((t.size-int64(off)-2)/12))
	entries := make([]byte, 12*n)
	if _, err := t.r.ReadAt(entries, int64(off)+2); err != nil {
		return 0, 0, 0, false
	}
	for i := range n {
		e := entries[12*i:]
		if t.order.Uint16(e) == tag {
			return t.order.Uint16(e[2:]), t.order.Uint32(e[4:]), t.order.Uint32(e[8:]), true
		}
	}
	return 0, 0, 0, false
}

// at is a rawTIFF reading the same file from base on: offsets in a maker
// note count from its own start or TIFF header, not the file's.
func (t *rawTIFF) at(base int64, order binary.ByteOrder, first uint32) *rawTIFF {
	return &rawTIFF{
		tiffReader: tiffReader{order: order},
		r:          io.NewSectionReader(t.r, base, t.size-base),
		size:       t.size - base,
		first:      first,
	}
}

// makerNotePreviews adds the previews only a maker note locates: Nikon's
// PreviewIFD; Olympus's thumbnail (tag 0x0100, the JPEG as its value) and
// preview (in its CameraSettings IFD, or in an old-style maker note its
// own IFD). Offsets are the file's.
func (t *rawTIFF) makerNotePreviews(add func(off, n int64)) {
	ifd0 := t.ifd(t.first)
	exif, ok := t.uint(ifd0[tagExifIFD])
	if !ok {
		return
	}
	_, count, field, ok := t.entry(exif, tagMakerNote)
	if !ok || count < 16 {
		return
	}
	mn := int64(field)
	// Nikon's is the longest header: 10 bytes, then a TIFF header of 8.
	var h [18]byte
	if mn+int64(len(h)) > t.size {
		return
	}
	if _, err := t.r.ReadAt(h[:], mn); err != nil {
		return
	}
	switch {
	case bytes.HasPrefix(h[:], []byte("Nikon\x00\x02")):
		// A TIFF header 10 bytes in; offsets count from it.
		base := mn + 10
		var order binary.ByteOrder
		switch string(h[10:12]) {
		case "II":
			order = binary.LittleEndian
		case "MM":
			order = binary.BigEndian
		default:
			return
		}
		sub := t.at(base, order, order.Uint32(h[14:]))
		v, ok := sub.uint(sub.ifd(sub.first)[tagNikonPreviewIFD])
		if !ok {
			return
		}
		p := sub.ifd(v)
		start, ok1 := sub.uint(p[tagJPEGOffset])
		n, ok2 := sub.uint(p[tagJPEGLength])
		if ok1 && ok2 {
			add(base+int64(start), int64(n))
		}
	case bytes.HasPrefix(h[:], []byte("OLYMPUS\x00")):
		// Byte order at 8, the IFD at 12; offsets count from the maker
		// note's start.
		var order binary.ByteOrder
		switch string(h[8:10]) {
		case "II":
			order = binary.LittleEndian
		case "MM":
			order = binary.BigEndian
		default:
			return
		}
		sub := t.at(mn, order, 12)
		if typ, n, at, ok := sub.entry(sub.first, tagOlympusThumbnail); ok && typ == typeUndef && n > 4 {
			add(mn+int64(at), int64(n))
		}
		// CameraSettings is an IFD pointer, or an UNDEFINED value whose
		// data is the IFD: either way the value field says where it is.
		_, _, cs, ok := sub.entry(sub.first, tagOlympusCamera)
		if !ok {
			return
		}
		p := sub.ifd(cs)
		start, ok1 := sub.uint(p[tagOlympusPreviewStart])
		n, ok2 := sub.uint(p[tagOlympusPreviewLen])
		if ok1 && ok2 {
			add(mn+int64(start), int64(n))
		}
	case bytes.HasPrefix(h[:], []byte("OLYMP\x00")):
		// The old style: the IFD at 8, in the file's byte order, offsets
		// counting from the file's TIFF header.
		if typ, n, at, ok := t.entry(uint32(mn+8), tagOlympusThumbnail); ok && typ == typeUndef && n > 4 {
			add(int64(at), int64(n))
		}
		ifd, _ := t.ifdAt(uint32(mn + 8))
		start, ok1 := t.uint(ifd[tagOlympusOldStart])
		n, ok2 := t.uint(ifd[tagOlympusOldLen])
		if ok1 && ok2 {
			add(int64(start), int64(n))
		}
	}
}
