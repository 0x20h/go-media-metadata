package mediameta

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"

	"github.com/0x20h/go-media-metadata/internal/bmff"
)

// maxExifHead is how much of an Exif item is read to find IFD0: the
// TIFF header and the first IFD sit at its start.
const maxExifHead = 64 << 10

// tagOrientation is the Exif/TIFF Orientation tag.
const tagOrientation = 0x0112

// exifItem is an Exif item's data as the file places it: its first bytes,
// and where each of them is in the file.
type exifItem struct {
	head []byte
	// pos maps an offset in head to its file offset.
	pos func(i int) (int64, bool)
	// inIdat is true for data in the meta box's idat.
	inIdat bool
	// idatOff is head[i]'s offset in idat's payload, for inIdat.
	idatOff func(i int) int64
}

// idatBody is where the idat box's payload starts in a meta box held in
// meta (at file offset base), or -1 without one.
func idatBody(meta []byte) int64 {
	top, err := bmff.ReadBoxes(bytes.NewReader(meta), 0, int64(len(meta)))
	if err != nil || len(top) != 1 || top[0].BodyLen() < 4 {
		return -1
	}
	kids, err := bmff.ReadBoxes(bytes.NewReader(meta), top[0].Body()+4, top[0].End())
	if err != nil {
		return -1
	}
	if b, ok := bmff.Find(kids, bmff.T("idat")); ok {
		return b.Body()
	}
	return -1
}

// readExifItem reads the start of item id's data (construction method 0
// or 1); ok is false for an item it can't place.
func (f *heifFile) readExifItem(src io.ReaderAt, size int64, id uint32, oldMeta []byte) (*exifItem, bool, error) {
	loc, ok := f.m.Loc.Item(id)
	if !ok || loc.DataRefIndex != 0 || len(loc.Extents) == 0 {
		return nil, false, nil
	}
	var base int64
	switch loc.ConstructionMethod {
	case 0:
	case 1:
		idat := idatBody(oldMeta)
		if idat < 0 {
			return nil, false, fmt.Errorf("%w: item %d in an idat that isn't there", ErrMalformed, id)
		}
		base = f.meta.Offset + idat
	default:
		return nil, false, nil
	}
	type span struct{ file, off, n int64 }
	var spans []span
	var head []byte
	for _, e := range loc.Extents {
		if len(head) >= maxExifHead {
			break
		}
		start := int64(loc.BaseOffset + e.Offset)
		if start < 0 || uint64(start) < loc.BaseOffset {
			return nil, false, fmt.Errorf("%w: item %d offset", ErrMalformed, id)
		}
		file := base + start
		n := int64(e.Length)
		if n == 0 || n > size-file {
			n = size - file
		}
		if file < 0 || file > size || n < 0 {
			return nil, false, fmt.Errorf("%w: item %d outside the file", ErrMalformed, id)
		}
		n = min(n, int64(maxExifHead-len(head)))
		buf := make([]byte, n)
		if _, err := src.ReadAt(buf, file); err != nil && err != io.EOF {
			return nil, false, fmt.Errorf("%w: reading item %d: %v", ErrMalformed, id, err)
		}
		spans = append(spans, span{file: file, off: int64(len(head)), n: n})
		head = append(head, buf...)
	}
	it := &exifItem{head: head, inIdat: loc.ConstructionMethod == 1}
	it.pos = func(i int) (int64, bool) {
		for _, s := range spans {
			if int64(i) >= s.off && int64(i) < s.off+s.n {
				return s.file + int64(i) - s.off, true
			}
		}
		return 0, false
	}
	it.idatOff = func(i int) int64 {
		p, _ := it.pos(i)
		return p - base
	}
	return it, true, nil
}

