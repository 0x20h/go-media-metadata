package bmff

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

// MaxMeta caps the size of a meta box read into memory.
const MaxMeta = 16 << 20

// Meta is a parsed meta box (ISO/IEC 14496-12 8.11.1) with the item
// boxes HEIF uses. Children this package doesn't change are kept as raw
// bytes and written back as they were, in their order.
type Meta struct {
	Version  uint8
	Flags    uint32
	large    bool
	children []child

	// Handler is the hdlr box's handler type ("pict" for HEIF images).
	Handler Type
	// Primary is the pitm box's item; HasPrimary is false without one.
	Primary    uint32
	HasPrimary bool
	// ItemTypes maps item ids to their infe item type, where known.
	ItemTypes map[uint32]Type

	Loc  *ILoc
	Prop *IPrp
	// Refs are the iref box's references, read only: the box is written
	// back as it was.
	Refs []ItemRef
}

// ItemRef is one reference of an iref box (ISO/IEC 14496-12 8.11.12):
// From refers to each of To by Type ("thmb", "auxl", "dimg", "cdsc", …).
type ItemRef struct {
	Type Type
	From uint32
	To   []uint32
}

type child struct {
	typ Type
	raw []byte // the whole box, header included, for children kept as is
}

// ParseMeta parses a whole meta box: b holds it from its header on.
func ParseMeta(b []byte) (*Meta, error) {
	if len(b) > MaxMeta {
		return nil, fmt.Errorf("%w: meta box of %d bytes", ErrMalformed, len(b))
	}
	r := bytes.NewReader(b)
	top, err := ReadBoxes(r, 0, int64(len(b)))
	if err != nil {
		return nil, err
	}
	if len(top) != 1 || top[0].Type != T("meta") {
		return nil, fmt.Errorf("%w: not a meta box", ErrMalformed)
	}
	mb := top[0]
	body := b[mb.Body():mb.End()]
	if len(body) < 4 {
		return nil, fmt.Errorf("%w: meta box without its version", ErrMalformed)
	}
	m := &Meta{Version: body[0], Flags: binary.BigEndian.Uint32(body[:4]) & 0xFFFFFF, large: mb.Large(),
		ItemTypes: map[uint32]Type{}}
	kids, err := ReadBoxes(bytes.NewReader(body), 4, int64(len(body)))
	if err != nil {
		return nil, err
	}
	for _, k := range kids {
		raw := body[k.Offset:k.End()]
		kb := body[k.Body():k.End()]
		switch k.Type {
		case T("hdlr"):
			// FullBox, pre_defined, then handler_type.
			if len(kb) >= 12 {
				copy(m.Handler[:], kb[8:12])
			}
		case T("pitm"):
			rd := &reader{b: kb}
			v := rd.u8()
			rd.need(3)
			rd.off += 3
			if v == 0 {
				m.Primary = uint32(rd.u16())
			} else {
				m.Primary = rd.u32()
			}
			if rd.err != nil {
				return nil, rd.err
			}
			m.HasPrimary = true
		case T("iinf"):
			if err := m.parseIinf(kb); err != nil {
				return nil, err
			}
		case T("iref"):
			if err := m.parseIref(kb); err != nil {
				return nil, err
			}
		case T("iloc"):
			if m.Loc != nil {
				return nil, fmt.Errorf("%w: two iloc boxes", ErrMalformed)
			}
			loc, err := parseILoc(kb, k.Large())
			if err != nil {
				return nil, err
			}
			m.Loc = loc
			m.children = append(m.children, child{typ: k.Type})
			continue
		case T("iprp"):
			if m.Prop != nil {
				return nil, fmt.Errorf("%w: two iprp boxes", ErrMalformed)
			}
			p, err := parseIPrp(kb, k.Large())
			if err != nil {
				return nil, err
			}
			m.Prop = p
			m.children = append(m.children, child{typ: k.Type})
			continue
		}
		m.children = append(m.children, child{typ: k.Type, raw: explicitSize(raw)})
	}
	return m, nil
}

