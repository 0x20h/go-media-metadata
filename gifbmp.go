package mediameta

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"strings"
)

// BMP and GIF: read only. A BMP's size is in its info header; a GIF's in
// its logical screen descriptor, and its comment and XMP in extension
// blocks that can come anywhere between the frames.

const (
	errNotBMP = formatError("BMP file")
	errNotGIF = formatError("GIF file")
)

// isBMP: "BM", then an info header of a size BMP versions use (12 for
// OS/2 1.x and Windows 2, 16 and 64 for OS/2 2.x, 40 to 124 for Windows),
// all within the file. "BM" alone is too common a start to go by.
func isBMP(r io.ReaderAt, size int64) bool {
	var h [18]byte
	if size < int64(len(h)) {
		return false
	}
	if _, err := r.ReadAt(h[:], 0); err != nil || h[0] != 'B' || h[1] != 'M' {
		return false
	}
	switch n := binary.LittleEndian.Uint32(h[14:]); n {
	case 12, 16, 40, 52, 56, 64, 108, 124:
		return 14+int64(n) <= size
	}
	return false
}

// bmpImageSize reads the width and height of a BMP's info header. A
// negative height is a top-down image; the size is its absolute value.
func bmpImageSize(r io.ReaderAt, size int64) (ImageSize, error) {
	if !isBMP(r, size) {
		return ImageSize{}, errNotBMP
	}
	var h [26]byte
	n := min(int64(len(h)), size)
	if _, err := r.ReadAt(h[:n], 0); err != nil {
		return ImageSize{}, fmt.Errorf("%w: BMP header: %v", ErrMalformed, err)
	}
	var w, ht int64
	if binary.LittleEndian.Uint32(h[14:]) == 12 {
		// BITMAPCOREHEADER: unsigned 16-bit width and height.
		w, ht = int64(binary.LittleEndian.Uint16(h[18:])), int64(binary.LittleEndian.Uint16(h[20:]))
	} else {
		w = int64(int32(binary.LittleEndian.Uint32(h[18:])))
		ht = int64(int32(binary.LittleEndian.Uint32(h[22:])))
		if ht < 0 {
			ht = -ht
		}
	}
	if w <= 0 || ht <= 0 {
		return ImageSize{}, fmt.Errorf("%w: a BMP of %d×%d", ErrMalformed, w, ht)
	}
	return ImageSize{Width: int(w), Height: int(ht)}, nil
}

// isGIF: "GIF87a" or "GIF89a" and a whole logical screen descriptor.
func isGIF(r io.ReaderAt, size int64) bool {
	var h [6]byte
	if size < 13 {
		return false
	}
	if _, err := r.ReadAt(h[:], 0); err != nil {
		return false
	}
	return string(h[:]) == "GIF87a" || string(h[:]) == "GIF89a"
}

func gifImageSize(r io.ReaderAt, size int64) (ImageSize, error) {
	if !isGIF(r, size) {
		return ImageSize{}, errNotGIF
	}
	var d [4]byte
	if _, err := r.ReadAt(d[:], 6); err != nil {
		return ImageSize{}, fmt.Errorf("%w: GIF screen descriptor: %v", ErrMalformed, err)
	}
	w, h := int(binary.LittleEndian.Uint16(d[:])), int(binary.LittleEndian.Uint16(d[2:]))
	if w == 0 || h == 0 {
		return ImageSize{}, fmt.Errorf("%w: a GIF of %d×%d", ErrMalformed, w, h)
	}
	return ImageSize{Width: w, Height: h}, nil
}

// gifMeta is what a GIF's extension blocks hold.
type gifMeta struct {
	comments []string
	xmp      []byte
}

// xmpAppID is the application extension XMP is in: "XMP Data" with the
// authentication code "XMP" (XMP Specification Part 3, 1.1.2).
const xmpAppID = "XMP DataXMP"

// xmpMagicTrailer is what follows a GIF's XMP packet: 0x01, then 0xFF down
// to 0x00, so that a reader walking it as sub-blocks finds lengths that
// lead to the block's terminator wherever it starts.
var xmpMagicTrailer = func() []byte {
	b := []byte{0x01}
	for i := 0xFF; i >= 0; i-- {
		b = append(b, byte(i))
	}
	return b
}()

