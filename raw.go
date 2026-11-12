package mediameta

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/0x20h/go-media-metadata/internal/bmff"
)

// ErrNotRAW is returned for a file that is neither TIFF-based (DNG, CR2,
// NEF, ARW, ORF, RW2, TIFF) nor a CR3.
var ErrNotRAW = errors.New("not a camera RAW or TIFF file")

// Tags of TIFF-based RAW files read here.
const (
	tagXMP        = 0x02BC // XMLPacket
	tagJpgFromRaw = 0x002E // Panasonic RW2: a JPEG holding the Exif
)

// maxRAWValue caps an IFD value read from a RAW file: the fields read
// here are short; image data, strips and maker notes are never read.
const maxRAWValue = 64 << 10

// maxRAWXMP caps an XMP packet read from a RAW file.
const maxRAWXMP = 4 << 20

// The uuid boxes of a Canon CR3: the one in moov holding the TIFF
// metadata (CMT1 to CMT4), and the top-level one holding XMP.
var (
	cr3MetaUUID = [16]byte{0x85, 0xc0, 0xb6, 0x87, 0x82, 0x0f, 0x11, 0xe0, 0x81, 0x11, 0xf4, 0xce, 0x46, 0x2b, 0x6a, 0x48}
	cr3XMPUUID  = [16]byte{0xbe, 0x7a, 0xcf, 0xcb, 0x97, 0xa9, 0x42, 0xe8, 0x9c, 0x71, 0x99, 0x94, 0x91, 0xe3, 0xaf, 0xac}
)

// rawTIFF reads the IFDs of a TIFF-based file through its ReaderAt, one
// IFD and its short values at a time.
type rawTIFF struct {
	tiffReader
	r     io.ReaderAt
	size  int64
	first uint32
}

// openRAWTIFF reads the header of a TIFF-based file: TIFF's own, or the
// variants Olympus ORF ("IIRO", "IIRS", "MMOR") and Panasonic RW2
// ("IIU\0") use.
func openRAWTIFF(r io.ReaderAt, size int64) (*rawTIFF, bool) {
	var h [8]byte
	if size < 16 {
		return nil, false
	}
	if _, err := r.ReadAt(h[:], 0); err != nil {
		return nil, false
	}
	var order binary.ByteOrder
	switch string(h[:4]) {
	case "II*\x00", "IIRO", "IIRS", "IIU\x00":
		order = binary.LittleEndian
	case "MM\x00*", "MMOR":
		order = binary.BigEndian
	default:
		return nil, false
	}
	return &rawTIFF{tiffReader: tiffReader{order: order}, r: r, size: size, first: order.Uint32(h[4:])}, true
}

// ifd reads the IFD at off as tiffReader.ifd does, values longer than
// maxRAWValue left out; nil for one outside the file. at maps each tag to
// the file offset of its entry.
func (t *rawTIFF) ifdAt(off uint32) (map[uint16]field, map[uint16]int64) {
	if off < 8 || int64(off)+2 > t.size {
		return nil, nil
	}
	var nb [2]byte
	if _, err := t.r.ReadAt(nb[:], int64(off)); err != nil {
		return nil, nil
	}
	n := min(int(t.order.Uint16(nb[:])), maxIFDEntries, int((t.size-int64(off)-2)/12))
	entries := make([]byte, 12*n)
	if _, err := t.r.ReadAt(entries, int64(off)+2); err != nil {
		return nil, nil
	}
	out := make(map[uint16]field, n)
	at := make(map[uint16]int64, n)
	for i := range n {
		e := entries[12*i : 12*i+12]
		tag, typ, count := t.order.Uint16(e), t.order.Uint16(e[2:]), t.order.Uint32(e[4:])
		if _, dup := out[tag]; dup {
			continue
		}
		size := uint64(typeSize(typ)) * uint64(count)
		if size == 0 || size > maxRAWValue {
			continue
		}
		var v []byte
		if size <= 4 {
			v = e[8 : 8+size]
		} else {
			p := uint64(t.order.Uint32(e[8:]))
			if p+size > uint64(t.size) {
				continue
			}
			v = make([]byte, size)
			if _, err := t.r.ReadAt(v, int64(p)); err != nil {
				continue
			}
		}
		out[tag] = field{typ: typ, count: int(count), value: v}
		at[tag] = int64(off) + 2 + 12*int64(i)
	}
	return out, at
}

func (t *rawTIFF) ifd(off uint32) map[uint16]field {
	m, _ := t.ifdAt(off)
	return m
}

