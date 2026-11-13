package mediameta

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
)

// ErrNotPNG is returned for a file that isn't a PNG.
var ErrNotPNG = errors.New("not a PNG file")

var pngSignature = []byte("\x89PNG\r\n\x1a\n")

// xmpKeyword is the iTXt keyword of a PNG's XMP (XMP specification part
// 3, 1.1.5).
const xmpKeyword = "XML:com.adobe.xmp"

// maxPNGChunks caps the chunks walked in one file.
const maxPNGChunks = 1 << 16

// pngChunk is one chunk: at is where its length field is, n the length of
// its data.
type pngChunk struct {
	typ [4]byte
	at  int64
	n   int64
}

func (c pngChunk) data() int64 { return c.at + 8 }
func (c pngChunk) end() int64  { return c.at + 12 + c.n }

func isPNG(r io.ReaderAt, size int64) bool {
	var b [8]byte
	if size < 8 {
		return false
	}
	if _, err := r.ReadAt(b[:], 0); err != nil {
		return false
	}
	return bytes.Equal(b[:], pngSignature)
}

// pngChunks walks a PNG's chunks up to IEND.
func pngChunks(r io.ReaderAt, size int64) ([]pngChunk, error) {
	if !isPNG(r, size) {
		return nil, ErrNotPNG
	}
	var out []pngChunk
	for at := int64(8); at < size; {
		if len(out) >= maxPNGChunks {
			return nil, fmt.Errorf("%w: more than %d PNG chunks", ErrMalformed, maxPNGChunks)
		}
		var h [8]byte
		if at+12 > size {
			return nil, fmt.Errorf("%w: a PNG chunk past the end", ErrMalformed)
		}
		if _, err := r.ReadAt(h[:], at); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrMalformed, err)
		}
		c := pngChunk{at: at, n: int64(binary.BigEndian.Uint32(h[:]))}
		copy(c.typ[:], h[4:])
		if c.n > 1<<31-1 || c.end() > size {
			return nil, fmt.Errorf("%w: PNG chunk %q of %d bytes", ErrMalformed, c.typ[:], c.n)
		}
		out = append(out, c)
		at = c.end()
		if string(c.typ[:]) == "IEND" {
			break
		}
	}
	if len(out) == 0 || string(out[0].typ[:]) != "IHDR" {
		return nil, fmt.Errorf("%w: a PNG without IHDR first", ErrMalformed)
	}
	return out, nil
}

func readChunk(r io.ReaderAt, at, n int64) ([]byte, error) {
	if n > maxMetadataItem {
		return nil, fmt.Errorf("%w: a metadata chunk of %d bytes", ErrUnsupported, n)
	}
	b := make([]byte, n)
	if _, err := r.ReadAt(b, at); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	return b, nil
}

// pngChunkBytes is a whole chunk: length, type, data, CRC.
func pngChunkBytes(typ string, data []byte) []byte {
	b := binary.BigEndian.AppendUint32(nil, uint32(len(data)))
	b = append(b, typ...)
	b = append(b, data...)
	return binary.BigEndian.AppendUint32(b, crc32.ChecksumIEEE(b[4:]))
}

// itxt is a parsed iTXt chunk: its keyword, the bytes before its text,
// and its text (inflated where it was compressed).
type itxt struct {
	keyword    string
	head       []byte
	text       []byte
	compressed bool
}

func parseITXt(data []byte) (itxt, bool) {
	k := bytes.IndexByte(data, 0)
	if k < 0 || k+3 > len(data) {
		return itxt{}, false
	}
	t := itxt{keyword: string(data[:k]), compressed: data[k+1] == 1}
	rest := data[k+3:]
	l := bytes.IndexByte(rest, 0) // language tag
	if l < 0 {
		return itxt{}, false
	}
	tk := bytes.IndexByte(rest[l+1:], 0) // translated keyword
	if tk < 0 {
		return itxt{}, false
	}
	headLen := k + 3 + l + 1 + tk + 1
	t.head, t.text = data[:headLen], data[headLen:]
	if t.compressed {
		zr, err := zlib.NewReader(bytes.NewReader(t.text))
		if err != nil {
			return itxt{}, false
		}
		text, err := io.ReadAll(io.LimitReader(zr, maxMetadataItem+1))
		if err != nil || len(text) > maxMetadataItem {
			return itxt{}, false
		}
		t.text = text
	}
	return t, true
}

// pngMetadata finds a PNG's Exif (the eXIf chunks) and XMP (the iTXt
// chunks with the XMP keyword): the first of each is the one read.
type pngMetadata struct {
	chunks []pngChunk
	exif   []int // indices in chunks
	xmp    []int
}