// orientationTag finds IFD0's Orientation value in an Exif item's data:
// a 4-byte offset to the TIFF header, then the TIFF data (ISO/IEC 23008-12
// A.2.1). It returns the value's offset in data, its byte order and value.
func orientationTag(data []byte) (at int, order binary.ByteOrder, value uint16, ok bool) {
	if len(data) < 4 {
		return 0, nil, 0, false
	}
	tiff := 4 + int(binary.BigEndian.Uint32(data))
	if tiff < 4 || tiff+8 > len(data) {
		return 0, nil, 0, false
	}
	switch string(data[tiff : tiff+4]) {
	case "II*\x00":
		order = binary.LittleEndian
	case "MM\x00*":
		order = binary.BigEndian
	default:
		return 0, nil, 0, false
	}
	ifd := tiff + int(order.Uint32(data[tiff+4:]))
	if ifd < tiff || ifd+2 > len(data) {
		return 0, nil, 0, false
	}
	n := int(order.Uint16(data[ifd:]))
	for i := range n {
		e := ifd + 2 + 12*i
		if e+12 > len(data) {
			return 0, nil, 0, false
		}
		if order.Uint16(data[e:]) != tagOrientation {
			continue
		}
		// SHORT, count 1: the value sits in the entry.
		if order.Uint16(data[e+2:]) != 3 || order.Uint32(data[e+4:]) != 1 {
			return 0, nil, 0, false
		}
		return e + 8, order, order.Uint16(data[e+8:]), true
	}
	return 0, nil, 0, false
}

// exifOrientationPatches are the output bytes that set each Exif item's
// Orientation tag to shown, where the tag exists and differs.
func (f *heifFile) exifOrientationPatches(src io.ReaderAt, size int64, newMeta []byte, delta int64, shown Orientation) ([]patch, error) {
	var oldMeta []byte
	var out []patch
	for id, t := range f.m.ItemTypes {
		if t != bmff.T("Exif") {
			continue
		}
		if oldMeta == nil {
			oldMeta = make([]byte, f.meta.Size)
			if _, err := src.ReadAt(oldMeta, f.meta.Offset); err != nil {
				return nil, fmt.Errorf("%w: reading the meta box: %v", ErrMalformed, err)
			}
		}
		it, ok, err := f.readExifItem(src, size, id, oldMeta)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		at, order, value, found := orientationTag(it.head)
		if !found || value == uint16(shown) {
			continue
		}
		b := make([]byte, 2)
		order.PutUint16(b, uint16(shown))
		for i := range 2 {
			var outAt int64
			if it.inIdat {
				idat := idatBody(newMeta)
				if idat < 0 {
					return nil, fmt.Errorf("%w: idat lost", ErrMalformed)
				}
				outAt = f.meta.Offset + idat + it.idatOff(at+i)
			} else {
				p, ok := it.pos(at + i)
				if !ok {
					return nil, fmt.Errorf("%w: Exif item %d", ErrMalformed, id)
				}
				switch {
				case p >= f.meta.End():
					outAt = p + delta
				case p >= f.meta.Offset:
					return nil, fmt.Errorf("%w: Exif item %d inside the meta box", ErrUnsupported, id)
				default:
					outAt = p
				}
			}
			out = append(out, patch{at: outAt, data: b[i : i+1]})
		}
	}
	return out, nil
}

// exifOrientation reads the Orientation tag of the file's Exif item; ok
// is false without one.
func exifOrientation(r io.ReaderAt, size int64) (Orientation, bool, error) {
	f, err := parseHEIF(r, size)
	if err != nil {
		return 0, false, err
	}
	oldMeta := make([]byte, f.meta.Size)
	if _, err := r.ReadAt(oldMeta, f.meta.Offset); err != nil {
		return 0, false, err
	}
	for id, t := range f.m.ItemTypes {
		if t != bmff.T("Exif") {
			continue
		}
		it, ok, err := f.readExifItem(r, size, id, oldMeta)
		if err != nil {
			return 0, false, err
		}
		if !ok {
			continue
		}
		if _, _, v, found := orientationTag(it.head); found {
			return Orientation(v), true, nil
		}
	}
	return 0, false, nil
}
