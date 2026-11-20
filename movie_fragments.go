package mediameta

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"

	"github.com/0x20h/go-media-metadata/internal/bmff"
)

// A fragmented movie (ISO/IEC 14496-12 movie fragments: moov with mvex,
// then moof and mdat pairs, optionally sidx and mfra) is rewritten as any
// other movie: moov changes, the boxes after it move. What points at
// absolute positions moves with them:
//   - a track fragment header's (tfhd) base-data-offset, when present;
//   - a track fragment random access box's (tfra, in mfra) moof offsets.
//
// What is relative stays: a track run's (trun) data offset, from its base
// (the moof itself, with default-base-is-moof or without a base offset);
// a segment index's (sidx) references, from the end of the sidx, as long
// as what they span moves as one (its first offset is patched when the
// sidx and what it indexes move apart).

// isFragmentBox is whether a top-level box belongs to a fragmented
// movie's fragments or their indexes.
func isFragmentBox(t bmff.Type) bool {
	switch t {
	case bmff.T("moof"), bmff.T("mfra"), bmff.T("sidx"), bmff.T("ssix"), bmff.T("emsg"), bmff.T("prft"), bmff.T("styp"):
		return true
	}
	return false
}

// maxFragmentBox caps a moof or mfra box read whole.
const maxFragmentBox = 16 << 20

// checkFragmentLayout refuses a fragmented layout this rewrite doesn't
// handle: fragments or indexes before moov.
func checkFragmentLayout(top []bmff.Box, mi int) error {
	for i, b := range top[:mi] {
		if isFragmentBox(b.Type) {
			return fmt.Errorf("%w: a fragmented movie's %v before its movie box (at %d)", ErrUnsupported, b.Type, top[i].Offset)
		}
	}
	return nil
}

