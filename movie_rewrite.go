package mediameta

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math"

	"github.com/0x20h/go-media-metadata/internal/bmff"
)

// RewriteOptions are what RewriteMovie changes besides putting the index
// first.
type RewriteOptions struct {
	// StripMetadata removes the movie's metadata: moov's and each track's
	// udta and meta (Keys, item lists, user data, XMP_), the XMP uuid box,
	// and a top-level meta or udta. What plays the movie stays: mvhd,
	// tkhd (with its matrix), mdhd, hdlr, stsd (colr, mdcv, clli in it),
	// edts, tref, sgpd and sbgp.
	StripMetadata bool
	// ZeroTimes sets the creation and modification times of mvhd, every
	// tkhd and every mdhd to 0 (QuickTime's "unknown"). They're kept
	// otherwise.
	ZeroTimes bool
}

// IndexFirst reports whether the QuickTime or MP4 movie in src (size bytes)
// has its index (moov) before its media data, as streaming wants it: no
// mdat comes before moov.
func IndexFirst(src io.ReaderAt, size int64) (bool, error) {
	top, err := rewriteBoxes(src, size)
	if err != nil {
		return false, err
	}
	for _, b := range top {
		switch b.Type {
		case bmff.T("moov"):
			return true, nil
		case bmff.T("mdat"):
			return false, nil
		}
	}
	return false, fmt.Errorf("%w: no movie box", ErrMalformed)
}

// RewriteMovie writes to dst the movie in src (size bytes) with its index
// first, as PlanRewriteMovie plans it.
func RewriteMovie(src io.ReaderAt, size int64, dst io.Writer, opts RewriteOptions) error {
	p, err := PlanRewriteMovie(src, size, opts)
	if err != nil {
		return err
	}
	_, err = p.WriteTo(dst)
	return err
}

// PlanRewriteMovie plans a rewrite of the QuickTime or MP4 movie in src
// (size bytes): ftyp, then moov, then the other top-level boxes in their
// order, top-level free, skip and wide boxes left out. No sample is
// decoded: boxes move, and every chunk offset (stco, co64) moves with the
// box it points into; an stco that a move would overflow becomes a co64,
// as FFmpeg's faststart does. With opts, the metadata is left out and
// the times zeroed (see RewriteOptions).
//
// A movie that is already so (and opts change nothing) gives an in-place
// plan without patches. Refused, with ErrUnsupported: fragmented movies
// (moof, mvex, sidx, mfra), sample auxiliary offsets (saio), media in
// other files (dref), a movie box over 64 MB; with ErrMalformed: a chunk
// offset outside the boxes kept. A file that isn't a movie gives
// ErrUnknownFormat.
func PlanRewriteMovie(src io.ReaderAt, size int64, opts RewriteOptions) (*Plan, error) {
	top, err := rewriteBoxes(src, size)
	if err != nil {
		return nil, err
	}
	mi := -1
	for i, b := range top {
		switch b.Type {
		case bmff.T("moov"):
			if mi >= 0 {
				return nil, fmt.Errorf("%w: two movie boxes", ErrMalformed)
			}
			mi = i
		case bmff.T("moof"), bmff.T("mfra"), bmff.T("sidx"), bmff.T("ssix"), bmff.T("emsg"):
			return nil, fmt.Errorf("%w: a fragmented movie (%v)", ErrUnsupported, b.Type)
		}
	}
	if mi < 0 {
		return nil, fmt.Errorf("%w: no movie box", ErrMalformed)
	}
	moovBox := top[mi]
	if moovBox.Size > maxMoov {
		return nil, fmt.Errorf("%w: a movie box of %d bytes", ErrUnsupported, moovBox.Size)
	}
	old := make([]byte, moovBox.Size)
	if _, err := src.ReadAt(old, moovBox.Offset); err != nil {
		return nil, fmt.Errorf("%w: reading the movie box: %v", ErrMalformed, err)
	}
	moov, err := parseQTNode(old, 0)
	if err != nil {
		return nil, err
	}
	if moov.raw != nil {
		return nil, fmt.Errorf("%w: a movie box this package can't walk", ErrUnsupported)
	}
	changed := false
	if opts.StripMetadata {
		changed = stripMovieMetadata(moov) || changed
	}
	tables, err := chunkTables(moov)
	if err != nil {
		return nil, err
	}
	if opts.ZeroTimes {
		changed = zeroMovieTimes(moov) || changed
	}

	// The boxes kept after moov, in their order; ftyp first.
	ftyp := -1
	if len(top) > 0 && top[0].Type == bmff.T("ftyp") {
		ftyp = 0
	}
	var keep []int
	for i, b := range top {
		if i == mi || i == ftyp {
			continue
		}
		switch {
		case b.Type == bmff.T("free") || b.Type == bmff.T("skip") || b.Type == bmff.T("wide"):
			changed = true
			continue
		case opts.StripMetadata && (b.Type == bmff.T("meta") || b.Type == bmff.T("udta") || isXMPUUID(src, b)):
			changed = true
			continue
		}
		keep = append(keep, i)
	}
	if mi != ftyp+1 {
		changed = true
	}

	// The layout, until the movie box's size settles: a co64 that an
	// stco becomes grows it, and moves everything after it.
	for range 4 {
		moovLen := int64(moov.encodedLen())
		at := moovLen
		if ftyp >= 0 {
			at += top[ftyp].Size
		}
		newAt := make(map[int]int64, len(keep))
		for _, i := range keep {
			newAt[i] = at
			at += top[i].Size
		}
		grew := false
		for _, t := range tables {
			vals := make([]int64, len(t.offsets))
			for j, v := range t.offsets {
				nv, ok := mapOffset(top, newAt, v)
				if !ok {
					return nil, fmt.Errorf("%w: chunk offset %d outside the media data kept", ErrMalformed, v)
				}
				vals[j] = nv
				if !t.wide && nv > math.MaxUint32 {
					t.wide, grew = true, true
				}
			}
			t.mapped = vals
		}
		if grew {
			continue
		}
		for _, t := range tables {
			if t.write() {
				changed = true
			}
		}
		if int64(moov.encodedLen()) != moovLen {
			return nil, fmt.Errorf("%w: the movie box changed size twice", ErrUnsupported)
		}
		if !changed && bytes.Equal(moov.encode(), old) {
			return (&spliceEdit{}).plan(src, size), nil
		}
		return rewritePlan(src, size, top, mi, ftyp, keep, moov.encode())
	}
	return nil, fmt.Errorf("%w: chunk offsets that don't settle", ErrUnsupported)
}

