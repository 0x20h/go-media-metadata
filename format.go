package mediameta

import (
	"encoding/binary"
	"io"

	"github.com/0x20h/go-media-metadata/internal/bmff"
)

// format is a file format this package reads, as its first bytes say.
type format int

const (
	formatUnknown format = iota
	formatHEIF
	formatJPEG
	formatPNG
	formatWebP
	formatJXL
	formatRAW // TIFF-based, CR3 or RAF
	formatMovie
)

// detectFormat tells a file's format from its first bytes, and for an
// ISO base media file from its ftyp brands: a HEIF image, a CR3, or a
// movie.
func detectFormat(r io.ReaderAt, size int64) format {
	switch {
	case isJPEG(r, size):
		return formatJPEG
	case isPNG(r, size):
		return formatPNG
	case isWebP(r, size):
		return formatWebP
	case isJXL(r, size):
		return formatJXL
	}
	if _, ok := openRAWTIFF(r, size); ok {
		return formatRAW
	}
	if _, _, isRAF, _ := openRAF(r, size); isRAF {
		return formatRAW
	}
	if startsWithFtyp(r, size) {
		var h [12]byte
		if _, err := r.ReadAt(h[:], 0); err != nil {
			return formatUnknown
		}
		if string(h[8:12]) == "crx " {
			return formatRAW
		}
		n := int64(binary.BigEndian.Uint32(h[:4]))
		if n < 16 || n > size {
			return formatUnknown
		}
		ftyp := bmff.Box{Type: bmff.T("ftyp"), Offset: 0, HeaderLen: 8, Size: n}
		if checkBrands(r, ftyp) == nil {
			return formatHEIF
		}
		if checkMovie(r, []bmff.Box{ftyp}) == nil {
			return formatMovie
		}
		return formatUnknown
	}
	if looksLikeMovie(r, size) {
		return formatMovie
	}
	return formatUnknown
}