// readGIFMeta walks a GIF's blocks to the trailer and collects the
// comment extensions and the XMP application extension. The walk is one
// pass through a buffered reader; frames' data is skipped sub-block by
// sub-block, never decoded.
func readGIFMeta(r io.ReaderAt, size int64) (gifMeta, error) {
	var m gifMeta
	if !isGIF(r, size) {
		return m, errNotGIF
	}
	br := bufio.NewReaderSize(io.NewSectionReader(r, 0, size), 32<<10)
	var head [13]byte
	if _, err := io.ReadFull(br, head[:]); err != nil {
		return m, fmt.Errorf("%w: GIF header: %v", ErrMalformed, err)
	}
	if head[10]&0x80 != 0 { // a global color table
		if err := discard(br, 3<<(head[10]&7+1)); err != nil {
			return m, err
		}
	}
	var metaBytes int
	for {
		kind, err := br.ReadByte()
		if err != nil {
			return m, fmt.Errorf("%w: GIF without its trailer: %v", ErrMalformed, err)
		}
		switch kind {
		case 0x3B: // the trailer
			return m, nil
		case 0x2C: // an image: its descriptor, a local color table, the LZW data
			var d [9]byte
			if _, err := io.ReadFull(br, d[:]); err != nil {
				return m, fmt.Errorf("%w: GIF image descriptor: %v", ErrMalformed, err)
			}
			if d[8]&0x80 != 0 {
				if err := discard(br, 3<<(d[8]&7+1)); err != nil {
					return m, err
				}
			}
			if _, err := br.ReadByte(); err != nil { // LZW minimum code size
				return m, fmt.Errorf("%w: GIF image data: %v", ErrMalformed, err)
			}
			if err := skipSubBlocks(br); err != nil {
				return m, err
			}
		case 0x21: // an extension
			label, err := br.ReadByte()
			if err != nil {
				return m, fmt.Errorf("%w: GIF extension: %v", ErrMalformed, err)
			}
			switch label {
			case 0xFE: // a comment
				text, err := readSubBlocks(br, maxMetadataItem-metaBytes)
				if err != nil {
					return m, err
				}
				metaBytes += len(text)
				m.comments = append(m.comments, string(text))
			case 0xFF: // an application extension
				id, err := readSubBlock(br)
				if err != nil {
					return m, err
				}
				if string(id) != xmpAppID || m.xmp != nil {
					if err := skipSubBlocks(br); err != nil {
						return m, err
					}
					continue
				}
				x, err := readGIFXMP(br, maxMetadataItem-metaBytes)
				if err != nil {
					return m, err
				}
				metaBytes += len(x)
				m.xmp = x
			default:
				if err := skipSubBlocks(br); err != nil {
					return m, err
				}
			}
		default:
			return m, fmt.Errorf("%w: a GIF block of kind 0x%02X", ErrMalformed, kind)
		}
	}
}

// readGIFXMP reads the XMP packet after its application extension's
// identifier: the packet's bytes as they are (not as sub-blocks), up to
// the magic trailer, which then runs to the block terminator.
func readGIFXMP(br *bufio.Reader, limit int) ([]byte, error) {
	var buf bytes.Buffer
	for {
		b, err := br.ReadByte()
		if err != nil {
			return nil, fmt.Errorf("%w: GIF XMP without its trailer: %v", ErrMalformed, err)
		}
		buf.WriteByte(b)
		if buf.Len() > limit+len(xmpMagicTrailer)+1 {
			return nil, fmt.Errorf("%w: GIF XMP longer than %d bytes", ErrMalformed, limit)
		}
		// The trailer ends 0x02 0x01 0x00, then the terminator 0x00; only
		// there is the whole trailer compared.
		got := buf.Bytes()
		if b == 0x00 && len(got) >= len(xmpMagicTrailer) && got[len(got)-2] == 0x01 && got[len(got)-3] == 0x02 &&
			bytes.HasSuffix(got, xmpMagicTrailer) {
			if t, err := br.ReadByte(); err != nil || t != 0 {
				return nil, fmt.Errorf("%w: GIF XMP trailer without its terminator", ErrMalformed)
			}
			return bytes.Clone(buf.Bytes()[:buf.Len()-len(xmpMagicTrailer)]), nil
		}
	}
}

// readSubBlock reads one sub-block: a length byte and that many bytes.
func readSubBlock(br *bufio.Reader) ([]byte, error) {
	n, err := br.ReadByte()
	if err != nil {
		return nil, fmt.Errorf("%w: GIF sub-block: %v", ErrMalformed, err)
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(br, b); err != nil {
		return nil, fmt.Errorf("%w: GIF sub-block: %v", ErrMalformed, err)
	}
	return b, nil
}

// readSubBlocks reads sub-blocks to their terminator, joined, at most
// limit bytes.
func readSubBlocks(br *bufio.Reader, limit int) ([]byte, error) {
	var out []byte
	for {
		b, err := readSubBlock(br)
		if err != nil {
			return nil, err
		}
		if len(b) == 0 {
			return out, nil
		}
		if len(out)+len(b) > limit {
			return nil, fmt.Errorf("%w: GIF metadata longer than %d bytes", ErrMalformed, maxMetadataItem)
		}
		out = append(out, b...)
	}
}

// skipSubBlocks skips sub-blocks to their terminator.
func skipSubBlocks(br *bufio.Reader) error {
	for {
		n, err := br.ReadByte()
		if err != nil {
			return fmt.Errorf("%w: GIF data without its terminator: %v", ErrMalformed, err)
		}
		if n == 0 {
			return nil
		}
		if err := discard(br, int(n)); err != nil {
			return err
		}
	}
}

func discard(br *bufio.Reader, n int) error {
	if got, err := br.Discard(n); err != nil || got != n {
		return fmt.Errorf("%w: GIF cut short", ErrMalformed)
	}
	return nil
}

// ReadComment returns the comment a file carries as text of its own: a
// GIF's comment extensions, joined by a line break when there are several.
// "" without one. Other formats keep their description elsewhere (Exif
// ImageDescription, XMP dc:description) and give ErrUnknownFormat here.
func ReadComment(r io.ReaderAt, size int64) (string, error) {
	if !isGIF(r, size) {
		return "", errNotGIF
	}
	m, err := readGIFMeta(r, size)
	if err != nil {
		return "", err
	}
	return strings.Join(m.comments, "\n"), nil
}

// readGIFXMPPacket is ReadXMP for a GIF: nil without one.
func readGIFXMPPacket(r io.ReaderAt, size int64) ([]byte, error) {
	m, err := readGIFMeta(r, size)
	if err != nil {
		return nil, err
	}
	return m.xmp, nil
}
