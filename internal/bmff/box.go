package bmff

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// ErrMalformed is returned for input that doesn't follow the box
// structure: a length past its container, a truncated header, too many
// boxes or too deep a nesting.
var ErrMalformed = errors.New("mediameta: malformed file")

// Limits on untrusted input.
const (
	// MaxBoxes is the most sibling boxes read in one container.
	MaxBoxes = 4096
	// MaxDepth is the deepest nesting followed.
	MaxDepth = 8
)

// Type is a four-character box type.
type Type [4]byte

// String returns the type as text, with non-printable bytes escaped.
func (t Type) String() string {
	return fmt.Sprintf("%q", string(t[:]))
}

// T makes a Type from a four-character string.
func T(s string) Type {
	var t Type
	copy(t[:], s)
	return t
}

// Box is a box's position: where its header starts, how long the header
// is, and its total size, header included. Offsets are relative to
// whatever the box was read from.
type Box struct {
	Type      Type
	Offset    int64
	HeaderLen int64
	Size      int64
}

// Body is where the box's payload starts.
func (b Box) Body() int64 { return b.Offset + b.HeaderLen }

// End is the offset just past the box.
func (b Box) End() int64 { return b.Offset + b.Size }

// BodyLen is the payload's length.
func (b Box) BodyLen() int64 { return b.Size - b.HeaderLen }

// Large reports whether the box was written with a 64-bit size.
func (b Box) Large() bool { return b.HeaderLen >= 16 }

// ReadBoxes reads the sibling boxes in [start, end) of r. A box of size 0
// runs to end. Box types "uuid" have their 16-byte extended type counted
// in the header.
func ReadBoxes(r io.ReaderAt, start, end int64) ([]Box, error) {
	var out []Box
	for off := start; off < end; {
		if len(out) >= MaxBoxes {
			return nil, fmt.Errorf("%w: more than %d boxes", ErrMalformed, MaxBoxes)
		}
		if end-off < 8 {
			return nil, fmt.Errorf("%w: %d stray bytes at %d", ErrMalformed, end-off, off)
		}
		var h [16]byte
		if _, err := r.ReadAt(h[:8], off); err != nil {
			return nil, fmt.Errorf("%w: header at %d: %v", ErrMalformed, off, err)
		}
		b := Box{Offset: off, HeaderLen: 8}
		copy(b.Type[:], h[4:8])
		size := int64(binary.BigEndian.Uint32(h[:4]))
		switch size {
		case 0:
			size = end - off
		case 1:
			if end-off < 16 {
				return nil, fmt.Errorf("%w: truncated large header at %d", ErrMalformed, off)
			}
			if _, err := r.ReadAt(h[8:16], off+8); err != nil {
				return nil, fmt.Errorf("%w: header at %d: %v", ErrMalformed, off, err)
			}
			large := binary.BigEndian.Uint64(h[8:16])
			if large > uint64(end-off) {
				return nil, fmt.Errorf("%w: box %v at %d runs past its container", ErrMalformed, b.Type, off)
			}
			size = int64(large)
			b.HeaderLen = 16
		}
		if b.Type == T("uuid") {
			b.HeaderLen += 16
		}
		if size < b.HeaderLen || size > end-off {
			return nil, fmt.Errorf("%w: box %v at %d has size %d", ErrMalformed, b.Type, off, size)
		}
		b.Size = size
		out = append(out, b)
		off += size
	}
	return out, nil
}

// Find returns the first box of type t.
func Find(boxes []Box, t Type) (Box, bool) {
	for _, b := range boxes {
		if b.Type == t {
			return b, true
		}
	}
	return Box{}, false
}

// AppendHeader appends a box header for a payload of bodyLen bytes: 32-bit
// unless large is set or the size doesn't fit.
func AppendHeader(dst []byte, t Type, bodyLen int, large bool) []byte {
	if !large && bodyLen+8 <= 0xFFFFFFFF {
		dst = binary.BigEndian.AppendUint32(dst, uint32(bodyLen+8))
		return append(dst, t[:]...)
	}
	dst = binary.BigEndian.AppendUint32(dst, 1)
	dst = append(dst, t[:]...)
	return binary.BigEndian.AppendUint64(dst, uint64(bodyLen+16))
}

// reader reads big-endian fields from a byte slice, recording the first
// overrun instead of panicking.
type reader struct {
	b   []byte
	off int
	err error
}

func (r *reader) need(n int) bool {
	if r.err != nil {
		return false
	}
	if n < 0 || len(r.b)-r.off < n {
		r.err = fmt.Errorf("%w: field past the end of its box", ErrMalformed)
		return false
	}
	return true
}

func (r *reader) u8() uint8 {
	if !r.need(1) {
		return 0
	}
	v := r.b[r.off]
	r.off++
	return v
}

func (r *reader) u16() uint16 {
	if !r.need(2) {
		return 0
	}
	v := binary.BigEndian.Uint16(r.b[r.off:])
	r.off += 2
	return v
}

func (r *reader) u32() uint32 {
	if !r.need(4) {
		return 0
	}
	v := binary.BigEndian.Uint32(r.b[r.off:])
	r.off += 4
	return v
}

// uint reads an unsigned field of size bytes (0, 4 or 8).
func (r *reader) uint(size uint8) uint64 {
	switch size {
	case 0:
		return 0
	case 4:
		return uint64(r.u32())
	case 8:
		if !r.need(8) {
			return 0
		}
		v := binary.BigEndian.Uint64(r.b[r.off:])
		r.off += 8
		return v
	}
	if r.err == nil {
		r.err = fmt.Errorf("%w: field size %d", ErrMalformed, size)
	}
	return 0
}

func (r *reader) left() int { return len(r.b) - r.off }

// appendUint appends v as a size-byte field (0, 4 or 8); false when it
// doesn't fit.
func appendUint(dst []byte, size uint8, v uint64) ([]byte, bool) {
	switch size {
	case 0:
		return dst, v == 0
	case 4:
		if v > 0xFFFFFFFF {
			return dst, false
		}
		return binary.BigEndian.AppendUint32(dst, uint32(v)), true
	case 8:
		return binary.BigEndian.AppendUint64(dst, v), true
	}
	return dst, false
}
