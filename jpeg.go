package mediameta

import (
	"bytes"
	"cmp"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"slices"

	"github.com/0x20h/go-media-metadata/internal/bmff"
)

// ErrNotJPEG is returned for a file that isn't a JPEG file.
var ErrNotJPEG = errors.New("not a JPEG file")

// JPEG markers (ITU-T T.81 B.1.1.3, and JFIF/Exif for the APPn ones).
const (
	markerSOI  = 0xD8
	markerEOI  = 0xD9
	markerSOS  = 0xDA
	markerTEM  = 0x01
	markerRST0 = 0xD0
	markerRST7 = 0xD7
	markerAPP1 = 0xE1
)

// maxSegments caps the marker segments read before the image data; a
// camera file has a dozen or so.
const maxSegments = 4096

// exifHeader starts an APP1 segment that holds Exif (CIPA DC-008 4.5.4).
var exifHeader = []byte("Exif\x00\x00")

// jpegSegment is one marker segment of a JPEG file's header.
type jpegSegment struct {
	marker byte
	// offset is where its 0xFF marker byte is in the file.
	offset int64
	// body and bodyLen place its payload, after the 2-byte length.
	body, bodyLen int64
}

// isJPEG reports whether the file starts with a JPEG's SOI marker and the
// start of another marker.
func isJPEG(r io.ReaderAt, size int64) bool {
	if size < 3 {
		return false
	}
	var b [3]byte
	if _, err := r.ReadAt(b[:], 0); err != nil {
		return false
	}
	return b[0] == 0xFF && b[1] == markerSOI && b[2] == 0xFF
}

// jpegFile is a JPEG file's marker segments, up to and including the
// first SOS (start of scan): the metadata segments all come before it, and
// everything after it is image data this package copies as it is.
type jpegFile struct {
	segs []jpegSegment
}

func parseJPEG(r io.ReaderAt, size int64) (*jpegFile, error) {
	if !isJPEG(r, size) {
		return nil, ErrNotJPEG
	}
	f := &jpegFile{}
	at := int64(2)
	var hdr [4]byte
	for {
		if len(f.segs) >= maxSegments {
			return nil, fmt.Errorf("%w: more than %d JPEG marker segments", ErrMalformed, maxSegments)
		}
		if at+2 > size {
			return nil, fmt.Errorf("%w: the JPEG ends before its image data", ErrMalformed)
		}
		if _, err := r.ReadAt(hdr[:2], at); err != nil {
			return nil, fmt.Errorf("%w: reading a marker: %v", ErrMalformed, err)
		}
		if hdr[0] != 0xFF {
			return nil, fmt.Errorf("%w: no JPEG marker at offset %d", ErrMalformed, at)
		}
		m := hdr[1]
		switch {
		case m == 0xFF:
			// A fill byte before a marker (T.81 B.1.1.2).
			at++
			continue
		case m == 0x00 || m == markerSOI:
			return nil, fmt.Errorf("%w: JPEG marker 0x%02X at offset %d", ErrMalformed, m, at)
		case m == markerEOI:
			return nil, fmt.Errorf("%w: the JPEG ends before its image data", ErrMalformed)
		case m == markerTEM || (m >= markerRST0 && m <= markerRST7):
			// No length and no payload.
			f.segs = append(f.segs, jpegSegment{marker: m, offset: at, body: at + 2})
			at += 2
			continue
		}
		if at+4 > size {
			return nil, fmt.Errorf("%w: the JPEG ends in a marker segment", ErrMalformed)
		}
		if _, err := r.ReadAt(hdr[2:4], at+2); err != nil {
			return nil, fmt.Errorf("%w: reading a segment length: %v", ErrMalformed, err)
		}
		n := int64(hdr[2])<<8 | int64(hdr[3])
		if n < 2 || at+2+n > size {
			return nil, fmt.Errorf("%w: JPEG segment 0x%02X at offset %d of %d bytes", ErrMalformed, m, at, n)
		}
		f.segs = append(f.segs, jpegSegment{marker: m, offset: at, body: at + 4, bodyLen: n - 2})
		if m == markerSOS {
			return f, nil
		}
		at += 2 + n
	}
}