// rewriteBoxes reads a movie's top-level boxes (ErrUnknownFormat for a
// file that isn't one).
func rewriteBoxes(src io.ReaderAt, size int64) ([]bmff.Box, error) {
	if detectFormat(src, size) != formatMovie {
		return nil, ErrUnknownFormat
	}
	top, err := bmff.ReadBoxes(src, 0, size)
	if err != nil {
		return nil, err
	}
	if err := checkMovie(src, top); err != nil {
		return nil, err
	}
	return top, nil
}

// rewritePlan is the rewrite as splices of the source: moov's new bytes
// after ftyp (or first), the old moov and the boxes left out removed; the
// boxes kept stay in their order, so their bytes are copied as they are.
func rewritePlan(src io.ReaderAt, size int64, top []bmff.Box, mi, ftyp int, keep []int, moov []byte) (*Plan, error) {
	kept := make(map[int]bool, len(keep))
	for _, i := range keep {
		kept[i] = true
	}
	e := &spliceEdit{}
	at := int64(0)
	if ftyp >= 0 {
		at = top[ftyp].End()
	}
	if mi == ftyp+1 {
		e.add(splice{at: top[mi].Offset, del: top[mi].Size, data: moov})
	} else {
		e.add(splice{at: at, data: moov})
		e.add(splice{at: top[mi].Offset, del: top[mi].Size})
	}
	for i, b := range top {
		if i != mi && i != ftyp && !kept[i] {
			e.add(splice{at: b.Offset, del: b.Size})
		}
	}
	if err := e.sorted(); err != nil {
		return nil, err
	}
	return e.plan(src, size), nil
}

// mapOffset is where an offset into a kept box lands: in the same place
// in that box, at its new position.
func mapOffset(top []bmff.Box, newAt map[int]int64, v int64) (int64, bool) {
	for i, b := range top {
		if v >= b.Offset && v < b.End() {
			at, ok := newAt[i]
			if !ok {
				return 0, false
			}
			return at + (v - b.Offset), true
		}
	}
	return 0, false
}

// chunkTable is a track's stco or co64, its offsets as read.
type chunkTable struct {
	node    *qtNode
	wide    bool
	offsets []int64
	mapped  []int64
}

// write sets the table's box to its mapped offsets (a co64 where it's
// wide); it reports whether that changed it.
func (t *chunkTable) write() bool {
	w := 4
	typ := bmff.T("stco")
	if t.wide {
		w, typ = 8, bmff.T("co64")
	}
	p := make([]byte, 8, 8+w*len(t.mapped))
	binary.BigEndian.PutUint32(p[4:], uint32(len(t.mapped)))
	for _, v := range t.mapped {
		if t.wide {
			p = binary.BigEndian.AppendUint64(p, uint64(v))
		} else {
			p = binary.BigEndian.AppendUint32(p, uint32(v))
		}
	}
	if typ == t.node.typ && bytes.Equal(p, t.node.payload()) {
		return false
	}
	t.node.typ = typ
	t.node.setPayload(p)
	return true
}

