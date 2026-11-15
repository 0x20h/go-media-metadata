package mediameta

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/0x20h/go-media-metadata/internal/bmff"
)

// ImageSize is an image's size in pixels as it's stored, and how it's
// shown.
type ImageSize struct {
	// Width and Height are the size as stored, before the orientation
	// turns the image.
	Width, Height int
	// Orientation is how the image is shown, as ReadOrientation reads it:
	// for HEIF and JPEG XL their own rotation and mirroring, for a movie
	// its track matrix, else the Exif tag. 0 when it can't be read.
	Orientation Orientation
}

// Shown is the size as the image is shown: width and height swapped for an
// orientation that turns it a quarter (5 to 8), as stored otherwise.
func (s ImageSize) Shown() (width, height int) {
	if s.Orientation >= 5 && s.Orientation <= 8 {
		return s.Height, s.Width
	}
	return s.Width, s.Height
}

// Tags of the sizes read here.
const (
	tagImageWidth      = 0x0100
	tagImageLength     = 0x0101
	tagExifImageWidth  = 0xA002
	tagExifImageHeight = 0xA003
	// A Panasonic RW2's IFD0: the sensor's borders around the image.
	tagRW2TopBorder    = 0x0004
	tagRW2LeftBorder   = 0x0005
	tagRW2BottomBorder = 0x0006
	tagRW2RightBorder  = 0x0007
)

// imageSizeFormats are the formats ReadImageSize takes.
const imageSizeFormats = "JPEG, PNG, WebP, HEIF, JPEG XL, camera RAW, QuickTime or MP4 file"

// ReadImageSize reads the size of the image in r (size bytes), and the
// orientation it's shown in:
//
//   - JPEG: the primary image's frame header (SOF), not Exif's
//     PixelXDimension;
//   - PNG: IHDR;
//   - WebP: VP8X's canvas, else the VP8 or VP8L frame;
//   - HEIF: the primary item's ispe (a grid's output size), before any
//     clap crops it;
//   - JPEG XL: the codestream's SizeHeader;
//   - camera RAW: the full-size image, as ExifTool's ImageSize gives it (see
//     docs/raw.md);
//   - QuickTime and MP4: the first video track's size, as Video has it.
//
// A file of none of these formats gives an error that is ErrUnknownFormat;
// one without a size it can read, ErrMalformed or ErrUnsupported.
func ReadImageSize(r io.ReaderAt, size int64) (ImageSize, error) {
	var s ImageSize
	var err error
	if f, herr := parseHEIF(r, size); herr == nil {
		s, err = f.imageSize()
	} else {
		switch detectFormat(r, size) {
		case formatJPEG:
			s, err = jpegImageSize(r, size)
		case formatPNG:
			s, err = pngImageSize(r, size)
		case formatWebP:
			s, err = webpImageSize(r, size)
		case formatJXL:
			s, err = jxlImageSize(r, size)
		case formatRAW:
			s, err = rawImageSize(r, size)
		case formatMovie, formatTS:
			v, verr := ReadVideo(r, size)
			if verr != nil {
				return ImageSize{}, verr
			}
			if v.Width <= 0 || v.Height <= 0 {
				return ImageSize{}, fmt.Errorf("%w: a movie without a video track size", ErrUnsupported)
			}
			return ImageSize{Width: v.Width, Height: v.Height, Orientation: v.Orientation}, nil
		default:
			if startsWithFtyp(r, size) && errors.Is(herr, ErrMalformed) {
				return ImageSize{}, herr
			}
			return ImageSize{}, formatError(imageSizeFormats)
		}
	}
	if err != nil {
		return ImageSize{}, err
	}
	if s.Width <= 0 || s.Height <= 0 {
		return ImageSize{}, fmt.Errorf("%w: an image size of %d×%d", ErrMalformed, s.Width, s.Height)
	}
	if o, err := ReadOrientation(r, size); err == nil {
		s.Orientation = o
	}
	return s, nil
}