func readPNGMetadata(r io.ReaderAt, size int64) (*pngMetadata, error) {
	chunks, err := pngChunks(r, size)
	if err != nil {
		return nil, err
	}
	m := &pngMetadata{chunks: chunks}
	for i, c := range chunks {
		switch string(c.typ[:]) {
		case "eXIf":
			m.exif = append(m.exif, i)
		case "iTXt":
			kw := make([]byte, min(c.n, int64(len(xmpKeyword)+1)))
			if _, err := r.ReadAt(kw, c.data()); err == nil && string(kw) == xmpKeyword+"\x00" {
				m.xmp = append(m.xmp, i)
			}
		}
	}
	return m, nil
}

// tiffPayload is the TIFF data of an Exif payload that may start with
// "Exif\0\0", as some writers put it in a PNG's eXIf or a WebP's EXIF:
// the prefix and the TIFF data.
func tiffPayload(b []byte) (prefix, tiff []byte) {
	if bytes.HasPrefix(b, exifHeader) {
		return b[:len(exifHeader)], b[len(exifHeader):]
	}
	return nil, b
}

func readPNGExif(r io.ReaderAt, size int64) (Exif, error) {
	m, err := readPNGMetadata(r, size)
	if err != nil || len(m.exif) == 0 {
		return Exif{}, err
	}
	c := m.chunks[m.exif[0]]
	b, err := readChunk(r, c.data(), c.n)
	if err != nil {
		return Exif{}, err
	}
	_, tiff := tiffPayload(b)
	return parseExif(tiff), nil
}

func readPNGXMP(r io.ReaderAt, size int64) ([]byte, error) {
	m, err := readPNGMetadata(r, size)
	if err != nil || len(m.xmp) == 0 {
		return nil, err
	}
	c := m.chunks[m.xmp[0]]
	b, err := readChunk(r, c.data(), c.n)
	if err != nil {
		return nil, err
	}
	t, ok := parseITXt(b)
	if !ok {
		return nil, fmt.Errorf("%w: a malformed XMP iTXt chunk", ErrMalformed)
	}
	return t.text, nil
}

// planPNGChunk sets a PNG's Exif (kindExif, the eXIf chunk) or XMP
// (kindXMP, an iTXt chunk): edit makes the new payload from the old one
// (nil without one). A payload no longer than the old one's fills its
// chunk, padded (zeros after TIFF data, spaces after XMP), with the CRC
// rewritten: in place, the old value overwritten. A longer one replaces
// the chunk; a new chunk goes right after IHDR, before the image data,
// where the PNG specification puts eXIf. Further chunks of the kind are
// removed, so no old value stays in the file.
func planPNGChunk(r io.ReaderAt, size int64, kind metadataKind, edit func(old []byte) ([]byte, error)) (*Plan, error) {
	m, err := readPNGMetadata(r, size)
	if err != nil {
		return nil, err
	}
	idx, typ, pad := m.exif, "eXIf", byte(0)
	if kind == kindXMP {
		idx, typ, pad = m.xmp, "iTXt", ' '
	}
	e := &spliceEdit{}
	var old []byte
	var first *pngChunk
	var head []byte // an iTXt's bytes before its text, kept in place
	compressed := false
	if len(idx) > 0 {
		c := m.chunks[idx[0]]
		first = &c
		b, err := readChunk(r, c.data(), c.n)
		if err != nil {
			return nil, err
		}
		old = b
		if kind == kindXMP {
			t, ok := parseITXt(b)
			if !ok {
				return nil, fmt.Errorf("%w: a malformed XMP iTXt chunk", ErrMalformed)
			}
			old, head, compressed = t.text, t.head, t.compressed
		}
	}
	payload, err := edit(old)
	if err != nil {
		return nil, err
	}
	switch {
	case first != nil && !compressed:
		// In its chunk when it fits; else the chunk is replaced.
		room := int(first.n) - len(head)
		data := append(append([]byte(nil), head...), payload...)
		if padded, ok := padTo(payload, room, pad); ok {
			data = append(append([]byte(nil), head...), padded...)
		}
		e.add(splice{at: first.at, del: first.end() - first.at, data: pngChunkBytes(typ, data)})
	case first != nil:
		// A compressed XMP chunk is replaced by an uncompressed one.
		e.add(splice{at: first.at, del: first.end() - first.at, data: pngChunkBytes(typ, xmpITXt(payload))})
	default:
		data := payload
		if kind == kindXMP {
			data = xmpITXt(payload)
		}
		e.add(splice{at: m.chunks[0].end(), data: pngChunkBytes(typ, data)})
	}
	if len(idx) > 1 {
		for _, i := range idx[1:] {
			c := m.chunks[i]
			e.add(splice{at: c.at, del: c.end() - c.at})
		}
	}
	if err := e.sorted(); err != nil {
		return nil, err
	}
	return e.plan(r, size), nil
}

// xmpITXt is the data of an uncompressed iTXt chunk holding XMP.
func xmpITXt(packet []byte) []byte {
	b := append([]byte(xmpKeyword), 0, 0, 0) // keyword, uncompressed, method 0
	b = append(b, 0, 0)                      // no language tag, no translated keyword
	return append(b, packet...)
}
