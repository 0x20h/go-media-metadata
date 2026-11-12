package mediameta

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// ErrOrientationInExif is returned by PlanExifEdit for an edit that sets
// the Orientation tag: a HEIF image is shown by its irot and imir
// properties, so the tag alone would only disagree with them. Use
// SetOrientation, which sets both.
var ErrOrientationInExif = errors.New("mediameta: a HEIF image's orientation is set with SetOrientation, not in its Exif")

// EditHEIFExif writes to dst the HEIF file in src (size bytes) with edit
// applied to its Exif item (see EditExif), or with a new Exif item holding
// the edit when it has none. Everything else is as SetExif writes it.
func EditHEIFExif(src io.ReaderAt, size int64, dst io.Writer, edit ExifEdit) error {
	p, err := PlanExifEdit(src, size, edit)
	if err != nil {
		return err
	}
	_, err = p.WriteTo(dst)
	return err
}

// PlanExifEdit is EditHEIFExif's edit, computed but not written. The Exif
// item keeps what comes before its TIFF data (the offset, and a header
// such as "Exif\0\0"), and EditExif keeps every byte it doesn't change
// where it was: an edit whose values fit where the old ones were (a new
// date, a GPS position replaced or removed) keeps the item's length and
// is in place, a patch of the item's data. Old values are zeroed, in the
// item and, when it moves, where it was.
func PlanExifEdit(src io.ReaderAt, size int64, edit ExifEdit) (*Plan, error) {
	if edit.Orientation != 0 {
		return nil, ErrOrientationInExif
	}
	f, err := parseHEIF(src, size)
	if err != nil {
		return nil, err
	}
	id, found, err := f.metadataItem(kindExif)
	if err != nil {
		return nil, err
	}
	if !found {
		tiff, err := EditExif(nil, edit)
		if err != nil {
			return nil, err
		}
		return PlanExif(src, size, tiff)
	}
	data, err := f.itemBytes(src, size, id)
	if err != nil {
		return nil, err
	}
	// A 4-byte offset to the TIFF header comes first (ISO/IEC 23008-12
	// A.2.1).
	if len(data) < 4 {
		return nil, fmt.Errorf("%w: an Exif item of %d bytes", ErrMalformed, len(data))
	}
	skip := 4 + uint64(binary.BigEndian.Uint32(data))
	if skip > uint64(len(data)) {
		return nil, fmt.Errorf("%w: the Exif item's TIFF offset is past its end", ErrMalformed)
	}
	tiff, err := EditExif(data[skip:], edit)
	if err != nil {
		return nil, err
	}
	return planItem(src, size, kindExif, append(data[:skip:skip], tiff...))
}