// parseIinf records each infe's item type (versions 2 and 3; earlier
// versions have none).
func (m *Meta) parseIinf(b []byte) error {
	if len(b) < 4 {
		return fmt.Errorf("%w: short iinf", ErrMalformed)
	}
	start := int64(6)
	if b[0] != 0 {
		start = 8
	}
	if int64(len(b)) < start {
		return fmt.Errorf("%w: short iinf", ErrMalformed)
	}
	entries, err := ReadBoxes(bytes.NewReader(b), start, int64(len(b)))
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Type != T("infe") {
			continue
		}
		rd := &reader{b: b[e.Body():e.End()]}
		v := rd.u8()
		rd.need(3)
		rd.off += 3
		var id uint32
		switch v {
		case 2:
			id = uint32(rd.u16())
		case 3:
			id = rd.u32()
		default:
			continue
		}
		rd.u16() // item_protection_index
		if !rd.need(4) {
			return rd.err
		}
		var t Type
		copy(t[:], rd.b[rd.off:rd.off+4])
		m.ItemTypes[id] = t
	}
	return nil
}

// parseIref reads an iref box's references (version 0: 16-bit item ids,
// 1: 32-bit).
func (m *Meta) parseIref(b []byte) error {
	if len(b) < 4 {
		return fmt.Errorf("%w: short iref", ErrMalformed)
	}
	wide := b[0] != 0
	refs, err := ReadBoxes(bytes.NewReader(b), 4, int64(len(b)))
	if err != nil {
		return err
	}
	for _, r := range refs {
		rd := &reader{b: b[r.Body():r.End()]}
		id := func() uint32 {
			if wide {
				return rd.u32()
			}
			return uint32(rd.u16())
		}
		ref := ItemRef{Type: r.Type, From: id()}
		n := int(rd.u16())
		size := 2
		if wide {
			size = 4
		}
		if rd.err != nil || n > rd.left()/size {
			return fmt.Errorf("%w: iref %v reference count", ErrMalformed, r.Type)
		}
		ref.To = make([]uint32, 0, n)
		for range n {
			ref.To = append(ref.To, id())
		}
		if rd.err != nil {
			return rd.err
		}
		m.Refs = append(m.Refs, ref)
	}
	return nil
}

// Encode returns the meta box with its iloc and iprp re-encoded and every
// other child as it was.
func (m *Meta) Encode() ([]byte, error) {
	body := binary.BigEndian.AppendUint32(nil, uint32(m.Version)<<24|m.Flags&0xFFFFFF)
	for _, c := range m.children {
		switch {
		case c.raw != nil:
			body = append(body, c.raw...)
		case c.typ == T("iloc"):
			b, err := m.Loc.encode()
			if err != nil {
				return nil, err
			}
			body = append(body, b...)
		case c.typ == T("iprp"):
			body = append(body, m.Prop.encode()...)
		}
	}
	return append(AppendHeader(nil, T("meta"), len(body), m.large), body...), nil
}

// ILoc is an item location box (ISO/IEC 14496-12 8.11.3).
type ILoc struct {
	Version                                           uint8
	Flags                                             uint32
	OffsetSize, LengthSize, BaseOffsetSize, IndexSize uint8
	Items                                             []LocItem
	large                                             bool
}

// LocItem is one item's location.
type LocItem struct {
	ID                 uint32
	ConstructionMethod uint8
	DataRefIndex       uint16
	BaseOffset         uint64
	Extents            []Extent
}

// Extent is one piece of an item's data.
type Extent struct {
	Index, Offset, Length uint64
}

func validSize(s uint8) bool { return s == 0 || s == 4 || s == 8 }