// readRAWTIFFExif reads the Exif of a TIFF-based file. A Panasonic RW2,
// whose IFD0 points to no Exif IFD, has its Exif in the JPEG it embeds.
func readRAWTIFFExif(t *rawTIFF) (Exif, error) {
	x := t.exif(t.ifd, t.first)
	ifd0 := t.ifd(t.first)
	if ifd0 == nil {
		return Exif{}, fmt.Errorf("%w: IFD0 at %d outside the file", ErrMalformed, t.first)
	}
	if _, ok := ifd0[tagExifIFD]; !ok {
		if j, ok := t.jpgFromRaw(); ok {
			if jx, err := readJPEGExif(j, j.Size()); err == nil {
				o := x.Orientation
				x = jx
				if o != 0 {
					x.Orientation = o // IFD0's is the RAW's own
				}
			}
		}
	}
	return x, nil
}

// jpgFromRaw is the JPEG an RW2 embeds, as a section of the file.
func (t *rawTIFF) jpgFromRaw() (*io.SectionReader, bool) {
	var nb [2]byte
	if _, err := t.r.ReadAt(nb[:], int64(t.first)); err != nil {
		return nil, false
	}
	n := min(int(t.order.Uint16(nb[:])), maxIFDEntries)
	entries := make([]byte, 12*n)
	if _, err := t.r.ReadAt(entries, int64(t.first)+2); err != nil {
		return nil, false
	}
	for i := range n {
		e := entries[12*i:]
		if t.order.Uint16(e) != tagJpgFromRaw {
			continue
		}
		size := uint64(typeSize(t.order.Uint16(e[2:]))) * uint64(t.order.Uint32(e[4:]))
		at := uint64(t.order.Uint32(e[8:]))
		if size <= 4 || at+size > uint64(t.size) {
			return nil, false
		}
		return io.NewSectionReader(t.r, int64(at), int64(size)), true
	}
	return nil, false
}

// xmpAt is where IFD0's XMP packet is in the file and how long; ok is
// false without one.
func (t *rawTIFF) xmpAt() (at, n int64, ok bool) {
	var nb [2]byte
	if _, err := t.r.ReadAt(nb[:], int64(t.first)); err != nil {
		return 0, 0, false
	}
	cnt := min(int(t.order.Uint16(nb[:])), maxIFDEntries)
	entries := make([]byte, 12*cnt)
	if _, err := t.r.ReadAt(entries, int64(t.first)+2); err != nil {
		return 0, 0, false
	}
	for i := range cnt {
		e := entries[12*i:]
		if t.order.Uint16(e) != tagXMP {
			continue
		}
		size := uint64(typeSize(t.order.Uint16(e[2:]))) * uint64(t.order.Uint32(e[4:]))
		if size <= 4 || size > maxRAWXMP {
			return 0, 0, false
		}
		p := uint64(t.order.Uint32(e[8:]))
		if p+size > uint64(t.size) {
			return 0, 0, false
		}
		return int64(p), int64(size), true
	}
	return 0, 0, false
}

// cr3 is a Canon CR3's metadata boxes: CMT1 (IFD0), CMT2 (the Exif IFD)
// and CMT4 (the GPS IFD), each a TIFF structure of its own, and its XMP.
type cr3 struct {
	cmt    map[bmff.Type]bmff.Box
	xmp    bmff.Box
	hasXMP bool
	r      io.ReaderAt
	size   int64
}

// openCR3 finds a CR3's metadata boxes; ok is false for a file that isn't
// one (its ftyp brand isn't "crx ").
func openCR3(r io.ReaderAt, size int64) (*cr3, bool, error) {
	if !startsWithFtyp(r, size) || size < 16 {
		return nil, false, nil
	}
	var brand [4]byte
	if _, err := r.ReadAt(brand[:], 8); err != nil || string(brand[:]) != "crx " {
		return nil, false, nil
	}
	top, err := bmff.ReadBoxes(r, 0, size)
	if err != nil {
		return nil, true, err
	}
	c := &cr3{cmt: map[bmff.Type]bmff.Box{}, r: r, size: size}
	// bmff counts a uuid box's 16-byte extended type in its header: it
	// ends where the body starts.
	uuidOf := func(b bmff.Box) ([16]byte, bool) {
		var u [16]byte
		if b.Type != bmff.T("uuid") || b.HeaderLen < 24 {
			return u, false
		}
		_, err := r.ReadAt(u[:], b.Body()-16)
		return u, err == nil
	}
	for _, b := range top {
		if u, ok := uuidOf(b); ok && u == cr3XMPUUID {
			c.xmp, c.hasXMP = b, true
		}
		if b.Type != bmff.T("moov") {
			continue
		}
		kids, err := bmff.ReadBoxes(r, b.Body(), b.End())
		if err != nil {
			return nil, true, err
		}
		for _, k := range kids {
			if u, ok := uuidOf(k); !ok || u != cr3MetaUUID {
				continue
			}
			cmts, err := bmff.ReadBoxes(r, k.Body(), k.End())
			if err != nil {
				return nil, true, err
			}
			for _, m := range cmts {
				switch m.Type {
				case bmff.T("CMT1"), bmff.T("CMT2"), bmff.T("CMT4"):
					c.cmt[m.Type] = m
				}
			}
		}
	}
	return c, true, nil
}

