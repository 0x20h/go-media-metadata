package mediameta

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/0x20h/go-media-metadata/internal/bmff"
	"github.com/andybalholm/brotli"
)

// ErrNotJXL is returned for a file that isn't a JPEG XL image.
var ErrNotJXL = errors.New("not a JPEG XL file")

// The JPEG XL container's signature box (ISO/IEC 18181-2), and a bare
// codestream's signature (ISO/IEC 18181-1).
var (
	jxlSignature  = []byte{0, 0, 0, 0x0C, 'J', 'X', 'L', ' ', 0x0D, 0x0A, 0x87, 0x0A}
	jxlCodestream = []byte{0xFF, 0x0A}
)

// maxJXLMetadata caps a metadata box (or its Brotli-decompressed content)
// read into memory.
const maxJXLMetadata = 16 << 20

func isJXL(r io.ReaderAt, size int64) bool {
	var b [12]byte
	if size < 2 {
		return false
	}
	n := min(size, 12)
	if _, err := r.ReadAt(b[:n], 0); err != nil && err != io.EOF {
		return false
	}
	return bytes.Equal(b[:n], jxlSignature[:n]) && n == 12 || bytes.HasPrefix(b[:n], jxlCodestream)
}

// jxlFile is a JPEG XL file: its boxes, or none for a bare codestream.
type jxlFile struct {
	boxes []bmff.Box
}

func parseJXL(r io.ReaderAt, size int64) (*jxlFile, error) {
	if !isJXL(r, size) {
		return nil, ErrNotJXL
	}
	var head [2]byte
	if _, err := r.ReadAt(head[:], 0); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	if bytes.Equal(head[:], jxlCodestream) {
		return &jxlFile{}, nil
	}
	boxes, err := bmff.ReadBoxes(r, 0, size)
	if err != nil {
		return nil, err
	}
	return &jxlFile{boxes: boxes}, nil
}

func (f *jxlFile) container() bool { return f.boxes != nil }

// jxlBoxType is the box type of a kind of metadata.
func jxlBoxType(kind metadataKind) bmff.Type {
	if kind == kindXMP {
		return bmff.T("xml ")
	}
	return bmff.T("Exif")
}

// metadataBoxes are the indexes of the boxes holding kind: a box of its
// type, or a Brotli-compressed one (brob) whose content is of its type.
func (f *jxlFile) metadataBoxes(r io.ReaderAt, kind metadataKind) ([]int, error) {
	t := jxlBoxType(kind)
	var out []int
	for i, b := range f.boxes {
		switch b.Type {
		case t:
			out = append(out, i)
		case bmff.T("brob"):
			var inner [4]byte
			if b.BodyLen() < 4 {
				continue
			}
			if _, err := r.ReadAt(inner[:], b.Body()); err != nil {
				return nil, fmt.Errorf("%w: reading a brob box: %v", ErrMalformed, err)
			}
			if bmff.Type(inner) == t {
				out = append(out, i)
			}
		}
	}
	return out, nil
}

// content is a metadata box's content: its payload, decompressed for a
// brob box.
func (f *jxlFile) content(r io.ReaderAt, i int) ([]byte, error) {
	b := f.boxes[i]
	if b.BodyLen() > maxJXLMetadata {
		return nil, fmt.Errorf("%w: a metadata box of %d bytes", ErrUnsupported, b.BodyLen())
	}
	p := make([]byte, b.BodyLen())
	if _, err := r.ReadAt(p, b.Body()); err != nil {
		return nil, fmt.Errorf("%w: reading a %v box: %v", ErrMalformed, b.Type, err)
	}
	if b.Type != bmff.T("brob") {
		return p, nil
	}
	out, err := io.ReadAll(io.LimitReader(brotli.NewReader(bytes.NewReader(p[4:])), maxJXLMetadata+1))
	if err != nil {
		return nil, fmt.Errorf("%w: a brob box that doesn't decompress: %v", ErrMalformed, err)
	}
	if len(out) > maxJXLMetadata {
		return nil, fmt.Errorf("%w: a brob box past %d bytes", ErrUnsupported, maxJXLMetadata)
	}
	return out, nil
}

