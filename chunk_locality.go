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
// whose media is this file's.
func drefSelfContained(minf *qtNode) ([]bool, error) {
	if minf == nil {
		return nil, nil
	}
	dinf := minf.child(bmff.T("dinf"))
	if dinf == nil {
		return nil, nil
	}
	b := dinf.payload()
	boxes, err := bmff.ParseBoxes(b, 0, int64(len(b)))
	if err != nil {
		return nil, fmt.Errorf("%w: a data information box that doesn't parse", ErrMalformed)
	}
	dref, ok := bmff.Find(boxes, bmff.T("dref"))
	if !ok || dref.BodyLen() < 8 {
		return nil, nil
	}
	entries, err := bmff.ParseBoxes(b, dref.Body()+8, dref.End())
	if err != nil {
		return nil, fmt.Errorf("%w: a data reference box that doesn't parse", ErrMalformed)
	}
	if n := int(binary.BigEndian.Uint32(b[dref.Body()+4:])); n != len(entries) {
		return nil, fmt.Errorf("%w: a data reference box of %d entries that says %d", ErrMalformed, len(entries), n)
	}
	out := make([]bool, len(entries)+1)
	for i, e := range entries {
		if e.BodyLen() < 4 {
			return nil, fmt.Errorf("%w: a data reference entry of %d bytes", ErrMalformed, e.Size)
		}
		out[i+1] = binary.BigEndian.Uint32(b[e.Body():])&1 != 0
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
