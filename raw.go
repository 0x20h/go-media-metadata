package mediameta

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math"

	"github.com/0x20h/go-media-metadata/internal/bmff"
)

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
	magic [4]byte // the header's first 4 bytes
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
	return &rawTIFF{tiffReader: tiffReader{order: order}, r: r, size: size, first: order.Uint32(h[4:]), magic: [4]byte(h[:4])}, true
}

// rw2 tells whether the file is a Panasonic RW2.
func (t *rawTIFF) rw2() bool { return string(t.magic[:]) == "IIU\x00" }

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
// One whose IFD0 points to a short Exif IFD has the camera fields that
// one lacks (ISO, say) in that JPEG's, as ExifTool reads them.
func readRAWTIFFExif(t *rawTIFF) (Exif, error) {
	x := t.exif(t.ifd, t.first)
	ifd0 := t.ifd(t.first)
	if ifd0 == nil {
		return Exif{}, fmt.Errorf("%w: IFD0 at %d outside the file", ErrMalformed, t.first)
	}
	if _, ok := ifd0[tagExifIFD]; ok {
		if x.Camera.incomplete() {
			if j, ok := t.jpgFromRaw(); ok {
				if jx, err := readJPEGExif(j, j.Size()); err == nil {
					x.Camera.fill(jx.Camera)
				}
			}
		}
	} else {
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
	var cmt1IFD0 map[uint16]field
	if data, _, ok := c.tiff("CMT1"); ok {
		if t, first, ok := newTIFF(data); ok {
			ifd0 := t.ifd(first)
			if f, ok := ifd0[tagOrientation]; ok {
				if v, ok := t.uint(f); ok && v <= 8 && Orientation(v).Valid() {
					x.Orientation = Orientation(v)
				}
			}
			x.Camera = t.camera(ifd0, nil)
			readXPTags(ifd0, &x)
			cmt1IFD0 = ifd0
		}
	}
	// CMT2 is the Exif IFD, a TIFF of its own: its byte order may differ
	// from CMT1's.
	if data, _, ok := c.tiff("CMT2"); ok {
		if t, first, ok := newTIFF(data); ok {
			exif := t.ifd(first)
			x.DateTimeOriginal, x.OffsetKnown = t.exifDate(exif, tagDateTimeOriginal, tagSubSecTimeOriginal, tagOffsetTimeOriginal)
			x.DateTimeDigitized, x.DigitizedOffsetKnown = t.exifDate(exif, tagDateTimeDigitized, tagSubSecTimeDigitized, tagOffsetTimeDigitized)
			// DateTime is in CMT1's IFD0; text reads the same in either
			// byte order.
			x.DateTime, x.DateTimeOffsetKnown = t.exifDateIn(cmt1IFD0, exif, tagDateTime, tagSubSecTime, tagOffsetTime)
			mk, model := x.Camera.Make, x.Camera.Model
			x.Camera = t.camera(nil, exif)
			x.Camera.Make, x.Camera.Model = mk, model
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

// readRAWExif reads the Exif of a TIFF-based RAW, a CR3 or a RAF (from
// the JPEG it embeds); ok is false for a file that is none of them.
func readRAWExif(r io.ReaderAt, size int64) (Exif, bool, error) {
	if x, ok, err := readRAFExif(r, size); ok {
		return x, true, err
	}
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
	if b, ok, err := readRAFXMP(r, size); ok {
		return b, true, err
	}
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

// planRAWOrientation plans setting the orientation of a camera RAW file:
// a TIFF-based one (DNG, CR2, NEF, ARW, ORF, RW2) or a CR3. Only the
// Orientation value of IFD0 (a CR3's CMT1) changes, where it is: a tag
// that's there, with its value in its entry. The plan is always in place,
// a patch of those bytes and, where the file's XMP packet says
// tiff:Orientation, of that digit, so the two agree. Nothing else is
// written: image data and maker notes stay byte for byte. A file without
// the tag is refused (ErrUnsupported) rather than restructured.
func planRAWOrientation(src io.ReaderAt, size int64, o Orientation) (*Plan, error) {
	if !o.Valid() {
		return nil, fmt.Errorf("mediameta: orientation %d: not 1 to 8", uint8(o))
	}
	var patches []Patch
	var xmpAt, xmpN int64
	var hasXMP bool
	if t, ok := openRAWTIFF(src, size); ok {
		f, at := t.ifdAt(t.first)
		if _, has := f[tagOrientation]; f != nil && !has {
			return addOrientationIFD0(t, o)
		}
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
			return nil, errNotRAW
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

// addOrientationIFD0 plans adding an Orientation tag to a TIFF whose IFD0
// has none, as most scanners' and converters' TIFFs and some DNGs: IFD0
// can't grow where it is, so a copy of it with the tag in tag order goes
// to the end of the file (at an even offset) and the header points to
// it. An Orientation entry IFD0 has but that doesn't read (a value past
// the file's end) is replaced. Every other byte stays where it was, so
// every offset into the file
// (strips, tiles, SubIFDs, ExifIFD, GPS, a DNG's raw data, IFD0's own
// values) stays valid; the old IFD0 is left unreferenced. An XMP packet's
// tiff:Orientation, if any, is set too. Standard TIFF headers only
// ("II*\0", "MM\0*"), under 4 GiB.
func addOrientationIFD0(t *rawTIFF, o Orientation) (*Plan, error) {
	if t.magic != [4]byte{'I', 'I', '*', 0} && t.magic != [4]byte{'M', 'M', 0, '*'} {
		return nil, fmt.Errorf("%w: no Orientation tag to add to in a %q file", ErrUnsupported, t.magic[:])
	}
	var nb [2]byte
	if _, err := t.r.ReadAt(nb[:], int64(t.first)); err != nil {
		return nil, fmt.Errorf("%w: reading IFD0: %v", ErrMalformed, err)
	}
	n := int(t.order.Uint16(nb[:]))
	if n == 0 || n >= maxIFDEntries || int64(t.first)+2+12*int64(n)+4 > t.size {
		return nil, fmt.Errorf("%w: an IFD0 of %d entries", ErrMalformed, n)
	}
	old := make([]byte, 12*n+4) // the entries and the next IFD's offset
	if _, err := t.r.ReadAt(old, int64(t.first)+2); err != nil {
		return nil, fmt.Errorf("%w: reading IFD0: %v", ErrMalformed, err)
	}
	end := t.size + t.size&1
	if end+2+12*int64(n+1)+4 > math.MaxUint32 {
		return nil, fmt.Errorf("%w: a TIFF too large to add an IFD to", ErrUnsupported)
	}
	ord := t.order.(binary.AppendByteOrder)
	entry := ord.AppendUint16(nil, tagOrientation)
	entry = ord.AppendUint16(entry, typeShort)
	entry = ord.AppendUint32(entry, 1)
	entry = ord.AppendUint16(entry, uint16(o))
	entry = append(entry, 0, 0)
	ifd := make([]byte, 0, int(end-t.size)+2+12*(n+1)+4)
	ifd = append(ifd, make([]byte, end-t.size)...)
	countAt := len(ifd)
	ifd = ord.AppendUint16(ifd, 0) // the entry count, set below
	kept, placed := 0, false
	for i := range n {
		e := old[12*i : 12*i+12]
		if t.order.Uint16(e) == tagOrientation {
			// An Orientation entry IFD0's reading dropped (a value past
			// the end of the file, say): replaced, or the reader would
			// still find it first.
			continue
		}
		kept++
		if !placed && t.order.Uint16(e) > tagOrientation {
			ifd, placed = append(ifd, entry...), true
		}
		ifd = append(ifd, e...)
	}
	if !placed {
		ifd = append(ifd, entry...)
	}
	t.order.PutUint16(ifd[countAt:], uint16(kept+1))
	ifd = append(ifd, old[12*n:]...)
	e := &spliceEdit{}
	e.add(splice{at: 4, del: 4, data: ord.AppendUint32(nil, uint32(end))})
	if xmpAt, xmpN, ok := t.xmpAt(); ok {
		packet := make([]byte, xmpN)
		if _, err := t.r.ReadAt(packet, xmpAt); err != nil {
			return nil, fmt.Errorf("%w: reading the XMP packet: %v", ErrMalformed, err)
		}
		for _, i := range xmpOrientationDigits(packet) {
			if packet[i] != byte('0'+o) {
				e.add(splice{at: xmpAt + int64(i), del: 1, data: []byte{byte('0' + o)}})
			}
		}
	}
	e.add(splice{at: t.size, data: ifd})
	if err := e.sorted(); err != nil {
		return nil, err
	}
	return e.plan(t.r, t.size), nil
}
