package bmff

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

// ErrNoRoom is returned when an item can't be added: an id that doesn't
// fit the boxes' fields, or no iinf box to list it in.
var ErrNoRoom = fmt.Errorf("mediameta: no room for another item")

// AddItem lists a new item in iinf: an infe (version 2, or 3 for an id
// past 16 bits) of type typ with an empty name, and for a "mime" item its
// content type. The item's location and references are the caller's.
func (m *Meta) AddItem(id uint32, typ Type, contentType string) error {
	ci := m.childIndex(T("iinf"))
	if ci < 0 {
		return fmt.Errorf("%w: no iinf box", ErrNoRoom)
	}
	if _, ok := m.ItemTypes[id]; ok {
		return fmt.Errorf("%w: item %d is there", ErrMalformed, id)
	}
	raw := m.children[ci].raw
	boxes, err := ReadBoxes(bytes.NewReader(raw), 0, int64(len(raw)))
	if err != nil || len(boxes) != 1 || boxes[0].BodyLen() < 6 {
		return fmt.Errorf("%w: iinf", ErrMalformed)
	}
	body := bytes.Clone(raw[boxes[0].Body():])
	if body[0] == 0 {
		n := binary.BigEndian.Uint16(body[4:])
		if n == 0xFFFF {
			return fmt.Errorf("%w: iinf holds 65535 items", ErrNoRoom)
		}
		binary.BigEndian.PutUint16(body[4:], n+1)
	} else {
		if len(body) < 8 {
			return fmt.Errorf("%w: iinf", ErrMalformed)
		}
		binary.BigEndian.PutUint32(body[4:], binary.BigEndian.Uint32(body[4:])+1)
	}
	var e []byte
	if id <= 0xFFFF {
		e = append(e, 2, 0, 0, 0)
		e = binary.BigEndian.AppendUint16(e, uint16(id))
	} else {
		e = append(e, 3, 0, 0, 0)
		e = binary.BigEndian.AppendUint32(e, id)
	}
	e = append(e, 0, 0) // item_protection_index
	e = append(e, typ[:]...)
	e = append(e, 0) // item_name: empty
	if typ == T("mime") {
		e = append(append(e, contentType...), 0)
	}
	body = append(body, AppendHeader(nil, T("infe"), len(e), false)...)
	body = append(body, e...)
	m.children[ci].raw = append(AppendHeader(nil, T("iinf"), len(body), boxes[0].Large()), body...)
	m.ItemTypes[id] = typ
	return nil
}

// ContentType is the content type of a "mime" item's infe, read from the
// iinf box when asked (most callers never do); "" for none.
func (m *Meta) ContentType(id uint32) string {
	ci := m.childIndex(T("iinf"))
	if ci < 0 || m.ItemTypes[id] != T("mime") {
		return ""
	}
	raw := m.children[ci].raw
	boxes, err := ReadBoxes(bytes.NewReader(raw), 0, int64(len(raw)))
	if err != nil || len(boxes) != 1 || boxes[0].BodyLen() < 4 {
		return ""
	}
	b := raw[boxes[0].Body():]
	start := int64(6)
	if b[0] != 0 {
		start = 8
	}
	if int64(len(b)) < start {
		return ""
	}
	entries, err := ReadBoxes(bytes.NewReader(b), start, int64(len(b)))
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.Type != T("infe") {
			continue
		}
		rd := &reader{b: b[e.Body():e.End()]}
		v := rd.u8()
		rd.need(3)
		rd.off += 3
		var got uint32
		switch v {
		case 2:
			got = uint32(rd.u16())
		case 3:
			got = rd.u32()
		default:
			continue
		}
		rd.u16() // item_protection_index
		if rd.err != nil || got != id || !rd.need(4) {
			continue
		}
		// The type, then item_name and content_type, each null-terminated.
		rest := rd.b[rd.off+4:]
		i := bytes.IndexByte(rest, 0)
		if i < 0 {
			return ""
		}
		ct := rest[i+1:]
		if j := bytes.IndexByte(ct, 0); j >= 0 {
			ct = ct[:j]
		}
		return string(ct)
	}
	return ""
}

// AddRef adds a reference of type typ from one item to others, to the
// iref box, or to a new one after iinf.
func (m *Meta) AddRef(typ Type, from uint32, to []uint32) error {
	if len(to) == 0 || len(to) > 0xFFFF {
		return fmt.Errorf("%w: %d references", ErrNoRoom, len(to))
	}
	ci := m.childIndex(T("iref"))
	var body []byte
	large := false
	if ci < 0 {
		body = []byte{0, 0, 0, 0}
	} else {
		raw := m.children[ci].raw
		boxes, err := ReadBoxes(bytes.NewReader(raw), 0, int64(len(raw)))
		if err != nil || len(boxes) != 1 || boxes[0].BodyLen() < 4 {
			return fmt.Errorf("%w: iref", ErrMalformed)
		}
		body = bytes.Clone(raw[boxes[0].Body():])
		large = boxes[0].Large()
	}
	wide := body[0] != 0
	put := func(b []byte, id uint32) ([]byte, bool) {
		if wide {
			return binary.BigEndian.AppendUint32(b, id), true
		}
		if id > 0xFFFF {
			return b, false
		}
		return binary.BigEndian.AppendUint16(b, uint16(id)), true
	}
	r, ok := put(nil, from)
	r = binary.BigEndian.AppendUint16(r, uint16(len(to)))
	for _, id := range to {
		var fits bool
		r, fits = put(r, id)
		ok = ok && fits
	}
	if !ok {
		return fmt.Errorf("%w: an item id past 16 bits in a version 0 iref", ErrNoRoom)
	}
	body = append(body, AppendHeader(nil, typ, len(r), false)...)
	body = append(body, r...)
	box := append(AppendHeader(nil, T("iref"), len(body), large), body...)
	if ci >= 0 {
		m.children[ci].raw = box
	} else {
		at := m.childIndex(T("iinf"))
		if at < 0 {
			at = len(m.children)
		} else {
			at++
		}
		m.children = append(m.children[:at], append([]child{{typ: T("iref"), raw: box}}, m.children[at:]...)...)
	}
	m.Refs = append(m.Refs, ItemRef{Type: typ, From: from, To: append([]uint32(nil), to...)})
	return nil
}

func (m *Meta) childIndex(t Type) int {
	for i, c := range m.children {
		if c.typ == t {
			return i
		}
	}
	return -1
}
