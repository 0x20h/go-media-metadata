// Package testbuild makes synthetic HEIF files for tests: the box
// structure of a real file (ISO/IEC 23008-12) around random bytes as
// "image data", so a test can check that a write leaves every item's data
// byte for byte as it was.
package testbuild

import (
	"encoding/binary"
)

// Options shape a file.
type Options struct {
	// IlocVersion is 0, 1 or 2; OffsetSize, LengthSize and BaseOffsetSize
	// are 4 or 8 (0 for BaseOffsetSize means none).
	IlocVersion                            uint8
	OffsetSize, LengthSize, BaseOffsetSize uint8
	// IpmaVersion is 0 or 1; IpmaWide uses 15-bit property indices.
	IpmaVersion uint8
	IpmaWide    bool
	// Grid makes the primary item a grid of four hidden tiles.
	Grid bool
	// Thumbnail adds a thumbnail item.
	Thumbnail bool
	// GainMap adds an auxiliary image (an HDR gain map, auxl to the
	// primary).
	GainMap bool
	// Irot is the primary item's irot in anticlockwise quarter turns, or
	// -1 for none; Imir its imir axis, or -1 for none.
	Irot, Imir int
	// SharedIrot associates the primary's irot with the thumbnail and the
	// gain map too, as iPhones do.
	SharedIrot bool
	// ExifOrientation adds an Exif item whose IFD0 has this Orientation
	// (0: an Exif item without the tag; -1: no Exif item). ExifBigEndian
	// writes its TIFF data big-endian; ExifInIdat puts it in idat (needs
	// IlocVersion ≥ 1).
	ExifOrientation int
	ExifBigEndian   bool
	ExifInIdat      bool
	// MetaAfterMdat puts the meta box after the media data.
	MetaAfterMdat bool
	// ExtraProps adds that many descriptive properties before the others,
	// to push property indices past 127.
	ExtraProps int
	// Seed seeds the random item data.
	Seed uint64
}

// Default is a plain iPhone-like layout: a grid, a thumbnail, an irot of
// 90° clockwise, an Exif item with Orientation 6, iloc version 1.
func Default() Options {
	return Options{IlocVersion: 1, OffsetSize: 4, LengthSize: 4, Grid: true, Thumbnail: true,
		Irot: 3, Imir: -1, ExifOrientation: 6, Seed: 1}
}

// Item ids.
const (
	Primary   = 1
	Thumbnail = 10
	Exif      = 20
	GainMap   = 30
)

type item struct {
	id     uint32
	typ    string
	data   []byte
	idat   bool
	hidden bool
}

// Build returns the file and every item's data by id.
func Build(o Options) ([]byte, map[uint32][]byte) {
	// A small xorshift generator: the bytes only need to differ per seed.
	state := o.Seed*0x9e3779b97f4a7c15 | 1
	random := func(n int) []byte {
		b := make([]byte, n)
		for i := range b {
			state ^= state << 13
			state ^= state >> 7
			state ^= state << 17
			b[i] = byte(state)
		}
		return b
	}
	var items []item
	if o.Grid {
		// ImageGrid: version, flags, rows-1, columns-1, 16-bit width, height.
		items = append(items, item{id: Primary, typ: "grid", data: []byte{0, 0, 1, 1, 0x02, 0x00, 0x01, 0x80}})
		for i := range 4 {
			items = append(items, item{id: uint32(2 + i), typ: "hvc1", data: random(300 + 37*i), hidden: true})
		}
	} else {
		items = append(items, item{id: Primary, typ: "hvc1", data: random(1200)})
	}
	if o.Thumbnail {
		items = append(items, item{id: Thumbnail, typ: "hvc1", data: random(200)})
	}
	if o.GainMap {
		items = append(items, item{id: GainMap, typ: "hvc1", data: random(150), hidden: true})
	}
	if o.ExifOrientation >= 0 {
		items = append(items, item{id: Exif, typ: "Exif", data: exifData(o.ExifOrientation, o.ExifBigEndian), idat: o.ExifInIdat})
	}
	data := map[uint32][]byte{}
	for _, it := range items {
		data[it.id] = it.data
	}

	ftyp := box("ftyp", []byte("heic\x00\x00\x00\x00mif1heicmiaf"))
	// Two passes: the meta box's size doesn't depend on the offsets in it.
	meta := buildMeta(o, items, 0)
	mdatBody := []byte{}
	for _, it := range items {
		if !it.idat {
			mdatBody = append(mdatBody, it.data...)
		}
	}
	var dataStart int
	if o.MetaAfterMdat {
		dataStart = len(ftyp) + 8
	} else {
		dataStart = len(ftyp) + len(meta) + 8
	}
	meta = buildMeta(o, items, dataStart)
	mdat := box("mdat", mdatBody)
	var file []byte
	file = append(file, ftyp...)
	if o.MetaAfterMdat {
		file = append(file, mdat...)
		file = append(file, meta...)
	} else {
		file = append(file, meta...)
		file = append(file, mdat...)
	}
	return file, data
}

