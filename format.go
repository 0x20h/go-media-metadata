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
	formatTS // an MPEG-2 transport stream: .ts, .mts, .m2ts
	formatBMP
	formatGIF
	formatMatroska // Matroska or WebM
	formatAVI      // RIFF AVI
	formatASF      // ASF: WMV, WMA
	formatMPEGPS   // an MPEG program stream
)

// detectFormat tells a file's format from its first bytes, and for an
// ISO base media file from its ftyp brands: a HEIF image, a CR3, or a
// movie.
func detectFormat(r io.ReaderAt, size int64) format {
	return detectFormatTop(r, size, nil)
}

// detectFormatTop is detectFormat of a file whose top-level boxes a caller
// has read (nil when it hasn't, or they don't read as boxes). A file
// whose first box is ftyp can only be a HEIF, a movie or a CR3: the
// signatures the other formats are told by aren't read for it.
func detectFormatTop(r io.ReaderAt, size int64, top []bmff.Box) format {
	if len(top) > 0 && top[0].Type == bmff.T("ftyp") {
		return ftypFormat(r, size)
	}
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
		return ftypFormat(r, size)
	}
	if looksLikeMovie(r, size) {
		return formatMovie
	}
	if isTS(r, size) {
		return formatTS
	}
	if isMatroska(r, size) {
		return formatMatroska
	}
	if isAVI(r, size) {
		return formatAVI
	}
	if isASF(r, size) {
		return formatASF
	}
	if isMPEGPS(r, size) {
		return formatMPEGPS
	}
	switch {
	case isGIF(r, size):
		return formatGIF
	case isBMP(r, size):
		return formatBMP
	}
	return formatUnknown
}

// ftypFormat is the format of a file that starts with an ftyp box.
func ftypFormat(r io.ReaderAt, size int64) format {
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
