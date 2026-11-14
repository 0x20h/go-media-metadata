package mediameta

import (
	"encoding/binary"
	"fmt"
	"io"
)

// VP8X flags (WebP container specification).
const (
	vp8xICC   = 0x20
	vp8xAlpha = 0x10
	vp8xEXIF  = 0x08
	vp8xXMP   = 0x04
	vp8xAnim  = 0x02
)

// maxWebPChunks caps the chunks walked in one file.
const maxWebPChunks = 1 << 16

// webpChunk is one RIFF chunk: at is where its FourCC is, n the length of
// its data (a pad byte follows an odd one).
type webpChunk struct {
	fourcc [4]byte
	at     int64
	n      int64
}

func (c webpChunk) data() int64 { return c.at + 8 }
func (c webpChunk) end() int64  { return c.at + 8 + c.n + c.n&1 }

func isWebP(r io.ReaderAt, size int64) bool {
	var b [12]byte
	if size < 12 {
		return false
	}
	if _, err := r.ReadAt(b[:], 0); err != nil {
		return false
	}
	return string(b[:4]) == "RIFF" && string(b[8:]) == "WEBP"
}

// webpFile is a WebP's chunks; riffEnd is where the RIFF data ends (data
// past it isn't the WebP's and is copied as it is).
type webpFile struct {
	chunks  []webpChunk
	riffEnd int64
}

func parseWebP(r io.ReaderAt, size int64) (*webpFile, error) {
	if !isWebP(r, size) {
		return nil, errNotWebP
	}
	var h [4]byte
	if _, err := r.ReadAt(h[:], 4); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	riffEnd := 8 + int64(binary.LittleEndian.Uint32(h[:]))
	if riffEnd > size || riffEnd < 12 {
		return nil, fmt.Errorf("%w: a RIFF size past the file", ErrMalformed)
	}
	f := &webpFile{riffEnd: riffEnd}
	for at := int64(12); at+8 <= riffEnd; {
		if len(f.chunks) >= maxWebPChunks {
			return nil, fmt.Errorf("%w: more than %d WebP chunks", ErrMalformed, maxWebPChunks)
		}
		var b [8]byte
		if _, err := r.ReadAt(b[:], at); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrMalformed, err)
		}
		c := webpChunk{at: at, n: int64(binary.LittleEndian.Uint32(b[4:]))}
		copy(c.fourcc[:], b[:4])
		if c.data()+c.n > riffEnd {
			return nil, fmt.Errorf("%w: WebP chunk %q of %d bytes", ErrMalformed, c.fourcc[:], c.n)
		}
		f.chunks = append(f.chunks, c)
		at = min(c.end(), riffEnd)
	}
	if len(f.chunks) == 0 {
		return nil, fmt.Errorf("%w: a WebP without chunks", ErrMalformed)
	}
	switch string(f.chunks[0].fourcc[:]) {
	case "VP8 ", "VP8L", "VP8X":
	default:
		return nil, fmt.Errorf("%w: a WebP starting with %q", ErrMalformed, f.chunks[0].fourcc[:])
	}
	return f, nil
}

func (f *webpFile) find(fourcc string) []int {
	var out []int
	for i, c := range f.chunks {
		if string(c.fourcc[:]) == fourcc {
			out = append(out, i)
		}
	}
	return out
}

func readWebPChunk(r io.ReaderAt, size int64, fourcc string) ([]byte, error) {
	f, err := parseWebP(r, size)
	if err != nil {
		return nil, err
	}
	idx := f.find(fourcc)
	if len(idx) == 0 {
		return nil, nil
	}
	c := f.chunks[idx[0]]
	return readChunk(r, c.data(), c.n)
}

func readWebPExif(r io.ReaderAt, size int64) (Exif, error) {
	b, err := readWebPChunk(r, size, "EXIF")
	if err != nil || b == nil {
		return Exif{}, err
	}
	_, tiff := tiffPayload(b)
	return parseExif(tiff), nil
}

func readWebPXMP(r io.ReaderAt, size int64) ([]byte, error) {
	return readWebPChunk(r, size, "XMP ")
}