// tiff reads a CMT box: TIFF data, IFD first.
func (c *cr3) tiff(name string) ([]byte, int64, bool) {
	b, ok := c.cmt[bmff.T(name)]
	if !ok || b.BodyLen() > maxRAWValue*16 {
		return nil, 0, false
	}
	data := make([]byte, b.BodyLen())
	if _, err := c.r.ReadAt(data, b.Body()); err != nil {
		return nil, 0, false
	}
	return data, b.Body(), true
}

// exif reads a CR3's Exif from its CMT boxes.
func (c *cr3) exif() Exif {
	var x Exif
	if data, _, ok := c.tiff("CMT1"); ok {
		if t, first, ok := newTIFF(data); ok {
			if f, ok := t.ifd(first)[tagOrientation]; ok {
				if v, ok := t.uint(f); ok && v <= 8 && Orientation(v).Valid() {
					x.Orientation = Orientation(v)
				}
			}
		}
	}
	if data, _, ok := c.tiff("CMT2"); ok {
		if t, first, ok := newTIFF(data); ok {
			x.DateTimeOriginal, x.OffsetKnown = t.dateTimeOriginal(t.ifd(first))
		}
	}
	if data, _, ok := c.tiff("CMT4"); ok {
		if t, first, ok := newTIFF(data); ok {
			x.GPS = t.gps(t.ifd(first))
		}
	}
	return x
}

// xmpAt is where a CR3's XMP packet is: its uuid box's payload.
func (c *cr3) xmpAt() (at, n int64, ok bool) {
	if !c.hasXMP || c.xmp.BodyLen() > maxRAWXMP || c.xmp.BodyLen() == 0 {
		return 0, 0, false
	}
	return c.xmp.Body(), c.xmp.BodyLen(), true
}

// readRAWExif reads the Exif of a TIFF-based RAW or a CR3; ok is false
// for a file that is neither.
func readRAWExif(r io.ReaderAt, size int64) (Exif, bool, error) {
	if t, ok := openRAWTIFF(r, size); ok {
		x, err := readRAWTIFFExif(t)
		return x, true, err
	}
	c, ok, err := openCR3(r, size)
	if !ok || err != nil {
		return Exif{}, ok, err
	}
	return c.exif(), true, nil
}

// readRAWXMP reads the XMP packet of a TIFF-based RAW (IFD0's XMLPacket)
// or a CR3 (its XMP uuid box); nil without one.
func readRAWXMP(r io.ReaderAt, size int64) ([]byte, bool, error) {
	var at, n int64
	var has bool
	if t, ok := openRAWTIFF(r, size); ok {
		at, n, has = t.xmpAt()
	} else {
		c, ok, err := openCR3(r, size)
		if !ok || err != nil {
			return nil, ok, err
		}
		at, n, has = c.xmpAt()
	}
	if !has {
		return nil, true, nil
	}
	b := make([]byte, n)
	if _, err := r.ReadAt(b, at); err != nil {
		return nil, true, fmt.Errorf("%w: reading the XMP packet: %v", ErrMalformed, err)
	}
	return b, true, nil
}

// SetRAWOrientation writes to dst the TIFF-based RAW or CR3 in src (size
// bytes) with its orientation set to o, as PlanRAWOrientation plans it.
func SetRAWOrientation(src io.ReaderAt, size int64, dst io.Writer, o Orientation) error {
	p, err := PlanRAWOrientation(src, size, o)
	if err != nil {
		return err
	}
	_, err = p.WriteTo(dst)
	return err
}