// readJXLBox is the content of the first box holding kind, nil without
// one. An Exif box's content starts with the offset of its TIFF header.
func readJXLBox(r io.ReaderAt, size int64, kind metadataKind) ([]byte, error) {
	f, err := parseJXL(r, size)
	if err != nil {
		return nil, err
	}
	idx, err := f.metadataBoxes(r, kind)
	if err != nil || len(idx) == 0 {
		return nil, err
	}
	return f.content(r, idx[0])
}

// jxlTIFF is an Exif box's TIFF data: after a 4-byte offset to the TIFF
// header, from that header on.
func jxlTIFF(b []byte) ([]byte, bool) {
	if len(b) < 4 {
		return nil, false
	}
	off := uint64(binary.BigEndian.Uint32(b))
	if off > uint64(len(b)-4) {
		return nil, false
	}
	return b[4+off:], true
}

func readJXLExif(r io.ReaderAt, size int64) (Exif, error) {
	b, err := readJXLBox(r, size, kindExif)
	if err != nil || b == nil {
		return Exif{}, err
	}
	tiff, ok := jxlTIFF(b)
	if !ok {
		return Exif{}, fmt.Errorf("%w: an Exif box with a bad TIFF offset", ErrMalformed)
	}
	x := parseExif(tiff)
	// JPEG XL readers show the codestream's orientation, not Exif's.
	if o, err := readJXLOrientation(r, size); err == nil {
		x.Orientation = o
	}
	return x, nil
}

func readJXLXMP(r io.ReaderAt, size int64) ([]byte, error) {
	return readJXLBox(r, size, kindXMP)
}

// codestreamAt is where the codestream starts: at 0 for a bare one, else
// in the jxlc box, or after the 4-byte index of the first jxlp box.
func (f *jxlFile) codestreamAt() (int64, int64, bool) {
	if !f.container() {
		return 0, -1, true
	}
	for _, b := range f.boxes {
		switch b.Type {
		case bmff.T("jxlc"):
			return b.Body(), b.BodyLen(), true
		case bmff.T("jxlp"):
			if b.BodyLen() > 4 {
				return b.Body() + 4, b.BodyLen() - 4, true
			}
		}
	}
	return 0, 0, false
}