// chunkTables reads every track's chunk offsets, and refuses what this
// rewrite can't move: sample auxiliary offsets, fragments (mvex), media in
// other files.
func chunkTables(moov *qtNode) ([]*chunkTable, error) {
	var out []*chunkTable
	for _, k := range moov.kids {
		switch k.typ {
		case bmff.T("mvex"):
			return nil, fmt.Errorf("%w: a fragmented movie (mvex)", ErrUnsupported)
		case bmff.T("trak"):
		default:
			continue
		}
		minf := path(k, "mdia", "minf")
		stbl := path(k, "mdia", "minf", "stbl")
		if minf == nil || stbl == nil {
			return nil, fmt.Errorf("%w: a track this package can't walk", ErrUnsupported)
		}
		if err := checkDref(minf); err != nil {
			return nil, err
		}
		for _, s := range stbl.kids {
			switch s.typ {
			case bmff.T("saio"):
				return nil, fmt.Errorf("%w: sample auxiliary information offsets (saio)", ErrUnsupported)
			case bmff.T("stco"), bmff.T("co64"):
				t, err := readChunkTable(s)
				if err != nil {
					return nil, err
				}
				out = append(out, t)
			}
		}
	}
	return out, nil
}

func readChunkTable(n *qtNode) (*chunkTable, error) {
	p := n.payload()
	wide := n.typ == bmff.T("co64")
	w := 4
	if wide {
		w = 8
	}
	if len(p) < 8 {
		return nil, fmt.Errorf("%w: a chunk offset box of %d bytes", ErrMalformed, len(p))
	}
	count := int(binary.BigEndian.Uint32(p[4:]))
	if count > (len(p)-8)/w {
		return nil, fmt.Errorf("%w: %d chunk offsets in %d bytes", ErrMalformed, count, len(p))
	}
	t := &chunkTable{node: n, wide: wide, offsets: make([]int64, count)}
	for i := range count {
		if wide {
			v := binary.BigEndian.Uint64(p[8+8*i:])
			if v > math.MaxInt64 {
				return nil, fmt.Errorf("%w: chunk offset %d", ErrMalformed, v)
			}
			t.offsets[i] = int64(v)
		} else {
			t.offsets[i] = int64(binary.BigEndian.Uint32(p[8+4*i:]))
		}
	}
	return t, nil
}

// checkDref refuses a track whose media data is in another file: a data
// reference entry ('url ' or 'urn ') without the self-contained flag.
func checkDref(minf *qtNode) error {
	dinf := minf.child(bmff.T("dinf"))
	if dinf == nil {
		return nil
	}
	b := dinf.payload()
	boxes, err := bmff.ReadBoxes(bytes.NewReader(b), 0, int64(len(b)))
	if err != nil {
		return fmt.Errorf("%w: a data information box that doesn't parse", ErrMalformed)
	}
	dref, ok := bmff.Find(boxes, bmff.T("dref"))
	if !ok || dref.BodyLen() < 8 {
		return nil
	}
	entries, err := bmff.ReadBoxes(bytes.NewReader(b), dref.Body()+8, dref.End())
	if err != nil {
		return fmt.Errorf("%w: a data reference box that doesn't parse", ErrMalformed)
	}
	for _, e := range entries {
		if e.BodyLen() < 4 {
			return fmt.Errorf("%w: a data reference entry of %d bytes", ErrMalformed, e.Size)
		}
		flags := binary.BigEndian.Uint32(b[e.Body():]) & 0xFFFFFF
		if flags&1 == 0 {
			return fmt.Errorf("%w: media data in another file (a %v data reference)", ErrUnsupported, e.Type)
		}
	}
	return nil
}

// stripMovieMetadata removes moov's and each track's udta and meta; it
// reports whether there were any.
func stripMovieMetadata(moov *qtNode) bool {
	changed := false
	drop := func(n *qtNode) {
		kept := n.kids[:0]
		for _, k := range n.kids {
			if k.typ == bmff.T("udta") || k.typ == bmff.T("meta") {
				changed = true
				continue
			}
			kept = append(kept, k)
		}
		n.kids = kept
	}
	drop(moov)
	for _, k := range moov.kids {
		if k.typ == bmff.T("trak") && k.raw == nil {
			drop(k)
		}
	}
	return changed
}

// zeroMovieTimes zeroes the creation and modification times of mvhd,
// every tkhd and every mdhd; it reports whether any wasn't zero.
func zeroMovieTimes(moov *qtNode) bool {
	changed := false
	zero := func(n *qtNode) {
		if n == nil {
			return
		}
		p := bytes.Clone(n.payload())
		if len(p) < 4 {
			return
		}
		end := 12 // version 0: two 32-bit times
		if p[0] == 1 {
			end = 20 // version 1: two 64-bit times
		}
		if len(p) < end {
			return
		}
		for i := 4; i < end; i++ {
			if p[i] != 0 {
				changed = true
				p[i] = 0
			}
		}
		n.setPayload(p)
	}
	zero(moov.child(bmff.T("mvhd")))
	for _, k := range moov.kids {
		if k.typ != bmff.T("trak") || k.raw != nil {
			continue
		}
		zero(k.child(bmff.T("tkhd")))
		zero(path(k, "mdia", "mdhd"))
	}
	return changed
}

// isXMPUUID reports whether b is a uuid box of XMP (xmpUUID).
func isXMPUUID(src io.ReaderAt, b bmff.Box) bool {
	if b.Type != bmff.T("uuid") || b.HeaderLen < 24 {
		return false
	}
	var id [16]byte
	if _, err := src.ReadAt(id[:], b.Body()-16); err != nil {
		return false
	}
	return bytes.Equal(id[:], xmpUUID)
}