// imageSize is the primary item's ispe.
func (f *heifFile) imageSize() (ImageSize, error) {
	_, entry := f.m.Prop.Entry(f.m.Primary)
	if entry != nil {
		for _, ref := range entry.Props {
			if ref.Index == 0 || int(ref.Index) > len(f.m.Prop.Props) {
				continue
			}
			p := f.m.Prop.Props[ref.Index-1]
			if p.Type != bmff.T("ispe") {
				continue
			}
			// A full box: 8-byte header, version and flags, then width
			// and height.
			if len(p.Raw) < 20 {
				return ImageSize{}, fmt.Errorf("%w: an ispe of %d bytes", ErrMalformed, len(p.Raw))
			}
			w, h := binary.BigEndian.Uint32(p.Raw[12:]), binary.BigEndian.Uint32(p.Raw[16:])
			if w > 1<<30 || h > 1<<30 {
				return ImageSize{}, fmt.Errorf("%w: an ispe of %d×%d", ErrMalformed, w, h)
			}
			return ImageSize{Width: int(w), Height: int(h)}, nil
		}
	}
	return ImageSize{}, fmt.Errorf("%w: a primary item without ispe", ErrMalformed)
}

// jpegImageSize reads the first frame header: SOF0 to SOF15, but for DHT
// (C4), JPG (C8) and DAC (CC), which share the range.
func jpegImageSize(r io.ReaderAt, size int64) (ImageSize, error) {
	f, err := parseJPEG(r, size)
	if err != nil {
		return ImageSize{}, err
	}
	for _, s := range f.segs {
		if s.marker < 0xC0 || s.marker > 0xCF || s.marker == 0xC4 || s.marker == 0xC8 || s.marker == 0xCC {
			continue
		}
		// precision, height, width
		var b [5]byte
		if s.bodyLen < 5 {
			return ImageSize{}, fmt.Errorf("%w: a frame header of %d bytes", ErrMalformed, s.bodyLen)
		}
		if _, err := r.ReadAt(b[:], s.body); err != nil {
			return ImageSize{}, fmt.Errorf("%w: %v", ErrMalformed, err)
		}
		return ImageSize{Width: int(binary.BigEndian.Uint16(b[3:])), Height: int(binary.BigEndian.Uint16(b[1:]))}, nil
	}
	return ImageSize{}, fmt.Errorf("%w: a JPEG without a frame header", ErrMalformed)
}

