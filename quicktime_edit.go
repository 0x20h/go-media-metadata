package mediameta

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/0x20h/go-media-metadata/internal/bmff"
)

// VideoEdit is a change to a movie's recording date and place, and to its
// title, description, keywords and rating. A field left at its zero value
// leaves that part as it is, so the zero VideoEdit changes nothing.
type VideoEdit struct {
	// Created sets the recording date: the Keys entry
	// com.apple.quicktime.creationdate, as the time with its zone's
	// offset; the user data's ©day where the movie has one; and the movie
	// header's creation time, in UTC, where its field can hold it. nil
	// leaves the date as it is.
	Created *time.Time

	// GPS sets the place: the Keys entry
	// com.apple.quicktime.location.ISO6709, and the user data's ©xyz
	// where the movie has one. The altitude is written when HasAltitude.
	// nil leaves the place as it is; RemoveGPS removes both instead.
	GPS       *GPS
	RemoveGPS bool

	// Title and Description set the Keys entries com.apple.quicktime.title
	// and .description, and the user data's ©nam and ©des and an MP4 item
	// list's ©nam and desc where the movie has them. nil leaves them as
	// they are; "" removes them.
	Title, Description *string
	// Keywords sets the Keys entry com.apple.quicktime.keywords and an MP4
	// item list's keyw where the movie has one, as one value,
	// comma-separated: ExifTool reads only one value of an entry. A
	// keyword can't hold a comma. nil leaves them as they are; an empty
	// list removes them.
	Keywords *[]string
	// Rating sets the Keys entry com.apple.quicktime.rating.user, -1
	// (rejected) or 0 to 5, as text. nil leaves it as it is; RemoveRating
	// removes it.
	Rating       *int
	RemoveRating bool
	// XMP edits the movie's XMP packet with EditXMP, for what Keys and
	// user data don't hold: people (PersonInImage), face regions, and the
	// XMP mirror of the other fields. The packet is edited where it is:
	// a top-level uuid box (as MP4 files have it), when the new packet
	// fits there, and the user data's XMP_ (QuickTime). One that doesn't
	// fit its uuid box moves to XMP_, the box becoming a free box of its
	// size. A movie without a packet gets one in XMP_. nil leaves the XMP
	// as it is.
	XMP *XMPEdit
}

// check reports what's wrong with an edit.
func (e VideoEdit) check() error {
	if e.GPS != nil && e.RemoveGPS {
		return fmt.Errorf("mediameta: GPS and RemoveGPS together")
	}
	if g := e.GPS; g != nil && !validGPS(g) {
		return fmt.Errorf("mediameta: GPS position %+v out of range", *g)
	}
	if e.Rating != nil && e.RemoveRating {
		return fmt.Errorf("mediameta: Rating and RemoveRating together")
	}
	if r := e.Rating; r != nil && (*r < -1 || *r > 5) {
		return fmt.Errorf("mediameta: rating %d: not -1 to 5", *r)
	}
	// No slice is built here: an edit without text must cost what it did
	// before text could be set (the bench gate counts allocations).
	badText := func(t string) bool { return !utf8.ValidString(t) || len(t) > maxLeafBox/2 }
	for _, t := range [...]*string{e.Title, e.Description} {
		if t != nil && badText(*t) {
			return fmt.Errorf("mediameta: %q: text that isn't UTF-8, or too long", *t)
		}
	}
	if e.Keywords != nil {
		for _, k := range *e.Keywords {
			if badText(k) || strings.TrimSpace(k) == "" || strings.Contains(k, ",") {
				return fmt.Errorf("mediameta: keyword %q: not UTF-8, too long, empty, or with a comma", k)
			}
		}
	}
	return nil
}

// textChange is whether the edit changes a text field.
func (e VideoEdit) textChange() bool {
	return e.Title != nil || e.Description != nil || e.Keywords != nil || e.Rating != nil || e.RemoveRating
}

// maxMoov caps the movie box a write loads into memory: its sample tables
// are rewritten with it when the media data moves.
const maxMoov = 64 << 20