func box(t string, body []byte) []byte {
	b := binary.BigEndian.AppendUint32(nil, uint32(len(body)+8))
	b = append(b, t...)
	return append(b, body...)
}

func fullBox(t string, version uint8, flags uint32, body []byte) []byte {
	return box(t, append(binary.BigEndian.AppendUint32(nil, uint32(version)<<24|flags), body...))
}

func putUint(b []byte, size uint8, v uint64) []byte {
	switch size {
	case 4:
		return binary.BigEndian.AppendUint32(b, uint32(v))
	case 8:
		return binary.BigEndian.AppendUint64(b, v)
	}
	return b
}

func buildMeta(o Options, items []item, dataStart int) []byte {
	be := binary.BigEndian
	var body []byte
	body = append(body, fullBox("hdlr", 0, 0, append(append(make([]byte, 4), "pict"...), make([]byte, 13)...))...)
	body = append(body, fullBox("pitm", 0, 0, be.AppendUint16(nil, Primary))...)

	iinf := be.AppendUint16(nil, uint16(len(items)))
	for _, it := range items {
		var flags uint32
		if it.hidden {
			flags = 1
		}
		e := be.AppendUint16(nil, uint16(it.id))
		e = be.AppendUint16(e, 0)
		e = append(e, it.typ...)
		e = append(e, 0)
		iinf = append(iinf, fullBox("infe", 2, flags, e)...)
	}
	body = append(body, fullBox("iinf", 0, 0, iinf)...)

	var iref []byte
	if o.Grid {
		r := be.AppendUint16(nil, Primary)
		r = be.AppendUint16(r, 4)
		for i := range 4 {
			r = be.AppendUint16(r, uint16(2+i))
		}
		iref = append(iref, box("dimg", r)...)
	}
	if o.Thumbnail {
		iref = append(iref, box("thmb", be.AppendUint16(be.AppendUint16(be.AppendUint16(nil, Thumbnail), 1), Primary))...)
	}
	if o.GainMap {
		iref = append(iref, box("auxl", be.AppendUint16(be.AppendUint16(be.AppendUint16(nil, GainMap), 1), Primary))...)
	}
	if o.ExifOrientation >= 0 {
		iref = append(iref, box("cdsc", be.AppendUint16(be.AppendUint16(be.AppendUint16(nil, Exif), 1), Primary))...)
	}
	body = append(body, fullBox("iref", 0, 0, iref)...)

	// iloc.
	v := o.IlocVersion
	iloc := []byte{o.OffsetSize<<4 | o.LengthSize, o.BaseOffsetSize << 4}
	if v < 2 {
		iloc = be.AppendUint16(iloc, uint16(len(items)))
	} else {
		iloc = be.AppendUint32(iloc, uint32(len(items)))
	}
	off, idatOff := uint64(dataStart), uint64(0)
	for _, it := range items {
		if v < 2 {
			iloc = be.AppendUint16(iloc, uint16(it.id))
		} else {
			iloc = be.AppendUint32(iloc, it.id)
		}
		where := off
		if v > 0 {
			method := uint16(0)
			if it.idat {
				method = 1
				where = idatOff
			}
			iloc = be.AppendUint16(iloc, method)
		}
		iloc = be.AppendUint16(iloc, 0)
		var base uint64
		if o.BaseOffsetSize > 0 {
			base = where
			where = 0
		}
		iloc = putUint(iloc, o.BaseOffsetSize, base)
		iloc = be.AppendUint16(iloc, 1)
		iloc = putUint(iloc, o.OffsetSize, where)
		iloc = putUint(iloc, o.LengthSize, uint64(len(it.data)))
		if it.idat {
			idatOff += uint64(len(it.data))
		} else {
			off += uint64(len(it.data))
		}
	}
	body = append(body, fullBox("iloc", v, 0, iloc)...)
	var idat []byte
	for _, it := range items {
		if it.idat {
			idat = append(idat, it.data...)
		}
	}
	if idat != nil {
		body = append(body, box("idat", idat)...)
	}

	// iprp: ipco properties, then ipma.
	var props [][]byte
	add := func(p []byte) uint16 { props = append(props, p); return uint16(len(props)) }
	for i := range o.ExtraProps {
		add(fullBox("ispe", 0, 0, be.AppendUint32(be.AppendUint32(nil, uint32(16+i)), 16)))
	}
	hvcC := add(box("hvcC", []byte{1, 1, 0x60, 0, 0, 0, 0xb0, 0, 0, 0, 0, 0, 0x5d, 0xf0, 0, 0xfc, 0xfd, 0xf8, 0xf8, 0, 0, 0x0f, 0}))
	tileSpe := add(fullBox("ispe", 0, 0, be.AppendUint32(be.AppendUint32(nil, 512), 512)))
	gridSpe := add(fullBox("ispe", 0, 0, be.AppendUint32(be.AppendUint32(nil, 1024), 768)))
	auxC := add(fullBox("auxC", 0, 0, []byte("urn:com:apple:photo:2020:aux:hdrgainmap\x00")))
	var irot, imir uint16
	if o.Irot >= 0 {
		irot = add(box("irot", []byte{byte(o.Irot & 3)}))
	}
	if o.Imir >= 0 {
		imir = add(box("imir", []byte{byte(o.Imir & 1)}))
	}
	type ref struct {
		idx       uint16
		essential bool
	}
	assoc := map[uint32][]ref{}
	transforms := func() []ref {
		var r []ref
		if irot != 0 {
			r = append(r, ref{irot, true})
		}
		if imir != 0 {
			r = append(r, ref{imir, true})
		}
		return r
	}
	if o.Grid {
		assoc[Primary] = append([]ref{{gridSpe, false}}, transforms()...)
		for i := range 4 {
			assoc[uint32(2+i)] = []ref{{hvcC, true}, {tileSpe, false}}
		}
	} else {
		assoc[Primary] = append([]ref{{hvcC, true}, {gridSpe, false}}, transforms()...)
	}
	if o.Thumbnail {
		r := []ref{{hvcC, true}, {tileSpe, false}}
		if o.SharedIrot && irot != 0 {
			r = append(r, ref{irot, true})
		}
		assoc[Thumbnail] = r
	}
	if o.GainMap {
		r := []ref{{hvcC, true}, {tileSpe, false}, {auxC, false}}
		if o.SharedIrot && irot != 0 {
			r = append(r, ref{irot, true})
		}
		assoc[GainMap] = r
	}
	var ipco []byte
	for _, p := range props {
		ipco = append(ipco, p...)
	}
	var flags uint32
	if o.IpmaWide {
		flags = 1
	}
	var ids []uint32
	for _, it := range items {
		if _, ok := assoc[it.id]; ok {
			ids = append(ids, it.id)
		}
	}
	ipma := be.AppendUint32(nil, uint32(len(ids)))
	for _, id := range ids {
		if o.IpmaVersion < 1 {
			ipma = be.AppendUint16(ipma, uint16(id))
		} else {
			ipma = be.AppendUint32(ipma, id)
		}
		ipma = append(ipma, byte(len(assoc[id])))
		for _, r := range assoc[id] {
			if o.IpmaWide {
				x := r.idx
				if r.essential {
					x |= 0x8000
				}
				ipma = be.AppendUint16(ipma, x)
			} else {
				x := byte(r.idx)
				if r.essential {
					x |= 0x80
				}
				ipma = append(ipma, x)
			}
		}
	}
	iprp := append(box("ipco", ipco), fullBox("ipma", o.IpmaVersion, flags, ipma)...)
	body = append(body, box("iprp", iprp)...)
	return fullBox("meta", 0, 0, body)
}

