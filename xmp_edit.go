package mediameta

import (
	"io"
)

// planXMPEdit is EditFileXMP's edit, computed but not written. The packet
// is read as ReadXMP reads it (a JPEG's extended XMP merged in), edited
// with EditXMP, which keeps the rest of the packet and its length where
// the change fits in its padding, and written back as PlanXMP writes it:
// in place where the new packet fits where the old one was (a HEIF item's
// data, a JPEG's XMP segment, a PNG or WebP chunk), else where SetXMP puts
// a longer one. A packet this package can't parse is refused (EditXMP's
// error) rather than replaced.
func planXMPEdit(src io.ReaderAt, size int64, edit XMPEdit) (*Plan, error) {
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