func parseILoc(b []byte, large bool) (*ILoc, error) {
	rd := &reader{b: b}
	vf := rd.u32()
	l := &ILoc{Version: uint8(vf >> 24), Flags: vf & 0xFFFFFF, large: large}
	if l.Version > 2 {
		return nil, fmt.Errorf("%w: iloc version %d", ErrMalformed, l.Version)
	}
	s := rd.u8()
	l.OffsetSize, l.LengthSize = s>>4, s&15
	s = rd.u8()
	l.BaseOffsetSize = s >> 4
	if l.Version > 0 {
		l.IndexSize = s & 15
	}
	for _, sz := range []uint8{l.OffsetSize, l.LengthSize, l.BaseOffsetSize, l.IndexSize} {
		if !validSize(sz) {
			return nil, fmt.Errorf("%w: iloc field size %d", ErrMalformed, sz)
		}
	}
	var count uint32
	if l.Version < 2 {
		count = uint32(rd.u16())
	} else {
		count = rd.u32()
	}
	// Each item takes at least 6 bytes: bound the count before allocating.
	if rd.err != nil || int(count) > rd.left()/6 {
		return nil, fmt.Errorf("%w: iloc item count %d", ErrMalformed, count)
	}
	l.Items = make([]LocItem, 0, count)
	extentSize := int(l.OffsetSize) + int(l.LengthSize)
	if l.Version > 0 {
		extentSize += int(l.IndexSize)
	}
	for range count {
		var it LocItem
		if l.Version < 2 {
			it.ID = uint32(rd.u16())
		} else {
			it.ID = rd.u32()
		}
		if l.Version > 0 {
			it.ConstructionMethod = uint8(rd.u16() & 15)
		}
		it.DataRefIndex = rd.u16()
		it.BaseOffset = rd.uint(l.BaseOffsetSize)
		n := rd.u16()
		if rd.err != nil {
			return nil, rd.err
		}
		if extentSize > 0 && int(n) > rd.left()/extentSize {
			return nil, fmt.Errorf("%w: iloc extent count %d", ErrMalformed, n)
		}
		it.Extents = make([]Extent, 0, n)
		for range n {
			var e Extent
			if l.Version > 0 {
				e.Index = rd.uint(l.IndexSize)
			}
			e.Offset = rd.uint(l.OffsetSize)
			e.Length = rd.uint(l.LengthSize)
			it.Extents = append(it.Extents, e)
		}
		if rd.err != nil {
			return nil, rd.err
		}
		l.Items = append(l.Items, it)
	}
	return l, rd.err
}

// errFieldTooSmall is a value that no longer fits its field.
var errFieldTooSmall = fmt.Errorf("an iloc value doesn't fit its field")

// ErrFieldTooSmall reports whether err is a value outgrowing its field.
func ErrFieldTooSmall(err error) bool { return err == errFieldTooSmall }

func (l *ILoc) encode() ([]byte, error) {
	b := binary.BigEndian.AppendUint32(nil, uint32(l.Version)<<24|l.Flags&0xFFFFFF)
	b = append(b, l.OffsetSize<<4|l.LengthSize, l.BaseOffsetSize<<4|l.IndexSize)
	if l.Version < 2 {
		b = binary.BigEndian.AppendUint16(b, uint16(len(l.Items)))
	} else {
		b = binary.BigEndian.AppendUint32(b, uint32(len(l.Items)))
	}
	var ok bool
	for _, it := range l.Items {
		if l.Version < 2 {
			b = binary.BigEndian.AppendUint16(b, uint16(it.ID))
		} else {
			b = binary.BigEndian.AppendUint32(b, it.ID)
		}
		if l.Version > 0 {
			b = binary.BigEndian.AppendUint16(b, uint16(it.ConstructionMethod&15))
		}
		b = binary.BigEndian.AppendUint16(b, it.DataRefIndex)
		if b, ok = appendUint(b, l.BaseOffsetSize, it.BaseOffset); !ok {
			return nil, errFieldTooSmall
		}
		b = binary.BigEndian.AppendUint16(b, uint16(len(it.Extents)))
		for _, e := range it.Extents {
			if l.Version > 0 {
				if b, ok = appendUint(b, l.IndexSize, e.Index); !ok {
					return nil, errFieldTooSmall
				}
			}
			if b, ok = appendUint(b, l.OffsetSize, e.Offset); !ok {
				return nil, errFieldTooSmall
			}
			if b, ok = appendUint(b, l.LengthSize, e.Length); !ok {
				return nil, errFieldTooSmall
			}
		}
	}
	return append(AppendHeader(nil, T("iloc"), len(b), l.large), b...), nil
}