// canvas is the image size of a simple (VP8 or VP8L) WebP from its
// bitstream's header, and whether a VP8L image says it uses alpha.
func (f *webpFile) canvas(r io.ReaderAt) (w, h int, alpha bool, err error) {
	c := f.chunks[0]
	var b [10]byte
	if c.n < 10 {
		return 0, 0, false, fmt.Errorf("%w: a short image chunk", ErrMalformed)
	}
	if _, err := r.ReadAt(b[:], c.data()); err != nil {
		return 0, 0, false, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	switch string(c.fourcc[:]) {
	case "VP8 ":
		// A key frame: 3 bytes of frame tag, the start code, then 14-bit
		// width and height (RFC 6386 9.1).
		if b[3] != 0x9d || b[4] != 0x01 || b[5] != 0x2a {
			return 0, 0, false, fmt.Errorf("%w: a VP8 chunk without a key frame", ErrUnsupported)
		}
		w = int(binary.LittleEndian.Uint16(b[6:]) & 0x3fff)
		h = int(binary.LittleEndian.Uint16(b[8:]) & 0x3fff)
	case "VP8L":
		// The signature, then 14 bits of width-1, 14 of height-1 and the
		// alpha hint.
		if b[0] != 0x2f {
			return 0, 0, false, fmt.Errorf("%w: a VP8L chunk without its signature", ErrMalformed)
		}
		v := binary.LittleEndian.Uint32(b[1:])
		w, h, alpha = int(v&0x3fff)+1, int(v>>14&0x3fff)+1, v>>28&1 == 1
	default:
		return 0, 0, false, fmt.Errorf("%w: a WebP starting with %q", ErrUnsupported, c.fourcc[:])
	}
	if w == 0 || h == 0 {
		return 0, 0, false, fmt.Errorf("%w: a WebP of %dx%d", ErrMalformed, w, h)
	}
	return w, h, alpha, nil
}

// planWebPChunk sets a WebP's EXIF or XMP chunk: edit makes the new
// payload from the old one (nil without one). A payload no longer than
// the old one's fills its chunk, padded (zeros after TIFF data, spaces
// after XMP): in place, the old value overwritten. A longer one replaces
// the chunk; a new chunk goes at the end of the RIFF data, after the
// image (the container's order). A simple WebP gets the VP8X header that
// metadata needs; VP8X's flag for the chunk is set, and the RIFF size
// follows. Further chunks of the kind are removed.
func planWebPChunk(r io.ReaderAt, size int64, kind metadataKind, edit func(old []byte) ([]byte, error)) (*Plan, error) {
	f, err := parseWebP(r, size)
	if err != nil {
		return nil, err
	}
	fourcc, flag, pad := "EXIF", byte(vp8xEXIF), byte(0)
	if kind == kindXMP {
		fourcc, flag, pad = "XMP ", vp8xXMP, ' '
	}
	idx := f.find(fourcc)
	var old []byte
	if len(idx) > 0 {
		c := f.chunks[idx[0]]
		if old, err = readChunk(r, c.data(), c.n); err != nil {
			return nil, err
		}
	}
	payload, err := edit(old)
	if err != nil {
		return nil, err
	}
	if uint64(len(payload)) > 1<<32-10 {
		return nil, fmt.Errorf("%w: a chunk of %d bytes", ErrUnsupported, len(payload))
	}
	chunk := func(data []byte) []byte {
		b := append([]byte(fourcc), binary.LittleEndian.AppendUint32(nil, uint32(len(data)))...)
		b = append(b, data...)
		if len(data)%2 == 1 {
			b = append(b, 0)
		}
		return b
	}
	e := &spliceEdit{}
	if len(idx) > 0 {
		c := f.chunks[idx[0]]
		data := payload
		if padded, ok := padTo(payload, int(c.n), pad); ok {
			data = padded
		}
		e.add(splice{at: c.at, del: c.end() - c.at, data: chunk(data)})
		for _, i := range idx[1:] {
			d := f.chunks[i]
			e.add(splice{at: d.at, del: d.end() - d.at})
		}
	} else {
		// After the last chunk: bytes after it in the RIFF data, too few
		// for a chunk, stay at the end, where readers pass over them. A
		// last chunk missing its pad byte would run into the new one.
		last := f.chunks[len(f.chunks)-1]
		if last.end() > f.riffEnd {
			return nil, fmt.Errorf("%w: a last chunk without its pad byte", ErrMalformed)
		}
		e.add(splice{at: last.end(), data: chunk(payload)})
	}
	// VP8X: its flag set, or a VP8X made for a simple file.
	if first := f.chunks[0]; string(first.fourcc[:]) == "VP8X" {
		if first.n < 10 {
			return nil, fmt.Errorf("%w: a VP8X of %d bytes", ErrMalformed, first.n)
		}
		var fl [1]byte
		if _, err := r.ReadAt(fl[:], first.data()); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrMalformed, err)
		}
		if fl[0]&flag == 0 {
			e.add(splice{at: first.data(), del: 1, data: []byte{fl[0] | flag}})
		}
	} else {
		w, h, alpha, err := f.canvas(r)
		if err != nil {
			return nil, err
		}
		x := []byte{'V', 'P', '8', 'X', 10, 0, 0, 0, flag, 0, 0, 0}
		if alpha {
			x[8] |= vp8xAlpha
		}
		x = append(x, byte(w-1), byte((w-1)>>8), byte((w-1)>>16), byte(h-1), byte((h-1)>>8), byte((h-1)>>16))
		e.add(splice{at: 12, data: x})
	}
	if err := e.sorted(); err != nil {
		return nil, err
	}
	// The RIFF size, for what the other splices add or take away (all of
	// them lie within the RIFF data).
	riff := f.riffEnd - 8 + e.delta()
	if riff > 1<<32-1 {
		return nil, fmt.Errorf("%w: a WebP past 4 GB", ErrUnsupported)
	}
	if riff != f.riffEnd-8 {
		e.add(splice{at: 4, del: 4, data: binary.LittleEndian.AppendUint32(nil, uint32(riff))})
		if err := e.sorted(); err != nil {
			return nil, err
		}
	}
	return e.plan(r, size), nil
}
