package mediameta

import (
	"encoding/binary"
	"fmt"

	"github.com/0x20h/go-media-metadata/internal/bmff"
)

// A track's media can be in other files (QuickTime reference movies, as
// older Final Cut, iMovie and QuickTime Pro exports write them): its data
// references (dref) list where, and each sample description names one
// of them by its data reference index. A chunk's samples are those of
// the sample description stsc gives the chunk, so its offset points into
// that entry's file. Only chunks in this file move with a rewrite; the
// offsets of the others are left as they are.

// drefSelfContained reads which of a track's data reference entries are
// this file (ISO/IEC 14496-12 8.7.2: the self-contained flag, 1): by
// entry, from index 1 (index 0 unused). nil for a track without a dref,
// or whose every entry is this file: the common case, read without
// allocating.
func drefSelfContained(minf *qtNode) ([]bool, error) {
	if minf == nil {
		return nil, nil
	}
	dinf := minf.child(bmff.T("dinf"))
	if dinf == nil {
		return nil, nil
	}
	b := leafPayload(dinf)
	// The dref box in dinf.
	at, end := -1, 0
	for k := 0; k+8 <= len(b); {
		n := int(binary.BigEndian.Uint32(b[k:]))
		if n < 8 || n > len(b)-k {
			return nil, fmt.Errorf("%w: a data information box that doesn't parse", ErrMalformed)
		}
		if string(b[k+4:k+8]) == "dref" {
			at, end = k+8, k+n
			break
		}
		k += n
	}
	if at < 0 || end-at < 8 {
		return nil, nil
	}
	count := int(binary.BigEndian.Uint32(b[at+4:]))
	// Its entries: a first pass for whether any is in another file, a
	// second to list them only then.
	var out []bool
	for pass := 0; pass < 2; pass++ {
		i, external := 0, false
		for k := at + 8; k < end; i++ {
			if k+12 > end {
				return nil, fmt.Errorf("%w: a data reference box that doesn't parse", ErrMalformed)
			}
			n := int(binary.BigEndian.Uint32(b[k:]))
			if n < 12 || n > end-k {
				return nil, fmt.Errorf("%w: a data reference entry of %d bytes", ErrMalformed, n)
			}
			self := binary.BigEndian.Uint32(b[k+8:])&1 != 0
			external = external || !self
			if out != nil && i+1 < len(out) {
				out[i+1] = self
			}
			k += n
		}
		if i != count {
			return nil, fmt.Errorf("%w: a data reference box of %d entries that says %d", ErrMalformed, i, count)
		}
		if !external || out != nil {
			break
		}
		out = make([]bool, count+1)
	}
	return out, nil
}

// chunkInFile reads, for each of a track's count chunks, whether its data
// is in this file. nil when every chunk's is (no data reference to
// another file), the common case. A track whose chunks can't be told
// apart for certain (stsc or stsd missing or out of step) is refused,
// never guessed.
func chunkInFile(minf, stbl *qtNode, count int) ([]bool, error) {
	self, err := drefSelfContained(minf)
	if err != nil || self == nil {
		return nil, err
	}
	external := false
	for _, s := range self[1:] {
		external = external || !s
	}
	if !external {
		return nil, nil
	}
	refused := func(why string) error {
		return fmt.Errorf("%w: a track with media in another file, %s", ErrUnsupported, why)
	}
	// Each sample description's data reference index (QuickTime and
	// ISO/IEC 14496-12 SampleEntry: 6 reserved bytes, then the index).
	stsd := stbl.child(bmff.T("stsd"))
	if stsd == nil {
		return nil, refused("without sample descriptions")
	}
	p := stsd.payload()
	if len(p) < 8 {
		return nil, refused("its sample descriptions cut short")
	}
	descs, err := bmff.ParseBoxes(p, 8, int64(len(p)))
	if err != nil || len(descs) != int(binary.BigEndian.Uint32(p[4:])) {
		return nil, refused("its sample descriptions don't parse")
	}
	ref := make([]int, len(descs)+1)
	for i, d := range descs {
		if d.BodyLen() < 8 {
			return nil, refused("a sample description cut short")
		}
		r := int(binary.BigEndian.Uint16(p[d.Body()+6:]))
		if r < 1 || r >= len(self) {
			return nil, refused(fmt.Sprintf("a sample description naming data reference %d of %d", r, len(self)-1))
		}
		ref[i+1] = r
	}
	// Each chunk's sample description, from stsc's runs.
	stsc := stbl.child(bmff.T("stsc"))
	if stsc == nil {
		return nil, refused("without a sample-to-chunk table")
	}
	q := stsc.payload()
	if len(q) < 8 {
		return nil, refused("its sample-to-chunk table cut short")
	}
	runs := int(binary.BigEndian.Uint32(q[4:]))
	if runs == 0 && count > 0 || runs > (len(q)-8)/12 {
		return nil, refused("its sample-to-chunk table out of step with its box")
	}
	out := make([]bool, count)
	prev := 0
	for i := range runs {
		e := q[8+12*i:]
		first, desc := int(binary.BigEndian.Uint32(e)), int(binary.BigEndian.Uint32(e[8:]))
		if i == 0 && first != 1 || first <= prev || desc < 1 || desc >= len(ref) {
			return nil, refused("a sample-to-chunk table out of order or naming no sample description")
		}
		next := count + 1
		if i+1 < runs {
			next = int(binary.BigEndian.Uint32(q[8+12*(i+1):]))
		}
		in := self[ref[desc]]
		for c := first; c < next && c <= count; c++ {
			out[c-1] = in
		}
		prev = first
	}
	return out, nil
}

// leafPayload is a box's payload, its header skipped without parsing it
// (as payload does, allocating): for a box read raw, after its 8- or
// 16-byte header.
func leafPayload(n *qtNode) []byte {
	if n.raw == nil {
		return n.body
	}
	h := 8
	if len(n.raw) >= 8 && binary.BigEndian.Uint32(n.raw) == 1 {
		h = 16
	}
	if len(n.raw) < h {
		return nil
	}
	return n.raw[h:]
}