// Item returns the location of item id.
func (l *ILoc) Item(id uint32) (*LocItem, bool) {
	for i := range l.Items {
		if l.Items[i].ID == id {
			return &l.Items[i], true
		}
	}
	return nil, false
}

// IPrp is an item properties box (ISO/IEC 23008-12 9.3): the property
// container and the associations.
type IPrp struct {
	// Props are ipco's property boxes, whole, in order: association
	// index i (1-based) is Props[i-1].
	Props     []Prop
	Assoc     []*IPMA
	ipcoLarge bool
	large     bool
	order     []Type // iprp's children in their order
	others    [][]byte
}

// Prop is one property box, kept whole.
type Prop struct {
	Type Type
	Raw  []byte
}

// IPMA is an item property association box.
type IPMA struct {
	Version uint8
	Flags   uint32
	Entries []AssocEntry
	large   bool
}

// AssocEntry is one item's properties.
type AssocEntry struct {
	ItemID uint32
	Props  []PropRef
}

// PropRef is one association: a 1-based index into Props (0 means none).
type PropRef struct {
	Essential bool
	Index     uint16
}

func parseIPrp(b []byte, large bool) (*IPrp, error) {
	p := &IPrp{large: large}
	kids, err := ReadBoxes(bytes.NewReader(b), 0, int64(len(b)))
	if err != nil {
		return nil, err
	}
	sawIpco := false
	for _, k := range kids {
		kb := b[k.Body():k.End()]
		switch {
		case k.Type == T("ipco") && !sawIpco:
			sawIpco = true
			p.ipcoLarge = k.Large()
			props, err := ReadBoxes(bytes.NewReader(kb), 0, int64(len(kb)))
			if err != nil {
				return nil, err
			}
			for _, pb := range props {
				p.Props = append(p.Props, Prop{Type: pb.Type, Raw: explicitSize(kb[pb.Offset:pb.End()])})
			}
		case k.Type == T("ipma"):
			a, err := parseIPMA(kb, k.Large())
			if err != nil {
				return nil, err
			}
			p.Assoc = append(p.Assoc, a)
		default:
			p.others = append(p.others, explicitSize(b[k.Offset:k.End()]))
		}
		p.order = append(p.order, k.Type)
	}
	if !sawIpco {
		return nil, fmt.Errorf("%w: iprp without ipco", ErrMalformed)
	}
	for _, a := range p.Assoc {
		for _, e := range a.Entries {
			for _, r := range e.Props {
				if int(r.Index) > len(p.Props) {
					return nil, fmt.Errorf("%w: property index %d of %d", ErrMalformed, r.Index, len(p.Props))
				}
			}
		}
	}
	return p, nil
}