// fragmentSplices are the patches of a fragmented movie's absolute
// offsets, and of its segment indexes' first offsets, for boxes moving to
// newAt (by top index; a box not there is left out).
func fragmentSplices(src io.ReaderAt, top []bmff.Box, newAt map[int]int64) ([]splice, error) {
	var out []splice
	for i, b := range top {
		if _, kept := newAt[i]; !kept {
			continue
		}
		var err error
		switch b.Type {
		case bmff.T("moof"):
			out, err = moofSplices(src, top, newAt, b, out)
		case bmff.T("mfra"):
			out, err = mfraSplices(src, top, newAt, b, out)
		case bmff.T("sidx"):
			out, err = sidxSplices(src, top, newAt, i, out)
		}
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func readFragmentBox(src io.ReaderAt, b bmff.Box) ([]byte, error) {
	if b.Size > maxFragmentBox {
		return nil, fmt.Errorf("%w: a %v box of %d bytes", ErrUnsupported, b.Type, b.Size)
	}
	buf := make([]byte, b.Size)
	if _, err := src.ReadAt(buf, b.Offset); err != nil {
		return nil, fmt.Errorf("%w: reading %v: %v", ErrMalformed, b.Type, err)
	}
	return buf, nil
}

// patch64 is a splice of an 8-byte offset at the source's position at,
// mapped; none where it doesn't move.
func patchOffset(top []bmff.Box, newAt map[int]int64, at int64, old uint64, width int, what string, out []splice) ([]splice, error) {
	if old > math.MaxInt64 {
		return nil, fmt.Errorf("%w: a %s out of range", ErrMalformed, what)
	}
	nv, ok := mapOffset(top, newAt, int64(old))
	if !ok {
		return nil, fmt.Errorf("%w: a %s (%d) outside the boxes kept", ErrMalformed, what, old)
	}
	if uint64(nv) == old {
		return out, nil
	}
	b := make([]byte, width)
	if width == 4 {
		if nv > math.MaxUint32 {
			return nil, fmt.Errorf("%w: a 32-bit %s the move would overflow", ErrUnsupported, what)
		}
		binary.BigEndian.PutUint32(b, uint32(nv))
	} else {
		binary.BigEndian.PutUint64(b, uint64(nv))
	}
	return append(out, splice{at: at, del: int64(width), data: b}), nil
}

// moofSplices patches each track fragment header's base-data-offset.
func moofSplices(src io.ReaderAt, top []bmff.Box, newAt map[int]int64, moof bmff.Box, out []splice) ([]splice, error) {
	buf, err := readFragmentBox(src, moof)
	if err != nil {
		return nil, err
	}
	kids, err := bmff.ParseBoxes(buf, moof.HeaderLen, int64(len(buf)))
	if err != nil {
		return nil, fmt.Errorf("%w: a movie fragment box that doesn't parse", ErrMalformed)
	}
	for _, traf := range kids {
		if traf.Type != bmff.T("traf") {
			continue
		}
		tk, err := bmff.ParseBoxes(buf, traf.Body(), traf.End())
		if err != nil {
			return nil, fmt.Errorf("%w: a track fragment box that doesn't parse", ErrMalformed)
		}
		for _, k := range tk {
			switch k.Type {
			case bmff.T("tfhd"):
				p := buf[k.Body():k.End()]
				if len(p) < 8 {
					return nil, fmt.Errorf("%w: a track fragment header of %d bytes", ErrMalformed, len(p))
				}
				if binary.BigEndian.Uint32(p)&0x000001 == 0 {
					continue // no base-data-offset: relative to the moof
				}
				if len(p) < 16 {
					return nil, fmt.Errorf("%w: a track fragment header without its base offset", ErrMalformed)
				}
				at := moof.Offset + k.Body() + 8
				if out, err = patchOffset(top, newAt, at, binary.BigEndian.Uint64(p[8:]), 8, "track fragment base offset", out); err != nil {
					return nil, err
				}
			case bmff.T("saio"):
				// Relative to the base offset, as trun's data offsets
				// are: they move with it.
			}
		}
	}
	return out, nil
}

// mfraSplices patches each tfra entry's moof offset.
func mfraSplices(src io.ReaderAt, top []bmff.Box, newAt map[int]int64, mfra bmff.Box, out []splice) ([]splice, error) {
	buf, err := readFragmentBox(src, mfra)
	if err != nil {
		return nil, err
	}
	kids, err := bmff.ParseBoxes(buf, mfra.HeaderLen, int64(len(buf)))
	if err != nil {
		return nil, fmt.Errorf("%w: a movie fragment random access box that doesn't parse", ErrMalformed)
	}
	for _, k := range kids {
		if k.Type != bmff.T("tfra") {
			continue
		}
		p := buf[k.Body():k.End()]
		if len(p) < 16 {
			return nil, fmt.Errorf("%w: a track fragment random access box of %d bytes", ErrMalformed, len(p))
		}
		v1 := p[0] == 1
		lens := binary.BigEndian.Uint32(p[8:])
		n := int64(binary.BigEndian.Uint32(p[12:]))
		w := 4
		if v1 {
			w = 8
		}
		// time, moof_offset, then traf, trun and sample numbers of 1 to
		// 4 bytes each.
		entry := int64(2*w) + int64((lens>>4)&3+1) + int64((lens>>2)&3+1) + int64(lens&3+1)
		if n*entry > int64(len(p)-16) {
			return nil, fmt.Errorf("%w: a tfra longer than its box", ErrMalformed)
		}
		for j := int64(0); j < n; j++ {
			off := 16 + j*entry + int64(w)
			var old uint64
			if v1 {
				old = binary.BigEndian.Uint64(p[off:])
			} else {
				old = uint64(binary.BigEndian.Uint32(p[off:]))
			}
			if out, err = patchOffset(top, newAt, mfra.Offset+k.Body()+off, old, w, "random access moof offset", out); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

// sidxSplices checks that what a segment index (top[i]) spans moves as
// one, and patches its first offset where the sidx and that span move
// apart.
func sidxSplices(src io.ReaderAt, top []bmff.Box, newAt map[int]int64, i int, out []splice) ([]splice, error) {
	b := top[i]
	if b.Size > maxFragmentBox {
		return nil, fmt.Errorf("%w: a segment index of %d bytes", ErrUnsupported, b.Size)
	}
	buf := make([]byte, b.Size)
	if _, err := src.ReadAt(buf, b.Offset); err != nil {
		return nil, fmt.Errorf("%w: reading a segment index: %v", ErrMalformed, err)
	}
	p := buf[b.HeaderLen:]
	if len(p) < 12 {
		return nil, fmt.Errorf("%w: a segment index of %d bytes", ErrMalformed, len(p))
	}
	v1 := p[0] == 1
	at := 12 // version, flags, reference_ID, timescale
	w := 4
	if v1 {
		w = 8
	}
	if len(p) < at+2*w+4 {
		return nil, fmt.Errorf("%w: a segment index cut short", ErrMalformed)
	}
	firstAt := at + w // after earliest_presentation_time
	var first uint64
	if v1 {
		first = binary.BigEndian.Uint64(p[firstAt:])
	} else {
		first = uint64(binary.BigEndian.Uint32(p[firstAt:]))
	}
	count := int(binary.BigEndian.Uint16(p[firstAt+w+2:]))
	refs := p[firstAt+w+4:]
	if len(refs) < 12*count {
		return nil, fmt.Errorf("%w: a segment index longer than its box", ErrMalformed)
	}
	var span uint64
	for j := 0; j < count; j++ {
		span += uint64(binary.BigEndian.Uint32(refs[12*j:]) & 0x7FFFFFFF)
	}
	if first > math.MaxInt64/2 || span > math.MaxInt64/2 {
		return nil, fmt.Errorf("%w: a segment index out of range", ErrMalformed)
	}
	start := b.End() + int64(first)
	end := start + int64(span)
	if count == 0 || span == 0 {
		return out, nil
	}
	// Every box the span touches is kept and moves by the same amount.
	shift, set := int64(0), false
	for j, t := range top {
		if t.End() <= start || t.Offset >= end {
			continue
		}
		na, kept := newAt[j]
		if !kept || t.Offset < start && j != i {
			return nil, fmt.Errorf("%w: a segment index over boxes the rewrite leaves out or splits", ErrUnsupported)
		}
		if set && na-t.Offset != shift {
			return nil, fmt.Errorf("%w: a segment index over boxes that move apart", ErrUnsupported)
		}
		shift, set = na-t.Offset, true
	}
	if !set {
		return nil, fmt.Errorf("%w: a segment index past the boxes kept", ErrMalformed)
	}
	newFirst := start + shift - (newAt[i] + b.Size)
	if newFirst < 0 {
		return nil, fmt.Errorf("%w: a segment index the move would put after what it indexes", ErrUnsupported)
	}
	if uint64(newFirst) == first {
		return out, nil
	}
	nb := make([]byte, w)
	if v1 {
		binary.BigEndian.PutUint64(nb, uint64(newFirst))
	} else {
		if newFirst > math.MaxUint32 {
			return nil, fmt.Errorf("%w: a 32-bit segment index offset the move would overflow", ErrUnsupported)
		}
		binary.BigEndian.PutUint32(nb, uint32(newFirst))
	}
	return append(out, splice{at: b.Offset + b.HeaderLen + int64(firstAt), del: int64(w), data: nb}), nil
}