// pngImageSize reads IHDR, which a PNG starts with.
func pngImageSize(r io.ReaderAt, size int64) (ImageSize, error) {
	var b [24]byte
	if !isPNG(r, size) {
		return ImageSize{}, errNotPNG
	}
	if size < int64(len(b)) {
		return ImageSize{}, fmt.Errorf("%w: a PNG cut short", ErrMalformed)
	}
	if _, err := r.ReadAt(b[:], 0); err != nil {
		return ImageSize{}, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	if string(b[12:16]) != "IHDR" {
		return ImageSize{}, fmt.Errorf("%w: a PNG without IHDR first", ErrMalformed)
	}
	w, h := binary.BigEndian.Uint32(b[16:]), binary.BigEndian.Uint32(b[20:])
	if w > 1<<31-1 || h > 1<<31-1 {
		return ImageSize{}, fmt.Errorf("%w: an IHDR of %d×%d", ErrMalformed, w, h)
	}
	return ImageSize{Width: int(w), Height: int(h)}, nil
}

// webpImageSize reads VP8X's canvas size, else the image's own (WebP
// container specification; RFC 6386 9.1 for VP8, the WebP lossless
// bitstream specification for VP8L).
func webpImageSize(r io.ReaderAt, size int64) (ImageSize, error) {
	f, err := parseWebP(r, size)
	if err != nil {
		return ImageSize{}, err
	}
	for _, c := range f.chunks {
		var b [10]byte
		n := min(c.n, int64(len(b)))
		if _, err := r.ReadAt(b[:n], c.data()); err != nil {
			return ImageSize{}, fmt.Errorf("%w: %v", ErrMalformed, err)
		}
		u24 := func(p []byte) int { return int(p[0]) | int(p[1])<<8 | int(p[2])<<16 }
		switch string(c.fourcc[:]) {
		case "VP8X":
			if n < 10 {
				break
			}
			return ImageSize{Width: 1 + u24(b[4:]), Height: 1 + u24(b[7:])}, nil
		case "VP8 ":
			// A key frame: 3-byte tag, start code 9D 01 2A, then 14-bit
			// width and height, each with a 2-bit scale above.
			if n < 10 || b[0]&1 != 0 || !bytes.Equal(b[3:6], []byte{0x9D, 0x01, 0x2A}) {
				break
			}
			return ImageSize{Width: int(binary.LittleEndian.Uint16(b[6:]) & 0x3FFF), Height: int(binary.LittleEndian.Uint16(b[8:]) & 0x3FFF)}, nil
		case "VP8L":
			// Signature 0x2F, then 14 bits of width-1 and of height-1.
			if n < 5 || b[0] != 0x2F {
				break
			}
			v := binary.LittleEndian.Uint32(b[1:])
			return ImageSize{Width: 1 + int(v&0x3FFF), Height: 1 + int(v>>14&0x3FFF)}, nil
		}
	}
	return ImageSize{}, fmt.Errorf("%w: a WebP without VP8X, VP8 or VP8L", ErrMalformed)
}

// jxlImageSize reads the codestream's SizeHeader (ISO/IEC 18181-1 A.2).
func jxlImageSize(r io.ReaderAt, size int64) (ImageSize, error) {
	br, err := jxlHeaderBits(r, size)
	if err != nil {
		return ImageSize{}, err
	}
	var h, w uint32
	small := br.bit() == 1
	if small {
		h = (br.bits(5) + 1) * 8
	} else {
		h = br.u32(9, 13, 18, 30)
	}
	switch ratio := br.bits(3); ratio {
	case 0:
		if small {
			w = (br.bits(5) + 1) * 8
		} else {
			w = br.u32(9, 13, 18, 30)
		}
	default:
		num := [...]uint64{1: 1, 2: 12, 3: 4, 4: 3, 5: 16, 6: 5, 7: 2}[ratio]
		den := [...]uint64{1: 1, 2: 10, 3: 3, 4: 2, 5: 9, 6: 4, 7: 1}[ratio]
		w = uint32(uint64(h) * num / den)
	}
	if err := br.err(); err != nil {
		return ImageSize{}, err
	}
	return ImageSize{Width: int(w), Height: int(h)}, nil
}

// rawImageSize is a camera RAW's full-size image, as ExifTool's composite
// ImageSize gives it: for a Fujifilm RAF, RawImageCroppedSize; for a CR3,
// and a Canon CR2, the Exif IFD's ExifImageWidth and ExifImageHeight; for
// a Panasonic RW2, the image within the sensor's borders; for the other
// TIFF-based files, the largest full-resolution image (NewSubfileType 0)
// among IFD0 and its SubIFDs.
func rawImageSize(r io.ReaderAt, size int64) (ImageSize, error) {
	if _, _, isRAF, _ := openRAF(r, size); isRAF {
		return rafImageSize(r, size)
	}
	if c, ok, err := openCR3(r, size); err != nil {
		return ImageSize{}, err
	} else if ok {
		data, _, ok := c.tiff("CMT2")
		if ok {
			if t, first, ok := newTIFF(data); ok {
				if s, ok := t.exifImageSize(t.ifd(first)); ok {
					return s, nil
				}
			}
		}
		return ImageSize{}, fmt.Errorf("%w: a CR3 without ExifImageWidth", ErrUnsupported)
	}
	t, ok := openRAWTIFF(r, size)
	if !ok {
		return ImageSize{}, formatError(imageSizeFormats)
	}
	ifd0 := t.ifd(t.first)
	if ifd0 == nil {
		return ImageSize{}, fmt.Errorf("%w: IFD0 at %d outside the file", ErrMalformed, t.first)
	}
	dim := func(m map[uint16]field, tag uint16) int {
		f, ok := m[tag]
		if !ok || f.typ != typeShort && f.typ != typeLong {
			return 0
		}
		v, _ := t.uint(f)
		return int(v)
	}
	// RW2: the borders, when they leave an image.
	if top, left, bottom, right := dim(ifd0, tagRW2TopBorder), dim(ifd0, tagRW2LeftBorder), dim(ifd0, tagRW2BottomBorder), dim(ifd0, tagRW2RightBorder); t.rw2() && right > left && bottom > top {
		return ImageSize{Width: right - left, Height: bottom - top}, nil
	}
	if mk, _ := ascii(ifd0[0x010F]); strings.HasPrefix(mk, "Canon") { // IFD0's Make
		if f, ok := ifd0[tagExifIFD]; ok {
			if off, ok := t.uint(f); ok {
				if s, ok := t.exifImageSize(t.ifd(off)); ok {
					return s, nil
				}
			}
		}
	}
	var best ImageSize
	consider := func(m map[uint16]field) {
		if f, ok := m[tagNewSubfileType]; ok {
			if v, _ := t.uint(f); v&1 != 0 {
				return // a reduced-resolution image
			}
		}
		w, h := dim(m, tagImageWidth), dim(m, tagImageLength)
		if w*h > best.Width*best.Height {
			best = ImageSize{Width: w, Height: h}
		}
	}
	consider(ifd0)
	if f, ok := ifd0[tagSubIFDs]; ok && (f.typ == typeLong || f.typ == typeIFD) {
		for i := 0; i < f.count && i < maxSubIFDs && 4*i+4 <= len(f.value); i++ {
			if off := t.order.Uint32(f.value[4*i:]); off != t.first {
				consider(t.ifd(off))
			}
		}
	}
	if best.Width == 0 {
		return ImageSize{}, fmt.Errorf("%w: a RAW without an image size", ErrUnsupported)
	}
	return best, nil
}

// exifImageSize reads ExifImageWidth and ExifImageHeight (SHORT or LONG).
func (t tiffReader) exifImageSize(exif map[uint16]field) (ImageSize, bool) {
	w, okW := t.uint(exif[tagExifImageWidth])
	h, okH := t.uint(exif[tagExifImageHeight])
	if !okW || !okH || w == 0 || h == 0 || w > 1<<30 || h > 1<<30 {
		return ImageSize{}, false
	}
	return ImageSize{Width: int(w), Height: int(h)}, true
}

// rafImageSize reads RawImageCroppedSize (tag 0x0111: height, then width)
// from a RAF's metadata directory, whose offset is at byte 92 of the
// header: a count, then entries of a tag, a length and the value, all
// big-endian.
func rafImageSize(r io.ReaderAt, size int64) (ImageSize, error) {
	var h [100]byte
	if size < int64(len(h)) {
		return ImageSize{}, fmt.Errorf("%w: a RAF header cut short", ErrMalformed)
	}
	if _, err := r.ReadAt(h[:], 0); err != nil {
		return ImageSize{}, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	at, n := int64(binary.BigEndian.Uint32(h[92:])), int64(binary.BigEndian.Uint32(h[96:]))
	if at < int64(len(h)) || n < 4 || at+n > size || n > 1<<16 {
		return ImageSize{}, fmt.Errorf("%w: a RAF without a metadata directory", ErrUnsupported)
	}
	dir := make([]byte, n)
	if _, err := r.ReadAt(dir, at); err != nil {
		return ImageSize{}, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	count := binary.BigEndian.Uint32(dir)
	for p, i := 4, uint32(0); i < count && p+4 <= len(dir); i++ {
		tag, l := binary.BigEndian.Uint16(dir[p:]), int(binary.BigEndian.Uint16(dir[p+2:]))
		p += 4
		if p+l > len(dir) {
			break
		}
		if tag == 0x0111 && l == 4 {
			hh, ww := int(binary.BigEndian.Uint16(dir[p:])), int(binary.BigEndian.Uint16(dir[p+2:]))
			return ImageSize{Width: ww, Height: hh}, nil
		}
		p += l
	}
	return ImageSize{}, fmt.Errorf("%w: a RAF without RawImageCroppedSize", ErrUnsupported)
}