// readJXLOrientation reads the orientation from the codestream's image
// header, which is what JPEG XL decoders apply: 1 for default metadata.
func readJXLOrientation(r io.ReaderAt, size int64) (Orientation, error) {
	f, err := parseJXL(r, size)
	if err != nil {
		return 0, err
	}
	at, n, ok := f.codestreamAt()
	if !ok {
		return 0, fmt.Errorf("%w: no codestream", ErrMalformed)
	}
	if n < 0 {
		n = size - at
	}
	head := make([]byte, min(n, 32))
	if _, err := r.ReadAt(head, at); err != nil && err != io.EOF {
		return 0, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	if !bytes.HasPrefix(head, jxlCodestream) {
		return 0, fmt.Errorf("%w: no codestream signature", ErrMalformed)
	}
	br := &bitReader{b: head[2:]}
	// SizeHeader.
	if br.bit() == 1 {
		br.bits(5) // ysize_div8_minus1
		if br.bits(3) == 0 {
			br.bits(5) // xsize_div8_minus1
		}
	} else {
		br.u32(9, 13, 18, 30)
		if br.bits(3) == 0 {
			br.u32(9, 13, 18, 30)
		}
	}
	// ImageMetadata: all_default, then extra_fields and the orientation.
	if br.bit() == 1 {
		return 1, br.err()
	}
	if br.bit() == 0 {
		return 1, br.err()
	}
	o := Orientation(br.bits(3) + 1)
	return o, br.err()
}

// bitReader reads a JPEG XL codestream's bits, least significant first.
type bitReader struct {
	b   []byte
	pos int
	bad bool
}

func (r *bitReader) bit() uint32 {
	if r.pos >= 8*len(r.b) {
		r.bad = true
		return 0
	}
	v := uint32(r.b[r.pos/8]>>(r.pos%8)) & 1
	r.pos++
	return v
}

func (r *bitReader) bits(n int) uint32 {
	var v uint32
	for i := range n {
		v |= r.bit() << i
	}
	return v
}

// u32 reads a U32 whose four distributions are 1 + u(n) bits.
func (r *bitReader) u32(n0, n1, n2, n3 int) uint32 {
	return 1 + r.bits([]int{n0, n1, n2, n3}[r.bits(2)])
}

func (r *bitReader) err() error {
	if r.bad {
		return fmt.Errorf("%w: a codestream header cut short", ErrMalformed)
	}
	return nil
}

// planJXLBox sets a JPEG XL file's Exif (TIFF data) or XMP (a packet) to
// what edit makes of the old one.
//
// A plain box whose payload the new data fits is written in place,
// padded (zeros after the TIFF data, whitespace after an XMP packet).
// Otherwise the box, or a Brotli-compressed (brob) one, is replaced by a
// plain box, and other boxes of the kind are removed. A file without one
// gets a box before its codestream; a bare codestream gets the container
// around it. The codestream is copied byte for byte.
//
// A file with JPEG reconstruction data (jbrd) records the original JPEG's
// segment sizes, so an edit that changes a metadata box's size is refused
// (ErrUnsupported).
func planJXLBox(r io.ReaderAt, size int64, kind metadataKind, edit func(old []byte) ([]byte, error)) (*Plan, error) {
	f, err := parseJXL(r, size)
	if err != nil {
		return nil, err
	}
	var idx []int
	var old []byte
	if f.container() {
		if idx, err = f.metadataBoxes(r, kind); err != nil {
			return nil, err
		}
		if len(idx) > 0 {
			c, err := f.content(r, idx[0])
			if err != nil {
				return nil, err
			}
			if kind == kindExif {
				if old, _ = jxlTIFF(c); old == nil {
					return nil, fmt.Errorf("%w: an Exif box with a bad TIFF offset", ErrMalformed)
				}
			} else {
				old = c
			}
		}
	}
	data, err := edit(old)
	if err != nil {
		return nil, err
	}
	payload := data
	pad := byte(' ')
	if kind == kindExif {
		payload = append(make([]byte, 4, 4+len(data)), data...)
		pad = 0
	}
	t := jxlBoxType(kind)
	box := func(p []byte) []byte {
		return append(bmff.AppendHeader(nil, t, len(p), false), p...)
	}
	e := &spliceEdit{}
	switch {
	case !f.container():
		// Wrap the bare codestream: signature, ftyp, the box, and a jxlc
		// box whose payload is the whole file.
		head := append([]byte(nil), jxlSignature...)
		head = append(head, bmff.AppendHeader(nil, bmff.T("ftyp"), 12, false)...)
		head = append(head, "jxl \x00\x00\x00\x00jxl "...)
		head = append(head, box(payload)...)
		head = bmff.AppendHeader(head, bmff.T("jxlc"), int(size), false)
		e.add(splice{at: 0, data: head})
	case len(idx) > 0:
		b := f.boxes[idx[0]]
		if b.Type == t && b.HeaderLen == 8 {
			if padded, ok := padTo(payload, int(b.BodyLen()), pad); ok {
				e.add(splice{at: b.Body(), del: b.BodyLen(), data: padded})
				break
			}
		}
		e.add(splice{at: b.Offset, del: b.Size, data: box(payload)})
		for _, i := range idx[1:] {
			d := f.boxes[i]
			e.add(splice{at: d.Offset, del: d.Size})
		}
	default:
		at := f.boxes[len(f.boxes)-1].End()
		for _, b := range f.boxes {
			if b.Type == bmff.T("jxlc") || b.Type == bmff.T("jxlp") {
				at = b.Offset
				break
			}
		}
		e.add(splice{at: at, data: box(payload)})
	}
	if err := e.sorted(); err != nil {
		return nil, err
	}
	if _, ok := bmff.Find(f.boxes, bmff.T("jbrd")); ok && e.delta() != 0 {
		return nil, fmt.Errorf("%w: JPEG reconstruction data records the metadata's size", ErrUnsupported)
	}
	return e.plan(r, size), nil
}