// exifData is an Exif item's payload: the 4-byte offset to the TIFF
// header, "Exif\0\0", and TIFF data with IFD0 holding Make and, unless
// orientation is 0, Orientation.
func exifData(orientation int, bigEndian bool) []byte {
	var order binary.AppendByteOrder = binary.LittleEndian
	head := []byte("II*\x00")
	if bigEndian {
		order = binary.BigEndian
		head = []byte("MM\x00*")
	}
	tiff := append([]byte{}, head...)
	tiff = order.AppendUint32(tiff, 8)
	n := 1
	if orientation > 0 {
		n = 2
	}
	tiff = order.AppendUint16(tiff, uint16(n))
	makeOff := uint32(8 + 2 + 12*n + 4)
	// Make: ASCII "Test\0", stored after the IFD.
	tiff = order.AppendUint16(tiff, 0x010F)
	tiff = order.AppendUint16(tiff, 2)
	tiff = order.AppendUint32(tiff, 5)
	tiff = order.AppendUint32(tiff, makeOff)
	if orientation > 0 {
		tiff = order.AppendUint16(tiff, 0x0112)
		tiff = order.AppendUint16(tiff, 3)
		tiff = order.AppendUint32(tiff, 1)
		tiff = order.AppendUint16(tiff, uint16(orientation))
		tiff = order.AppendUint16(tiff, 0)
	}
	tiff = order.AppendUint32(tiff, 0) // no next IFD
	tiff = append(tiff, "Test\x00"...)
	out := binary.BigEndian.AppendUint32(nil, 6)
	out = append(out, "Exif\x00\x00"...)
	return append(out, tiff...)
}