func parseIPMA(b []byte, large bool) (*IPMA, error) {
	rd := &reader{b: b}
	vf := rd.u32()
	a := &IPMA{Version: uint8(vf >> 24), Flags: vf & 0xFFFFFF, large: large}
	count := rd.u32()
	if rd.err != nil || int(count) > rd.left()/3 {
		return nil, fmt.Errorf("%w: ipma entry count %d", ErrMalformed, count)
	}
	a.Entries = make([]AssocEntry, 0, count)
	wide := a.Flags&1 != 0
	for range count {
		var e AssocEntry
		if a.Version < 1 {
			e.ItemID = uint32(rd.u16())
		} else {
			e.ItemID = rd.u32()
		}
		n := rd.u8()
		if rd.err != nil {
			return nil, rd.err
		}
		e.Props = make([]PropRef, 0, n)
		for range n {
			if wide {
				v := rd.u16()
				e.Props = append(e.Props, PropRef{Essential: v&0x8000 != 0, Index: v & 0x7FFF})
			} else {
				v := rd.u8()
				e.Props = append(e.Props, PropRef{Essential: v&0x80 != 0, Index: uint16(v & 0x7F)})
			}
		}
		if rd.err != nil {
			return nil, rd.err
		}
		a.Entries = append(a.Entries, e)
	}
	return a, rd.err
}

func (a *IPMA) encode() []byte {
	b := binary.BigEndian.AppendUint32(nil, uint32(a.Version)<<24|a.Flags&0xFFFFFF)
	b = binary.BigEndian.AppendUint32(b, uint32(len(a.Entries)))
	wide := a.Flags&1 != 0
	for _, e := range a.Entries {
		if a.Version < 1 {
			b = binary.BigEndian.AppendUint16(b, uint16(e.ItemID))
		} else {
			b = binary.BigEndian.AppendUint32(b, e.ItemID)
		}
		b = append(b, uint8(len(e.Props)))
		for _, r := range e.Props {
			if wide {
				v := r.Index & 0x7FFF
				if r.Essential {
					v |= 0x8000
				}
				b = binary.BigEndian.AppendUint16(b, v)
			} else {
				v := uint8(r.Index & 0x7F)
				if r.Essential {
					v |= 0x80
				}
				b = append(b, v)
			}
		}
	}
	return append(AppendHeader(nil, T("ipma"), len(b), a.large), b...)
}

func (p *IPrp) encode() []byte {
	var ipco []byte
	for _, pr := range p.Props {
		ipco = append(ipco, pr.Raw...)
	}
	var body []byte
	assoc, other, wroteIpco := 0, 0, false
	for _, t := range p.order {
		switch {
		case t == T("ipco") && !wroteIpco:
			// Even without properties: an empty ipco is still the ipco.
			body = append(body, AppendHeader(nil, T("ipco"), len(ipco), p.ipcoLarge)...)
			body = append(body, ipco...)
			wroteIpco = true
		case t == T("ipma") && assoc < len(p.Assoc):
			body = append(body, p.Assoc[assoc].encode()...)
			assoc++
		case other < len(p.others):
			body = append(body, p.others[other]...)
			other++
		}
	}
	return append(AppendHeader(nil, T("iprp"), len(body), p.large), body...)
}

// Entry returns the association entry of item id and its ipma.
func (p *IPrp) Entry(id uint32) (*IPMA, *AssocEntry) {
	for _, a := range p.Assoc {
		for i := range a.Entries {
			if a.Entries[i].ItemID == id {
				return a, &a.Entries[i]
			}
		}
	}
	return nil, nil
}

// UsesOf counts the associations, over every item, with property index i.
func (p *IPrp) UsesOf(i uint16) int {
	n := 0
	for _, a := range p.Assoc {
		for _, e := range a.Entries {
			for _, r := range e.Props {
				if r.Index == i {
					n++
				}
			}
		}
	}
	return n
}

// explicitSize returns a box kept as raw bytes with a size-0 header ("to
// the end of the container") rewritten to its actual size: written back
// with something after it, it would otherwise swallow that.
func explicitSize(raw []byte) []byte {
	if len(raw) < 8 || binary.BigEndian.Uint32(raw) != 0 || len(raw) > 0xFFFFFFFF {
		return raw
	}
	out := bytes.Clone(raw)
	binary.BigEndian.PutUint32(out, uint32(len(raw)))
	return out
}
