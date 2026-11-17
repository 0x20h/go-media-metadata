package mediameta

import (
	"fmt"
	"io"
)

// TIFF and DNG files are their own Exif: IFD0 and its ExifIFD and GPS IFD
// are the file's, and every offset in them is from the start of the file.
// So an Exif edit is EditExif's edit of the whole file, and an XMP edit
// one of IFD0's XMLPacket (tag 700). The TIFF writer changes values where
// they are when they fit, appends what doesn't (a longer value, an IFD
// with more entries) at the end of the file, and zeroes what it frees; it
// moves nothing else, so strips, tiles, SubIFDs and a DNG's raw data keep
// their offsets.

// maxTIFFEdit caps the TIFF and DNG files edited: the edit holds the file
// in memory.
const maxTIFFEdit = 1 << 30

// tiffFileEditable reports whether src is a TIFF or DNG file whose
// metadata this package writes: a standard TIFF header ("II*\0",
// "MM\0*"), and either a DNG (IFD0 has DNGVersion) or a file without an
// Exif maker note. A camera's own RAW (CR2, NEF, ARW, …) has one, holding
// offsets of its own: it's refused (ErrUnsupported), as with other RAWs.
// ok is false for a file that isn't TIFF-based at all.
func tiffFileEditable(src io.ReaderAt, size int64) (ok bool, err error) {
	t, isTIFF := openRAWTIFF(src, size)
	if !isTIFF {
		return false, nil
	}
	if t.magic != [4]byte{'I', 'I', '*', 0} && t.magic != [4]byte{'M', 'M', 0, '*'} {
		return true, fmt.Errorf("%w: metadata isn't written to a %q RAW file", ErrUnsupported, t.magic[:])
	}
	ifd0, _ := t.ifdAt(t.first)
	if ifd0 == nil {
		return true, fmt.Errorf("%w: no IFD0", ErrMalformed)
	}
	if _, dng := ifd0[tagDNGVersion]; dng {
		return true, nil
	}
	if f, ok := ifd0[tagExifIFD]; ok {
		if off, ok := t.uint(f); ok {
			if exif, _ := t.ifdAt(off); exif != nil {
				if _, mn := exif[tagMakerNote]; mn {
					return true, fmt.Errorf("%w: metadata isn't written to a camera RAW file (it has a maker note)", ErrUnsupported)
				}
			}
		}
	}
	if size > maxTIFFEdit {
		return true, fmt.Errorf("%w: a TIFF of %d bytes", ErrUnsupported, size)
	}
	return true, nil
}

// planTIFFFileEdit plans edit applied to the whole file in src: the bytes
// it changes as patches, and what it appends at the end. In place when it
// appends nothing.
func planTIFFFileEdit(src io.ReaderAt, size int64, edit func(file []byte) ([]byte, error)) (*Plan, error) {
	file := make([]byte, size)
	if _, err := src.ReadAt(file, 0); err != nil && err != io.EOF {
		return nil, fmt.Errorf("%w: reading the file: %v", ErrMalformed, err)
	}
	out, err := edit(file)
	if err != nil {
		return nil, err
	}
	if int64(len(out)) < size {
		return nil, fmt.Errorf("mediameta: a TIFF edit that shrank the file")
	}
	e := &spliceEdit{}
	diffSplices(e, 0, file, out[:size])
	if int64(len(out)) > size {
		e.add(splice{at: size, data: out[size:]})
	}
	// The image data, as HashImageData takes it, must stay as it is. In a
	// file whose strips or tiles claim bytes the metadata uses (or run
	// past the end, over what's appended), the edit is refused.
	var ranges [][2]int64
	h := &rangeWriter{r: src, size: size, w: io.Discard, record: &ranges}
	if err := h.raw(); err == nil {
		for _, s := range e.splices {
			end := s.at + max(s.del, 1) // an insert at the end touches the byte after
			for _, rg := range ranges {
				if s.at < rg[1] && rg[0] < end {
					return nil, fmt.Errorf("%w: a TIFF whose image data overlaps its metadata (at %d)", ErrUnsupported, s.at)
				}
			}
		}
	}
	if err := e.sorted(); err != nil {
		return nil, err
	}
	return e.plan(src, size), nil
}

// editTIFFXMP applies edit to the XMP packet in a TIFF's IFD0 (tag 700,
// XMLPacket; a new one where there's none).
func editTIFFXMP(file []byte, edit XMPEdit) ([]byte, error) {
	w, err := newTIFFWriter(file)
	if err != nil {
		return nil, err
	}
	es, err := w.entries(w.first)
	if err != nil {
		return nil, err
	}
	var old []byte
	for _, e := range es {
		if e.tag != tagXMP {
			continue
		}
		if e.typ != typeByte && e.typ != typeUndef {
			return nil, fmt.Errorf("%w: an XMLPacket of type %d", ErrUnsupported, e.typ)
		}
		if start, end, ok := w.valueSpan(e); ok {
			if end > uint64(len(w.data)) {
				return nil, fmt.Errorf("%w: an XMLPacket past the end of the file", ErrMalformed)
			}
			old = w.data[start:end]
		} else {
			old = e.raw[8 : 8+e.size()]
		}
		break
	}
	packet, err := EditXMP(old, edit)
	if err != nil {
		return nil, err
	}
	if string(packet) == string(old) {
		return file, nil
	}
	at, err := w.apply(w.first, true, []tiffChange{{tag: tagXMP, typ: typeByte, count: uint32(len(packet)), value: packet}})
	if err != nil {
		return nil, err
	}
	w.order.PutUint32(w.data[4:], at)
	return w.data, nil
}
