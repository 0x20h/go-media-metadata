package mediameta

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"time"

	"github.com/0x20h/go-media-metadata/internal/bmff"
)

// VideoEdit is a change to a movie's recording date and place. A field
// left at its zero value leaves that part as it is.
type VideoEdit struct {
	// Created sets the recording date: the Keys entry
	// com.apple.quicktime.creationdate, as the time with its zone's
	// offset; the user data's ©day where the movie has one; and the movie
	// header's creation time, in UTC, where its field can hold it.
	Created *time.Time

	// GPS sets the place: the Keys entry
	// com.apple.quicktime.location.ISO6709, and the user data's ©xyz
	// where the movie has one. The altitude is written when HasAltitude.
	// RemoveGPS removes both instead.
	GPS       *GPS
	RemoveGPS bool
}

// maxMoov caps the movie box a write loads into memory: its sample tables
// are rewritten with it when the media data moves.
const maxMoov = 64 << 20

// PlanVideoEdit is EditVideo's edit, computed but not written. It reads
// the movie box, never the media data.
//
// It is in place when the movie box keeps its size: a value of the same
// length, a movie box that shrinks by 8 bytes or more (it's padded with a
// free box inside it), or one that grows into a free box right after it.
// Otherwise the movie box is rewritten; when it comes before the media
// data, every chunk offset (stco, co64) that points past it moves with it.
func PlanVideoEdit(src io.ReaderAt, size int64, edit VideoEdit) (*Plan, error) {
	if edit.GPS != nil && edit.RemoveGPS {
		return nil, fmt.Errorf("mediameta: GPS and RemoveGPS together")
	}
	if g := edit.GPS; g != nil && !validGPS(g) {
		return nil, fmt.Errorf("mediameta: GPS position %+v out of range", *g)
	}
	if size < 8 {
		return nil, ErrNotQuickTime
	}
	top, err := bmff.ReadBoxes(src, 0, size)
	if err != nil {
		if !looksLikeMovie(src, size) {
			return nil, ErrNotQuickTime
		}
		return nil, err
	}
	if err := checkMovie(src, top); err != nil {
		return nil, err
	}
	mi := -1
	for i, b := range top {
		if b.Type == bmff.T("moov") {
			if mi >= 0 {
				return nil, fmt.Errorf("%w: two movie boxes", ErrMalformed)
			}
			mi = i
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
	if err := applyVideoEdit(moov, edit); err != nil {
		return nil, err
	}
	enc := moov.encode()
	delta := int64(len(enc)) - moovBox.Size

	e := &spliceEdit{}
	switch {
	case delta == 0:
		diffSplices(e, moovBox.Offset, old, enc)
	case delta <= -8:
		// Pad the movie box back to its size with a free box inside it.
		moov.kids = append(moov.kids, &qtNode{typ: bmff.T("free"), body: make([]byte, -delta-8)})
		diffSplices(e, moovBox.Offset, old, moov.encode())
	default:
		if mi+1 < len(top) && isFree(top[mi+1]) && top[mi+1].Size-delta >= 8 && top[mi+1].HeaderLen == 8 {
			// Grow (or shrink) into the free box after it.
			next := top[mi+1]
			region := make([]byte, moovBox.Size+next.Size)
			copy(region, old)
			if _, err := src.ReadAt(region[len(old):], next.Offset); err != nil {
				return nil, fmt.Errorf("%w: reading a free box: %v", ErrMalformed, err)
			}
			free := bmff.AppendHeader(nil, next.Type, int(next.Size-delta-8), false)
			free = append(free, make([]byte, next.Size-delta-8)...)
			diffSplices(e, moovBox.Offset, region, append(enc, free...))
			break
		}
		if err := shiftChunkOffsets(moov, top, moovBox, delta); err != nil {
			return nil, err
		}
		enc = moov.encode()
		if int64(len(enc))-moovBox.Size != delta {
			return nil, fmt.Errorf("%w: the movie box changed size twice", ErrUnsupported)
		}
		e.add(splice{at: moovBox.Offset, del: moovBox.Size, data: enc})
	}
	return e.plan(src, size), nil
}

// EditVideo writes to dst the QuickTime or MP4 movie in src (size bytes)
// with edit applied (see VideoEdit and PlanVideoEdit). The media data is
// copied byte for byte. On an error nothing useful has been written to
// dst: write to a temporary file and rename it over the original only on
// success.
func EditVideo(src io.ReaderAt, size int64, dst io.Writer, edit VideoEdit) error {
	p, err := PlanVideoEdit(src, size, edit)
	if err != nil {
		return err
	}
	_, err = p.WriteTo(dst)
	return err
}

func validGPS(g *GPS) bool {
	ok := func(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
	return ok(g.Latitude) && ok(g.Longitude) && math.Abs(g.Latitude) <= 90 && math.Abs(g.Longitude) <= 180 &&
		(!g.HasAltitude || ok(g.Altitude) && math.Abs(g.Altitude) < 1e7)
}

func isFree(b bmff.Box) bool { return b.Type == bmff.T("free") || b.Type == bmff.T("skip") }

// diffSplices adds a same-length splice for each run of bytes that
// differs between old and new (equal lengths), at base. Runs a few bytes
// apart become one splice.
func diffSplices(e *spliceEdit, base int64, old, new []byte) {
	start, end := -1, -1 // the run being built: [start, end)
	flush := func() {
		if start >= 0 {
			e.add(splice{at: base + int64(start), del: int64(end - start), data: new[start:end]})
		}
	}
	for i := range new {
		if old[i] == new[i] {
			continue
		}
		if start >= 0 && i-end <= patchMergeGap {
			end = i + 1
			continue
		}
		flush()
		start, end = i, i+1
	}
	flush()
}

// qtNode is a box of a movie box being edited: kept as it was (raw), a
// leaf with a new payload (body), or a container whose children are
// encoded again (kids, after pre: a FullBox's version and flags).
type qtNode struct {
	typ   bmff.Type
	raw   []byte
	body  []byte
	pre   []byte
	kids  []*qtNode
	large bool
}

// qtContainers are the boxes on the way to what a write changes: the
// metadata, the track headers' dates and the chunk offsets.
var qtContainers = map[bmff.Type]bool{
	bmff.T("moov"): true, bmff.T("trak"): true, bmff.T("mdia"): true, bmff.T("minf"): true,
	bmff.T("stbl"): true, bmff.T("udta"): true, bmff.T("meta"): true, bmff.T("ilst"): true,
}

// parseQTNode parses box b (header included): a container's children,
// and their children where they are containers; other boxes stay raw,
// with an explicit size where theirs is 0 ("to the end"), so a box
// added after one doesn't end up inside it.
func parseQTNode(b []byte, depth int) (*qtNode, error) {
	boxes, err := bmff.ReadBoxes(bytes.NewReader(b), 0, int64(len(b)))
	if err != nil || len(boxes) != 1 {
		return nil, fmt.Errorf("%w: a box that doesn't parse", ErrMalformed)
	}
	box := boxes[0]
	n := &qtNode{typ: box.Type, large: box.Large()}
	if !qtContainers[box.Type] || depth > bmff.MaxDepth {
		n.raw = bmff.ExplicitSize(b)
		return n, nil
	}
	start := box.Body()
	if box.Type == bmff.T("meta") {
		// MP4's meta is a FullBox: its children start 4 bytes in.
		if len(b) >= int(start)+8 && string(b[start+4:start+8]) != "hdlr" && box.BodyLen() >= 4 {
			n.pre = b[start : start+4]
			start += 4
		}
	}
	kids, err := bmff.ReadBoxes(bytes.NewReader(b), start, int64(len(b)))
	if err != nil {
		// A container this package can't walk (udta with a trailing
		// zero, say) is kept as it was, and not edited.
		return &qtNode{typ: box.Type, raw: bmff.ExplicitSize(b)}, nil
	}
	for _, k := range kids {
		c, err := parseQTNode(b[k.Offset:k.End()], depth+1)
		if err != nil {
			return nil, err
		}
		n.kids = append(n.kids, c)
	}
	return n, nil
}

// encode is the box's bytes.
func (n *qtNode) encode() []byte {
	if n.raw != nil {
		return n.raw
	}
	var content []byte
	content = append(content, n.pre...)
	if n.kids != nil {
		for _, k := range n.kids {
			content = append(content, k.encode()...)
		}
	} else {
		content = append(content, n.body...)
	}
	large := n.large || len(content)+8 > math.MaxUint32
	out := bmff.AppendHeader(make([]byte, 0, len(content)+16), n.typ, len(content), large)
	return append(out, content...)
}

func (n *qtNode) parsed() bool { return n.raw == nil }

// child is n's first child of type t.
func (n *qtNode) child(t bmff.Type) *qtNode {
	for _, k := range n.kids {
		if k.typ == t {
			return k
		}
	}
	return nil
}

// payload is a leaf's payload.
func (n *qtNode) payload() []byte {
	if n.raw == nil {
		return n.body
	}
	boxes, err := bmff.ReadBoxes(bytes.NewReader(n.raw), 0, int64(len(n.raw)))
	if err != nil || len(boxes) != 1 {
		return nil
	}
	return n.raw[boxes[0].Body():]
}

// setPayload makes n a leaf with payload p.
func (n *qtNode) setPayload(p []byte) {
	n.raw, n.kids, n.body = nil, nil, p
}

// handler is a meta box's hdlr handler type.
func (n *qtNode) handler() string {
	h := n.child(bmff.T("hdlr"))
	if h == nil {
		return ""
	}
	p := h.payload()
	if len(p) < 12 {
		return ""
	}
	return string(p[8:12])
}

// applyVideoEdit makes the edit's changes to the parsed movie box.
func applyVideoEdit(moov *qtNode, edit VideoEdit) error {
	if edit.Created == nil && edit.GPS == nil && !edit.RemoveGPS {
		return nil
	}
	keys, err := keysMeta(moov, edit.Created != nil || edit.GPS != nil)
	if err != nil {
		return err
	}
	udta := moov.child(bmff.T("udta"))
	if udta != nil && !udta.parsed() {
		udta = nil
	}
	if t := edit.Created; t != nil {
		s := t.Format("2006-01-02T15:04:05-0700")
		if err := keys.set(keyCreated, s); err != nil {
			return err
		}
		if udta != nil {
			setUdtaText(udta, "\xa9day", s)
		}
		if mvhd := moov.child(bmff.T("mvhd")); mvhd != nil {
			setMvhdCreated(mvhd, *t)
		}
	}
	if g := edit.GPS; g != nil {
		s := iso6709(g)
		if err := keys.set(keyLocation, s); err != nil {
			return err
		}
		if udta != nil {
			setUdtaText(udta, "\xa9xyz", s)
		}
	}
	if edit.RemoveGPS {
		if keys != nil {
			keys.remove(keyLocation)
		}
		if udta != nil {
			removeKids(udta, bmff.T("\xa9xyz"))
		}
	}
	return nil
}

// iso6709 is a position as ISO 6709 text, as cameras write it but with
// six decimals (about 0.1 m).
func iso6709(g *GPS) string {
	s := fmt.Sprintf("%+010.6f%+011.6f", g.Latitude, g.Longitude)
	if g.HasAltitude {
		s += fmt.Sprintf("%+.3f", g.Altitude)
	}
	return s + "/"
}

// setMvhdCreated sets the movie header's creation time to t in seconds
// since 1904 (UTC), where the field can hold it.
func setMvhdCreated(mvhd *qtNode, t time.Time) {
	p := bytes.Clone(mvhd.payload())
	if len(p) < 4 {
		return
	}
	secs := t.Unix() - qtEpoch.Unix()
	if secs <= 0 || secs >= maxQTSeconds {
		return
	}
	switch p[0] {
	case 0:
		if len(p) < 8 || secs > math.MaxUint32 {
			return
		}
		binary.BigEndian.PutUint32(p[4:], uint32(secs))
	case 1:
		if len(p) < 12 {
			return
		}
		binary.BigEndian.PutUint64(p[4:], uint64(secs))
	default:
		return
	}
	mvhd.setPayload(p)
}

// setUdtaText sets the first text of user data entry typ, where the movie
// has one; a missing entry isn't added.
func setUdtaText(udta *qtNode, typ, s string) {
	n := udta.child(bmff.T(typ))
	if n == nil {
		return
	}
	old := n.payload()
	lang := []byte{0x55, 0xc4} // undetermined
	if len(old) >= 4 {
		lang = old[2:4]
	}
	p := binary.BigEndian.AppendUint16(nil, uint16(len(s)))
	p = append(append(p, lang...), s...)
	n.setPayload(p)
}

func removeKids(n *qtNode, t bmff.Type) {
	kept := n.kids[:0]
	for _, k := range n.kids {
		if k.typ != t {
			kept = append(kept, k)
		}
	}
	n.kids = kept
}

// keysNode is a Keys metadata box being edited: its keys and ilst.
type keysNode struct {
	keys, ilst *qtNode
	names      []string
}

// keysMeta finds the movie's Keys metadata: a meta box with handler mdta
// in moov, else in udta. With create, one is added to moov where there's
// none; without, nil is returned then.
func keysMeta(moov *qtNode, create bool) (*keysNode, error) {
	var found *qtNode
	if m := moov.child(bmff.T("meta")); m != nil && m.parsed() && m.handler() == "mdta" {
		found = m
	} else if u := moov.child(bmff.T("udta")); u != nil && u.parsed() {
		if m := u.child(bmff.T("meta")); m != nil && m.parsed() && m.handler() == "mdta" {
			found = m
		}
	}
	if found == nil {
		if !create {
			return nil, nil
		}
		if m := moov.child(bmff.T("meta")); m != nil {
			return nil, fmt.Errorf("%w: the movie box has a metadata box that isn't Keys", ErrUnsupported)
		}
		hdlr := &qtNode{typ: bmff.T("hdlr"), body: append(append(make([]byte, 8), "mdta"...), make([]byte, 13)...)}
		found = &qtNode{typ: bmff.T("meta"), kids: []*qtNode{
			hdlr,
			{typ: bmff.T("keys"), body: make([]byte, 8)},
			{typ: bmff.T("ilst"), kids: []*qtNode{}},
		}}
		// Before udta, where QuickTime writers put it.
		at := len(moov.kids)
		for i, k := range moov.kids {
			if k.typ == bmff.T("udta") {
				at = i
				break
			}
		}
		moov.kids = append(moov.kids[:at], append([]*qtNode{found}, moov.kids[at:]...)...)
	}
	k := &keysNode{keys: found.child(bmff.T("keys")), ilst: found.child(bmff.T("ilst"))}
	if k.keys == nil || k.ilst == nil || !k.ilst.parsed() {
		return nil, fmt.Errorf("%w: a Keys metadata box without keys or ilst", ErrUnsupported)
	}
	p := k.keys.payload()
	k.names = parseKeys(p)
	if len(p) < 8 || uint32(len(k.names)) != binary.BigEndian.Uint32(p[4:]) {
		return nil, fmt.Errorf("%w: a keys box this package can't read whole", ErrUnsupported)
	}
	return k, nil
}

// index is the 1-based index of key name, 0 without one.
func (k *keysNode) index(name string) uint32 {
	for i, n := range k.names {
		if n == name {
			return uint32(i + 1)
		}
	}
	return 0
}

// item is ilst's item for index i.
func (k *keysNode) item(i uint32) *qtNode {
	for _, it := range k.ilst.kids {
		if binary.BigEndian.Uint32(it.typ[:]) == i {
			return it
		}
	}
	return nil
}

// set sets key name's value to UTF-8 text s: the item's data box, or a new
// key and item.
func (k *keysNode) set(name, s string) error {
	data := &qtNode{typ: bmff.T("data"), body: append(binary.BigEndian.AppendUint32(make([]byte, 0, 8+len(s)), 1), append(make([]byte, 4), s...)...)}
	i := k.index(name)
	if i == 0 {
		k.names = append(k.names, name)
		i = uint32(len(k.names))
		k.writeKeys()
	}
	it := k.item(i)
	if it == nil {
		var t bmff.Type
		binary.BigEndian.PutUint32(t[:], i)
		k.ilst.kids = append(k.ilst.kids, &qtNode{typ: t, kids: []*qtNode{data}})
		return nil
	}
	// Keep the item's other boxes; replace its first data box.
	if it.raw != nil {
		b := it.raw
		boxes, err := bmff.ReadBoxes(bytes.NewReader(b), 0, int64(len(b)))
		if err != nil || len(boxes) != 1 {
			return fmt.Errorf("%w: an ilst item that doesn't parse", ErrMalformed)
		}
		kids, err := bmff.ReadBoxes(bytes.NewReader(b), boxes[0].Body(), boxes[0].End())
		if err != nil {
			return fmt.Errorf("%w: an ilst item that doesn't parse", ErrMalformed)
		}
		it.raw, it.large = nil, boxes[0].Large()
		it.kids = []*qtNode{}
		for _, c := range kids {
			it.kids = append(it.kids, &qtNode{typ: c.Type, raw: bmff.ExplicitSize(b[c.Offset:c.End()])})
		}
	}
	for j, c := range it.kids {
		if c.typ == bmff.T("data") {
			it.kids[j] = data
			return nil
		}
	}
	it.kids = append(it.kids, data)
	return nil
}

// remove removes key name and its item; the items after it are
// renumbered.
func (k *keysNode) remove(name string) {
	i := k.index(name)
	if i == 0 {
		return
	}
	k.names = append(k.names[:i-1], k.names[i:]...)
	k.writeKeys()
	kept := k.ilst.kids[:0]
	for _, it := range k.ilst.kids {
		n := binary.BigEndian.Uint32(it.typ[:])
		switch {
		case n == i:
			continue
		case n > i:
			binary.BigEndian.PutUint32(it.typ[:], n-1)
			if it.raw != nil {
				it.raw = bytes.Clone(it.raw)
				// The type sits after the 4-byte size (also for a large
				// header, whose 64-bit size follows the type).
				copy(it.raw[4:8], it.typ[:])
			}
		}
		kept = append(kept, it)
	}
	k.ilst.kids = kept
}

// writeKeys encodes the key names into the keys box.
func (k *keysNode) writeKeys() {
	p := make([]byte, 4, 64) // version, flags
	p = binary.BigEndian.AppendUint32(p, uint32(len(k.names)))
	for _, n := range k.names {
		p = binary.BigEndian.AppendUint32(p, uint32(8+len(n)))
		p = append(append(p, "mdta"...), n...)
	}
	k.keys.setPayload(p)
}

// shiftChunkOffsets moves every chunk offset that points past the movie
// box by delta, its change in size. Where other absolute offsets could
// point past it (fragments, sample auxiliary information, item
// locations), it refuses rather than leave them wrong.
func shiftChunkOffsets(moov *qtNode, top []bmff.Box, moovBox bmff.Box, delta int64) error {
	end := moovBox.End()
	needed := false
	for _, b := range top {
		if b.Offset >= end {
			needed = true
		}
		switch b.Type {
		case bmff.T("moof"), bmff.T("sidx"), bmff.T("mfra"), bmff.T("meta"), bmff.T("ssix"), bmff.T("emsg"):
			if b.Offset >= end || b.Type == bmff.T("meta") {
				return fmt.Errorf("%w: a %v box with offsets this package doesn't move", ErrUnsupported, b.Type)
			}
		}
	}
	if !needed {
		return nil
	}
	for _, trak := range moov.kids {
		if trak.typ != bmff.T("trak") {
			continue
		}
		stbl := path(trak, "mdia", "minf", "stbl")
		if stbl == nil {
			if trak.raw != nil {
				return fmt.Errorf("%w: a track this package can't walk", ErrUnsupported)
			}
			continue
		}
		for _, k := range stbl.kids {
			switch k.typ {
			case bmff.T("stco"), bmff.T("co64"):
				p, err := shiftOffsets(k.payload(), k.typ == bmff.T("co64"), end, delta)
				if err != nil {
					return err
				}
				k.setPayload(p)
			case bmff.T("saio"):
				return fmt.Errorf("%w: sample auxiliary information offsets this package doesn't move", ErrUnsupported)
			}
		}
	}
	return nil
}

// path follows parsed children of these types from n.
func path(n *qtNode, types ...string) *qtNode {
	for _, t := range types {
		if n == nil || !n.parsed() {
			return nil
		}
		n = n.child(bmff.T(t))
	}
	if n == nil || !n.parsed() {
		return nil
	}
	return n
}

// shiftOffsets adds delta to every offset in an stco or co64 payload
// that is at or past from.
func shiftOffsets(p []byte, wide bool, from, delta int64) ([]byte, error) {
	if len(p) < 8 {
		return nil, fmt.Errorf("%w: a chunk offset box of %d bytes", ErrMalformed, len(p))
	}
	n := int(binary.BigEndian.Uint32(p[4:]))
	w := 4
	if wide {
		w = 8
	}
	if n > (len(p)-8)/w {
		return nil, fmt.Errorf("%w: %d chunk offsets in %d bytes", ErrMalformed, n, len(p))
	}
	out := bytes.Clone(p)
	for i := range n {
		at := 8 + i*w
		if wide {
			v := int64(binary.BigEndian.Uint64(out[at:]))
			if v >= from {
				binary.BigEndian.PutUint64(out[at:], uint64(v+delta))
			}
			continue
		}
		v := int64(binary.BigEndian.Uint32(out[at:]))
		if v >= from {
			nv := v + delta
			if nv < 0 || nv > math.MaxUint32 {
				return nil, fmt.Errorf("%w: a chunk offset outgrows its 32-bit field", ErrUnsupported)
			}
			binary.BigEndian.PutUint32(out[at:], uint32(nv))
		}
	}
	return out, nil
}
