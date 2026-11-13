package mediameta

import (
	"io"
)

// EditFileXMP writes to dst the JPEG, HEIF, PNG or WebP file in src (size
// bytes) with edit applied to its XMP packet (see EditXMP), or with a new
// packet holding the edit when it has none. Everything else is as SetXMP
// writes it: the image data is copied byte for byte, and no old value
// stays in the file.
//
// On an error nothing useful has been written to dst: write to a
// temporary file and rename it over the original only on success.
func EditFileXMP(src io.ReaderAt, size int64, dst io.Writer, edit XMPEdit) error {
	p, err := PlanXMPEdit(src, size, edit)
	if err != nil {
		return err
	}
	_, err = p.WriteTo(dst)
	return err
}

// PlanXMPEdit is EditFileXMP's edit, computed but not written. The packet
// is read as ReadXMP reads it (a JPEG's extended XMP merged in), edited
// with EditXMP, which keeps the rest of the packet and its length where
// the change fits in its padding, and written back as PlanXMP writes it:
// in place where the new packet fits where the old one was (a HEIF item's
// data, a JPEG's XMP segment, a PNG or WebP chunk), else where SetXMP puts
// a longer one. A packet this package can't parse is refused (EditXMP's
// error) rather than replaced.
func PlanXMPEdit(src io.ReaderAt, size int64, edit XMPEdit) (*Plan, error) {
	apply := func(old []byte) ([]byte, error) { return EditXMP(old, edit) }
	if isJPEG(src, size) {
		old, err := readJPEGXMP(src, size)
		if err != nil {
			return nil, err
		}
		packet, err := apply(old)
		if err != nil {
			return nil, err
		}
		return planJPEGXMP(src, size, packet)
	}
	if p, ok, err := planChunked(src, size, kindXMP, apply); ok {
		return p, err
	}
	f, err := parseHEIF(src, size)
	if err != nil {
		return nil, err
	}
	id, found, err := f.metadataItem(kindXMP)
	if err != nil {
		return nil, err
	}
	var old []byte
	if found {
		if old, err = f.itemBytes(src, size, id); err != nil {
			return nil, err
		}
	}
	packet, err := apply(old)
	if err != nil {
		return nil, err
	}
	return planItem(src, size, kindXMP, packet)
}