// exifSegment is the first APP1 segment that holds Exif; ok is false
// without one.
func (f *jpegFile) exifSegment(r io.ReaderAt) (seg jpegSegment, payload []byte, ok bool, err error) {
	for _, s := range f.segs {
		if s.marker != markerAPP1 || s.bodyLen < int64(len(exifHeader))+8 {
			continue
		}
		head := make([]byte, len(exifHeader))
		if _, err := r.ReadAt(head, s.body); err != nil {
			return s, nil, false, fmt.Errorf("%w: reading APP1: %v", ErrMalformed, err)
		}
		// "Exif" and a NUL, then a pad byte that should be another NUL;
		// some writers put 0xFF there, and ExifTool reads those too.
		if !bytes.Equal(head[:5], exifHeader[:5]) {
			continue
		}
		payload := make([]byte, s.bodyLen)
		if _, err := r.ReadAt(payload, s.body); err != nil {
			return s, nil, false, fmt.Errorf("%w: reading APP1: %v", ErrMalformed, err)
		}
		// One without a TIFF header is passed over for the next, as
		// ExifTool does.
		if _, _, ok := newTIFF(payload[len(exifHeader):]); !ok {
			continue
		}
		return s, payload, true, nil
	}
	return jpegSegment{}, nil, false, nil
}

// readJPEGExif reads the Exif of the JPEG file in r.
func readJPEGExif(r io.ReaderAt, size int64) (Exif, error) {
	f, err := parseJPEG(r, size)
	if err != nil {
		return Exif{}, err
	}
	_, payload, ok, err := f.exifSegment(r)
	if err != nil || !ok {
		return Exif{}, err
	}
	return parseExif(payload[len(exifHeader):]), nil
}

// exifItems are the HEIF file's Exif items in the order they're tried:
// those that describe the primary image (a cdsc reference to it) first,
// then the others, each by item id.
func (f *heifFile) exifItems() []uint32 {
	var ids []uint32
	for id, t := range f.m.ItemTypes {
		if t == bmff.T("Exif") {
			ids = append(ids, id)
		}
	}
	primary := map[uint32]bool{}
	for _, ref := range f.m.Refs {
		if ref.Type == bmff.T("cdsc") && slices.Contains(ref.To, f.m.Primary) {
			primary[ref.From] = true
		}
	}
	slices.SortFunc(ids, func(a, b uint32) int {
		if primary[a] != primary[b] {
			if primary[a] {
				return -1
			}
			return 1
		}
		return cmp.Compare(a, b)
	})
	return ids
}

// readHEIFExif reads the Exif item of the HEIF file in r, as far as its
// first maxExifHead bytes go: the first of exifItems that holds TIFF data.
func readHEIFExif(r io.ReaderAt, size int64) (Exif, error) {
	f, err := parseHEIF(r, size)
	if err != nil {
		return Exif{}, err
	}
	var oldMeta []byte
	for _, id := range f.exifItems() {
		if oldMeta == nil {
			oldMeta = make([]byte, f.meta.Size)
			if _, err := r.ReadAt(oldMeta, f.meta.Offset); err != nil {
				return Exif{}, fmt.Errorf("%w: reading the meta box: %v", ErrMalformed, err)
			}
		}
		it, ok, err := f.readExifItem(r, size, id, oldMeta)
		if err != nil {
			return Exif{}, err
		}
		if !ok || len(it.head) < 4 {
			continue
		}
		// A 4-byte offset to the TIFF header comes first (ISO/IEC
		// 23008-12 A.2.1).
		skip := 4 + uint64(binary.BigEndian.Uint32(it.head))
		if skip > uint64(len(it.head)) {
			continue
		}
		if _, _, ok := newTIFF(it.head[skip:]); !ok {
			continue
		}
		return parseExif(it.head[skip:]), nil
	}
	return Exif{}, nil
}

// ReadExif reads the Exif metadata of the JPEG or HEIF file in r (size
// bytes): a JPEG's first Exif APP1 segment, a HEIF file's Exif item. A
// file without Exif gives an empty Exif and no error. A file that is
// neither format gives an error that is both ErrNotJPEG and ErrNotHEIF.
func ReadExif(r io.ReaderAt, size int64) (Exif, error) {
	if isJPEG(r, size) {
		return readJPEGExif(r, size)
	}
	if !startsWithFtyp(r, size) {
		return Exif{}, fmt.Errorf("%w: %w", ErrNotJPEG, ErrNotHEIF)
	}
	x, err := readHEIFExif(r, size)
	if errors.Is(err, ErrNotHEIF) {
		return Exif{}, fmt.Errorf("%w: %w", ErrNotJPEG, err)
	}
	return x, err
}

// startsWithFtyp reports whether the file starts with an ftyp box, as an
// ISO base media file (a HEIF file among them) does.
func startsWithFtyp(r io.ReaderAt, size int64) bool {
	var b [8]byte
	if size < 8 {
		return false
	}
	if _, err := r.ReadAt(b[:], 0); err != nil {
		return false
	}
	return string(b[4:]) == "ftyp"
}