// PlanRAWOrientation plans setting the orientation of a camera RAW file:
// a TIFF-based one (DNG, CR2, NEF, ARW, ORF, RW2) or a CR3. Only the
// Orientation value of IFD0 (a CR3's CMT1) changes, where it is: a tag
// that's there, with its value in its entry. The plan is always in place,
// a patch of those bytes and, where the file's XMP packet says
// tiff:Orientation, of that digit, so the two agree. Nothing else is
// written: image data and maker notes stay byte for byte. A file without
// the tag is refused (ErrUnsupported) rather than restructured.
func PlanRAWOrientation(src io.ReaderAt, size int64, o Orientation) (*Plan, error) {
	if !o.Valid() {
		return nil, fmt.Errorf("mediameta: orientation %d: not 1 to 8", uint8(o))
	}
	var patches []Patch
	var xmpAt, xmpN int64
	var hasXMP bool
	if t, ok := openRAWTIFF(src, size); ok {
		f, at := t.ifdAt(t.first)
		p, err := orientationPatch(t.tiffReader, f, at, o)
		if err != nil {
			return nil, err
		}
		patches = append(patches, p)
		xmpAt, xmpN, hasXMP = t.xmpAt()
	} else {
		c, ok, err := openCR3(src, size)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, ErrNotRAW
		}
		data, base, ok := c.tiff("CMT1")
		if !ok {
			return nil, fmt.Errorf("%w: a CR3 without CMT1", ErrUnsupported)
		}
		tr, first, ok := newTIFF(data)
		if !ok {
			return nil, fmt.Errorf("%w: CMT1 isn't TIFF", ErrMalformed)
		}
		mem := &rawTIFF{tiffReader: tr, r: bytes.NewReader(data), size: int64(len(data)), first: first}
		f, at := mem.ifdAt(first)
		p, err := orientationPatch(tr, f, at, o)
		if err != nil {
			return nil, err
		}
		p.Offset += base
		patches = append(patches, p)
		xmpAt, xmpN, hasXMP = c.xmpAt()
	}
	if hasXMP {
		packet := make([]byte, xmpN)
		if _, err := src.ReadAt(packet, xmpAt); err != nil {
			return nil, fmt.Errorf("%w: reading the XMP packet: %v", ErrMalformed, err)
		}
		for _, i := range xmpOrientationDigits(packet) {
			if packet[i] != byte('0'+o) {
				patches = append(patches, Patch{Offset: xmpAt + int64(i), Data: []byte{byte('0' + o)}})
			}
		}
	}
	for i := 1; i < len(patches); i++ {
		for j := i; j > 0 && patches[j].Offset < patches[j-1].Offset; j-- {
			patches[j], patches[j-1] = patches[j-1], patches[j]
		}
	}
	var internal []patch
	for _, p := range patches {
		internal = append(internal, patch{at: p.Offset, data: p.Data})
	}
	return &Plan{InPlace: true, Patches: patches, Size: size, NewSize: size, src: src, patches: internal}, nil
}

// orientationPatch is the patch that sets IFD0's Orientation value in its
// entry: SHORT (or LONG, as some writers have it), count 1.
func orientationPatch(t tiffReader, ifd0 map[uint16]field, at map[uint16]int64, o Orientation) (Patch, error) {
	if ifd0 == nil {
		return Patch{}, fmt.Errorf("%w: no IFD0", ErrMalformed)
	}
	f, ok := ifd0[tagOrientation]
	if !ok {
		return Patch{}, fmt.Errorf("%w: no Orientation tag to change in place", ErrUnsupported)
	}
	var v []byte
	switch {
	case f.typ == typeShort && f.count == 1:
		v = t.order.(binary.AppendByteOrder).AppendUint16(nil, uint16(o))
	case f.typ == typeLong && f.count == 1:
		v = t.order.(binary.AppendByteOrder).AppendUint32(nil, uint32(o))
	default:
		return Patch{}, fmt.Errorf("%w: an Orientation of type %d, count %d", ErrUnsupported, f.typ, f.count)
	}
	return Patch{Offset: at[tagOrientation] + 8, Data: v}, nil
}

// xmpOrientationDigits are the offsets in an XMP packet of each
// tiff:Orientation value, as an attribute (tiff:Orientation="6") or an
// element (<tiff:Orientation>6</tiff:Orientation>): one digit, 1 to 8.
func xmpOrientationDigits(packet []byte) []int {
	var out []int
	for _, pre := range []string{`tiff:Orientation="`, `tiff:Orientation='`, `<tiff:Orientation>`} {
		rest, base := packet, 0
		for {
			i := bytes.Index(rest, []byte(pre))
			if i < 0 {
				break
			}
			d := base + i + len(pre)
			if d+1 < len(packet) && packet[d] >= '1' && packet[d] <= '8' &&
				(packet[d+1] == '"' || packet[d+1] == '\'' || packet[d+1] == '<') {
				out = append(out, d)
			}
			base = d
			rest = packet[d:]
		}
	}
	return out
}