// PlanVideoMetadata is SetVideoMetadata's edit, computed but not written. It reads
// the movie box, never the media data.
//
// It is in place when the movie box keeps its size: a value of the same
// length, a movie box that shrinks by 8 bytes or more (it's padded with a
// free box inside it), or one that grows into a free box right after it.
// Otherwise the movie box is rewritten; when it comes before the media
// data, every chunk offset (stco, co64) that points past it moves with it.
func PlanVideoMetadata(src io.ReaderAt, size int64, edit VideoEdit) (*Plan, error) {
	if err := edit.check(); err != nil {
		return nil, err
	}
	if size < 8 {
		return nil, errNotQuickTime
	}
	isTSOnly := func() bool { return !startsWithFtyp(src, size) && isTS(src, size) }
	top, err := bmff.ReadBoxes(src, 0, size)
	if err != nil {
		if isTSOnly() {
			return nil, errTSReadOnly
		}
		if isMatroska(src, size) {
			return nil, errMatroskaReadOnly
		}
		if isAVI(src, size) {
			return nil, errAVIReadOnly
		}
		if isMPEGPS(src, size) {
			return nil, errMPEGPSReadOnly
		}
		if !looksLikeMovie(src, size) {
			return nil, errNotQuickTime
		}
		return nil, err
	}
	if err := checkMovie(src, top); err != nil {
		if isTSOnly() {
			return nil, errTSReadOnly
		}
		if isMatroska(src, size) {
			return nil, errMatroskaReadOnly
		}
		if isAVI(src, size) {
			return nil, errAVIReadOnly
		}
		if isMPEGPS(src, size) {
			return nil, errMPEGPSReadOnly
		}
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
	// The image data as declared, before the edit changes the movie box.
	ranges := movieImageRangesIn(top, old)
	moov, err := parseQTNode(old, 0)
	if err != nil {
		return nil, err
	}
	if err := applyVideoEdit(moov, edit); err != nil {
		return nil, err
	}
	var uuidSplice *splice
	if x := edit.XMP; x != nil && *x != (XMPEdit{}) {
		if uuidSplice, err = editMovieXMP(src, top, moov, *x); err != nil {
			return nil, err
		}
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
	if uuidSplice != nil {
		e.add(*uuidSplice)
		if err := e.sorted(); err != nil {
			return nil, err
		}
	}
	p := e.plan(src, size)
	if err := guardPlan(p, ranges); err != nil {
		return nil, err
	}
	return p, nil
}

// editMovieXMP applies edit to a movie's XMP packets (see VideoEdit.XMP):
// the user data's XMP_ in moov, changed in the tree, and a top-level uuid
// box, changed by the splice it returns (nil when there's none to change).
func editMovieXMP(src io.ReaderAt, top []bmff.Box, moov *qtNode, edit XMPEdit) (*splice, error) {
	var uuid *bmff.Box
	for i, b := range top {
		if b.Type != bmff.T("uuid") || b.HeaderLen < 24 {
			continue
		}
		var id [16]byte
		if _, err := src.ReadAt(id[:], b.Body()-16); err != nil {
			return nil, fmt.Errorf("%w: reading uuid: %v", ErrMalformed, err)
		}
		if bytes.Equal(id[:], xmpUUID) {
			uuid = &top[i]
			break
		}
	}
	udta := moov.child(bmff.T("udta"))
	if udta != nil && !udta.parsed() {
		return nil, fmt.Errorf("%w: user data that can't be edited", ErrUnsupported)
	}
	var xmpNode *qtNode // the user data's XMP_, if any
	if udta != nil {
		xmpNode = udta.child(bmff.T("XMP_"))
	}
	var out *splice
	var moved []byte // a uuid box's edited packet that didn't fit it
	if uuid != nil {
		if uuid.BodyLen() > maxXMP || uuid.HeaderLen != 24 {
			return nil, fmt.Errorf("%w: an XMP uuid box of %d bytes", ErrUnsupported, uuid.BodyLen())
		}
		old := make([]byte, uuid.BodyLen())
		if _, err := src.ReadAt(old, uuid.Body()); err != nil {
			return nil, fmt.Errorf("%w: reading XMP: %v", ErrMalformed, err)
		}
		packet, err := EditXMP(old, edit)
		if err != nil {
			return nil, err
		}
		if padded, ok := padTo(packet, len(old), ' '); ok {
			if !bytes.Equal(padded, old) {
				out = &splice{at: uuid.Body(), del: int64(len(old)), data: padded}
			}
		} else {
			// Too long for its box: the box becomes free space, zeroed, and
			// the packet goes to the user data, inside moov, which can grow
			// without moving anything else.
			free := bmff.AppendHeader(nil, bmff.T("free"), int(uuid.Size-8), false)
			out = &splice{at: uuid.Offset, del: uuid.Size, data: append(free, make([]byte, uuid.Size-8)...)}
			moved = packet
		}
	}
	switch {
	case xmpNode != nil && moved != nil:
		// The uuid box's packet was the one read: it replaces this one.
		xmpNode.setPayload(moved)
	case xmpNode != nil:
		// Both kept in step, so neither has old values.
		old := xmpNode.payload()
		packet, err := EditXMP(old, edit)
		if err != nil {
			return nil, err
		}
		if !bytes.Equal(packet, old) {
			xmpNode.setPayload(packet)
		}
	case uuid == nil || moved != nil:
		packet := moved
		if packet == nil {
			var err error
			if packet, err = EditXMP(nil, edit); err != nil {
				return nil, err
			}
		}
		if udta == nil {
			udta = &qtNode{typ: bmff.T("udta")}
			moov.kids = append(moov.kids, udta)
		}
		udta.kids = append(udta.kids, &qtNode{typ: bmff.T("XMP_"), body: packet})
	}
	return out, nil
}

// SetVideoMetadata writes to dst the QuickTime or MP4 movie in src (size bytes)
// with edit applied (see VideoEdit and PlanVideoMetadata). The media data is
// copied byte for byte. On an error nothing useful has been written to
// dst: write to a temporary file and rename it over the original only on
// success.
func SetVideoMetadata(src io.ReaderAt, size int64, dst io.Writer, edit VideoEdit) error {
	p, err := PlanVideoMetadata(src, size, edit)
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
	boxes, err := bmff.ParseBoxes(b, 0, int64(len(b)))
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
	kids, err := bmff.ParseBoxes(b, start, int64(len(b)))
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
	// One buffer for the whole tree: its size first, then every box
	// appended to it.
	return n.appendTo(make([]byte, 0, n.encodedLen()))
}

// contentLen is the length of n's payload as encode writes it.
func (n *qtNode) contentLen() int {
	l := len(n.pre)
	if n.kids != nil {
		for _, k := range n.kids {
			l += k.encodedLen()
		}
	} else {
		l += len(n.body)
	}
	return l
}

// encodedLen is the length of n as encode writes it, header included.
func (n *qtNode) encodedLen() int {
	if n.raw != nil {
		return len(n.raw)
	}
	l := n.contentLen()
	if n.large || l+8 > math.MaxUint32 {
		return l + 16
	}
	return l + 8
}

// appendTo appends n as encode writes it.
func (n *qtNode) appendTo(dst []byte) []byte {
	if n.raw != nil {
		return append(dst, n.raw...)
	}
	l := n.contentLen()
	dst = bmff.AppendHeader(dst, n.typ, l, n.large || l+8 > math.MaxUint32)
	dst = append(dst, n.pre...)
	if n.kids != nil {
		for _, k := range n.kids {
			dst = k.appendTo(dst)
		}
		return dst
	}
	return append(dst, n.body...)
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
	boxes, err := bmff.ParseBoxes(n.raw, 0, int64(len(n.raw)))
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
	if edit.Created == nil && edit.GPS == nil && !edit.RemoveGPS && !edit.textChange() {
		return nil
	}
	sets := edit.Created != nil || edit.GPS != nil || edit.Rating != nil ||
		edit.Title != nil && *edit.Title != "" || edit.Description != nil && *edit.Description != "" ||
		edit.Keywords != nil && len(*edit.Keywords) > 0
	keys, err := keysMeta(moov, sets)
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
		// The user data's ©xyz too, added where there's none, as ExifTool
		// writes it: readers that don't read Keys (Android's) look there.
		if udta == nil && moov.child(bmff.T("udta")) == nil {
			udta = &qtNode{typ: bmff.T("udta")}
			moov.kids = append(moov.kids, udta)
		}
		if udta != nil {
			if udta.child(bmff.T("\xa9xyz")) == nil {
				udta.kids = append(udta.kids, &qtNode{typ: bmff.T("\xa9xyz")})
			}
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
	if !edit.textChange() {
		return nil
	}
	items := itemList(udta)
	// Every place a reader finds the title and description gets the new
	// value, so none keeps the old one: Keys; the item list's ©nam, and
	// ©des (ExifTool's ItemList Description) and desc; the user data's
	// QuickTime text (©nam, ©des) and 3GPP strings (titl, dscp). An item
	// list gets ©nam and ©des when it has none (ExifTool writes them);
	// Keys are added when missing, as before; the rest are updated where
	// they are.
	for _, f := range [...]struct {
		v              *string
		key, udta, gpp string
		items          [2]string // set (added when missing), updated
	}{
		{edit.Title, keyTitle, "\xa9nam", "titl", [2]string{"\xa9nam", ""}},
		{edit.Description, keyDesc, "\xa9des", "dscp", [2]string{"\xa9des", "desc"}},
	} {
		if f.v == nil {
			continue
		}
		if *f.v == "" {
			if keys != nil {
				keys.remove(f.key)
			}
			if udta != nil {
				removeKids(udta, bmff.T(f.udta))
				removeKids(udta, bmff.T(f.gpp))
			}
			if items != nil {
				for _, it := range f.items {
					if it != "" {
						removeKids(items, bmff.T(it))
					}
				}
			}
			continue
		}
		if err := keys.set(f.key, *f.v); err != nil {
			return err
		}
		if udta != nil {
			setUdtaText(udta, f.udta, *f.v)
			setGppText(udta, f.gpp, *f.v)
		}
		addItem(items, f.items[0], *f.v)
		if f.items[1] != "" {
			setItem(items, f.items[1], *f.v)
		}
	}
	if kw := edit.Keywords; kw != nil {
		if len(*kw) == 0 {
			if keys != nil {
				keys.remove(keyKeywords)
			}
			if items != nil {
				removeKids(items, bmff.T("keyw"))
			}
		} else {
			if err := keys.set(keyKeywords, strings.Join(*kw, ",")); err != nil {
				return err
			}
			setItem(items, "keyw", strings.Join(*kw, ","))
		}
	}
	if edit.RemoveRating && keys != nil {
		keys.remove(keyRating)
	}
	if r := edit.Rating; r != nil {
		if err := keys.set(keyRating, strconv.Itoa(*r)); err != nil {
			return err
		}
	}
	return nil
}

// itemList is the item list of an MP4's user data metadata (a meta box of
// handler mdir), nil without one.
func itemList(udta *qtNode) *qtNode {
	if udta == nil {
		return nil
	}
	m := udta.child(bmff.T("meta"))
	if m == nil || !m.parsed() || m.handler() != "mdir" {
		return nil
	}
	if l := m.child(bmff.T("ilst")); l != nil && l.parsed() {
		return l
	}
	return nil
}

// setItem sets item typ of an item list to UTF-8 text s, where the list
// has one; a missing item isn't added.
func setItem(ilst *qtNode, typ, s string) {
	if ilst == nil {
		return
	}
	for i, it := range ilst.kids {
		if it.typ == bmff.T(typ) {
			ilst.kids[i] = &qtNode{typ: it.typ, kids: []*qtNode{textData(s)}}
			return
		}
	}
}

// addItem sets item typ of an item list to UTF-8 text s, adding the item
// when the list has none.
func addItem(ilst *qtNode, typ, s string) {
	if ilst == nil {
		return
	}
	for _, it := range ilst.kids {
		if it.typ == bmff.T(typ) {
			setItem(ilst, typ, s)
			return
		}
	}
	ilst.kids = append(ilst.kids, &qtNode{typ: bmff.T(typ), kids: []*qtNode{textData(s)}})
}

// setGppText sets a 3GPP asset string box of the user data (titl, dscp:
// TS 26.244 8.1) to UTF-8 text s, keeping its language, where there is
// one.
func setGppText(udta *qtNode, typ, s string) {
	n := udta.child(bmff.T(typ))
	if n == nil {
		return
	}
	old := n.payload()
	lang := []byte{0x55, 0xc4} // undetermined
	if len(old) >= 6 {
		lang = old[4:6]
	}
	p := make([]byte, 4, 4+2+len(s)+1) // version and flags 0
	p = append(append(append(p, lang...), s...), 0)
	n.setPayload(p)
}

// textData is a data box of UTF-8 text (type 1, locale 0).
func textData(s string) *qtNode {
	return &qtNode{typ: bmff.T("data"), body: append(binary.BigEndian.AppendUint32(make([]byte, 0, 8+len(s)), 1), append(make([]byte, 4), s...)...)}
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
	data := textData(s)
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
		boxes, err := bmff.ParseBoxes(b, 0, int64(len(b)))
		if err != nil || len(boxes) != 1 {
			return fmt.Errorf("%w: an ilst item that doesn't parse", ErrMalformed)
		}
		kids, err := bmff.ParseBoxes(b, boxes[0].Body(), boxes[0].End())
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
