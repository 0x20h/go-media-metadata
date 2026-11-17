package mediameta

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"slices"

	"github.com/0x20h/go-media-metadata/internal/bmff"
)

// HashImageData writes the image data of the file in r (size bytes) to w,
// leaving out the metadata, and returns how many bytes it wrote. With w a
// hash (md5.New(), sha256.New()), the sum is the value ExifTool gives as
// ImageDataHash with the same ImageHashType: it changes when the image
// does, and not when only the metadata does. That shows a metadata write
// left the image as it was, and finds the same image under different
// metadata.
//
// What is the image data, in each format:
//
//   - JPEG: the scans, from the first SOS marker to EOI, leaving out the
//     segments between scans (DHT, DQT, …), EOI and what follows it. As
//     ExifTool does, the data of a scan after its last RST marker (or
//     stuffed 0xFF) is left out when a segment follows it rather than
//     the next scan.
//   - PNG: the IDAT chunks' data (and a JNG's JDAT and JDAA), without
//     their length, type and CRC.
//   - WebP: the VP8, VP8L, ALPH, ANIM and ANMF chunks' data, each with its
//     padding byte, without its header.
//   - HEIF: the data of every coded image item (av01, avc1, hvc1, lhv1,
//     hvt1: tiles, thumbnails and auxiliary images too), in ascending
//     item id, each item's extents in order.
//   - JPEG XL: the jxlc and jxlp boxes' payloads (jxlp's with its part
//     index); a bare codestream is the whole file.
//   - TIFF-based camera RAW: the strips and tiles of each IFD and SubIFD
//     but previews (see docs/api.md), and a Panasonic RW2's raw data.
//   - Fujifilm RAF: the raw data blocks the header points to.
//   - QuickTime, MP4 and Canon CR3: the chunks of each video and sound
//     track, in track order, then in chunk-offset order.
//
// A transport stream (MTS, M2TS) gives an error that is ErrUnsupported:
// ExifTool gives no ImageDataHash for one either. A file of none of these
// formats gives an error that is ErrUnknownFormat. A file without image data writes nothing, with no
// error; ExifTool gives no ImageDataHash then.
func HashImageData(r io.ReaderAt, size int64, w io.Writer) (int64, error) {
	h := &rangeWriter{r: r, size: size, w: w}
	var err error
	if f, herr := parseHEIF(r, size); herr == nil {
		err = h.heif(f)
	} else {
		switch detectFormat(r, size) {
		case formatJPEG:
			err = h.jpeg()
		case formatPNG:
			err = h.png()
		case formatWebP:
			err = h.webp()
		case formatJXL:
			err = h.jxl()
		case formatRAW:
			err = h.raw()
		case formatMovie:
			err = h.movie()
		case formatTS:
			// ExifTool gives no ImageDataHash for a transport stream either.
			return 0, fmt.Errorf("%w: no image data hash for a transport stream", ErrUnsupported)
		case formatMatroska:
			// Nor for Matroska.
			return 0, fmt.Errorf("%w: no image data hash for a Matroska or WebM file", ErrUnsupported)
		default:
			if startsWithFtyp(r, size) {
				// A brand this package doesn't take as HEIF or a movie.
				err = h.movie()
				break
			}
			return 0, formatError(imageDataFormats)
		}
	}
	return h.n, err
}

// imageDataFormats are the formats HashImageData takes.
const imageDataFormats = "JPEG, PNG, WebP, HEIF, JPEG XL, camera RAW, QuickTime or MP4 file"

// rangeWriter copies ranges of r to w, counting the bytes.
type rangeWriter struct {
	r    io.ReaderAt
	size int64
	w    io.Writer
	n    int64
	buf  []byte
}

// copy writes r's bytes [off, off+n) to w; a range past the end of the
// file is cut at it.
func (h *rangeWriter) copy(off, n int64) error {
	if off < 0 || n <= 0 || off >= h.size {
		return nil
	}
	n = min(n, h.size-off)
	if h.buf == nil {
		h.buf = make([]byte, 64<<10)
	}
	for n > 0 {
		k := min(n, int64(len(h.buf)))
		if _, err := h.r.ReadAt(h.buf[:k], off); err != nil {
			return fmt.Errorf("%w: %v", ErrMalformed, err)
		}
		if err := h.write(h.buf[:k]); err != nil {
			return err
		}
		off, n = off+k, n-k
	}
	return nil
}

func (h *rangeWriter) write(b []byte) error {
	n, err := h.w.Write(b)
	h.n += int64(n)
	return err
}

// jpeg writes the scans (ITU-T T.81 B.2). After the first SOS, the file is
// read as pieces, each from a marker without a length (a stuffed 0xFF00,
// RST0 to RST7, or SOS, whose header comes with its data here) to the next
// marker. A piece is written when that next marker has no length either;
// one followed by a segment (DHT, DQT, …) isn't. Fill bytes (0xFF) before
// a marker belong to the piece before it.
func (h *rangeWriter) jpeg() error {
	f, err := parseJPEG(h.r, h.size)
	if err != nil {
		return err
	}
	at := int64(-1)
	for _, s := range f.segs {
		if s.marker == markerSOS {
			at = s.offset
			break
		}
	}
	if at < 0 {
		return nil
	}
	sc := &jpegScanner{r: h.r, size: h.size}
	// The piece being read: where it starts, and whether it's written.
	start, hashed := at, true
	p := at + 2 // after the FF DA
	for {
		ff, ok := sc.next0xFF(p)
		if !ok {
			return sc.err // the end of the file before EOI: the piece is left out
		}
		// A run of 0xFF: the last is the marker's, the others fill.
		q := ff + 1
		for {
			b, ok := sc.byteAt(q)
			if !ok {
				return sc.err
			}
			if b != 0xFF {
				break
			}
			q++
		}
		m, _ := sc.byteAt(q)
		markerAt := q - 1
		if jpegNoLength(m) {
			if hashed {
				if err := sc.copyTo(h, start, markerAt-start); err != nil {
					return err
				}
			}
			if m == markerEOI {
				return nil
			}
			start, hashed = markerAt, m == 0x00 || m == markerSOS || m >= 0xD0 && m <= 0xD7
			p = q + 1
			continue
		}
		// A segment: the piece before it is left out, and so is the
		// segment.
		var l [2]byte
		if _, err := h.r.ReadAt(l[:], q+1); err != nil {
			return nil
		}
		n := int64(binary.BigEndian.Uint16(l[:]))
		if n < 2 {
			return fmt.Errorf("%w: a JPEG segment of length %d", ErrMalformed, n)
		}
		p = q + 1 + n
		start, hashed = p, false
	}
}

// jpegNoLength tells whether a marker has no length after it: TEM, RST0 to
// RST7, SOI, EOI, SOS (whose header is read with its scan here), and 0x00,
// which makes 0xFF00 a stuffed byte rather than a marker.
func jpegNoLength(m byte) bool {
	return m == 0x00 || m == 0x01 || m >= 0xD0 && m <= markerSOS
}

// jpegScanner finds bytes in a JPEG's scan data, reading a window at a
// time.
type jpegScanner struct {
	r      io.ReaderAt
	size   int64
	win    []byte
	winAt  int64
	err    error
	window int
}

func (s *jpegScanner) load(p int64) bool {
	if p >= s.size || p < 0 {
		return false
	}
	if s.win == nil {
		s.window = 64 << 10
		s.win = make([]byte, s.window)
	}
	n := min(int64(s.window), s.size-p)
	if _, err := s.r.ReadAt(s.win[:n], p); err != nil && err != io.EOF {
		s.err = fmt.Errorf("%w: %v", ErrMalformed, err)
		return false
	}
	s.win, s.winAt = s.win[:n], p
	return true
}

func (s *jpegScanner) has(p int64) bool {
	return s.win != nil && p >= s.winAt && p < s.winAt+int64(len(s.win))
}

func (s *jpegScanner) byteAt(p int64) (byte, bool) {
	if !s.has(p) && !s.load(p) {
		return 0, false
	}
	return s.win[p-s.winAt], true
}

// copyTo writes [off, off+n) to h: from the window when it holds them, else
// read again.
func (s *jpegScanner) copyTo(h *rangeWriter, off, n int64) error {
	if n > 0 && s.has(off) && s.has(off+n-1) {
		return h.write(s.win[off-s.winAt : off-s.winAt+n])
	}
	return h.copy(off, n)
}

// next0xFF is the offset of the next 0xFF at or after p.
func (s *jpegScanner) next0xFF(p int64) (int64, bool) {
	for {
		if !s.has(p) && !s.load(p) {
			return 0, false
		}
		if i := bytes.IndexByte(s.win[p-s.winAt:], 0xFF); i >= 0 {
			return p + int64(i), true
		}
		p = s.winAt + int64(len(s.win))
	}
}

// png writes the IDAT, JDAT and JDAA chunks' data (PNG 11.2.4; JNG).
func (h *rangeWriter) png() error {
	chunks, err := pngChunks(h.r, h.size)
	if err != nil {
		return err
	}
	for _, c := range chunks {
		switch string(c.typ[:]) {
		case "IDAT", "JDAT", "JDAA":
			if err := h.copy(c.data(), c.n); err != nil {
				return err
			}
		}
	}
	return nil
}

// webp writes the data of the chunks that hold the image (WebP container
// specification): VP8, VP8L, ALPH, ANIM and ANMF, each with its padding.
func (h *rangeWriter) webp() error {
	f, err := parseWebP(h.r, h.size)
	if err != nil {
		return err
	}
	for _, c := range f.chunks {
		switch string(c.fourcc[:]) {
		case "VP8 ", "VP8L", "ALPH", "ANIM", "ANMF":
			if err := h.copy(c.data(), c.n+c.n&1); err != nil {
				return err
			}
		}
	}
	return nil
}

// heifImageTypes are the item types that are coded images.
var heifImageTypes = []bmff.Type{bmff.T("av01"), bmff.T("avc1"), bmff.T("hvc1"), bmff.T("lhv1"), bmff.T("hvt1")}

// heif writes the coded image items' data (ISO/IEC 23008-12), by item
// id, each item's extents (ISO/IEC 14496-12 8.11.3) in order: in the file
// (construction method 0) or in the meta box's idat (1).
func (h *rangeWriter) heif(f *heifFile) error {
	if f.m.Loc == nil {
		return nil
	}
	items := slices.Clone(f.m.Loc.Items)
	slices.SortStableFunc(items, func(a, b bmff.LocItem) int { return int(int64(a.ID) - int64(b.ID)) })
	idat, idatEnd := int64(-1), int64(0)
	for _, it := range items {
		if !slices.Contains(heifImageTypes, f.m.ItemTypes[it.ID]) || it.DataRefIndex != 0 || it.ConstructionMethod > 1 {
			continue
		}
		base := int64(it.BaseOffset)
		if it.BaseOffset > uint64(h.size) {
			continue
		}
		// An extent of length 0 runs to the end of the data its offsets
		// count in: the file, or idat's payload (ISO/IEC 14496-12
		// 8.11.3.3).
		end := h.size
		if it.ConstructionMethod == 1 {
			if idat < 0 {
				idat, idatEnd = f.idatBody(h.r)
			}
			if idat < 0 {
				continue
			}
			base += idat
			end = idatEnd
		}
		for _, e := range it.Extents {
			if e.Offset > uint64(h.size) || e.Length > uint64(h.size) {
				continue
			}
			n := int64(e.Length)
			if n == 0 {
				n = end - (base + int64(e.Offset))
				if n < 0 {
					continue
				}
			}
			if err := h.copy(base+int64(e.Offset), n); err != nil {
				return err
			}
		}
	}
	return nil
}

// idatBody is where the meta box's idat payload starts and ends, -1
// without one.
func (f *heifFile) idatBody(r io.ReaderAt) (start, end int64) {
	kids, err := bmff.ReadBoxes(r, f.meta.Body()+4, f.meta.End())
	if err != nil {
		return -1, 0
	}
	if b, ok := bmff.Find(kids, bmff.T("idat")); ok {
		return b.Body(), b.End()
	}
	return -1, 0
}

// jxl writes a container's codestream boxes' payloads (ISO/IEC 18181-2):
// jxlc, and each jxlp with its 4-byte part index; a bare codestream is all
// of the file.
func (h *rangeWriter) jxl() error {
	f, err := parseJXL(h.r, h.size)
	if err != nil {
		return err
	}
	if !f.container() {
		return h.copy(0, h.size)
	}
	for _, b := range f.boxes {
		switch b.Type {
		case bmff.T("jxlc"), bmff.T("jxlp"), bmff.T("jp2c"), bmff.T("jbrd"):
			if err := h.copy(b.Body(), b.BodyLen()); err != nil {
				return err
			}
		}
	}
	return nil
}

// Tags of a RAW's image data.
const (
	tagRW2RawDataOffset   = 0x0118
	tagTileOffsets        = 0x0144
	tagTileByteCounts     = 0x0145
	tagJPEGInterchange    = 0x0201
	tagJPEGInterchangeLen = 0x0202
	tagDNGVersion         = 0xC612
	tagRAFStripOffsets    = 0xF007
	tagRAFStripByteCounts = 0xF008
)

// raw writes a camera RAW's image data: a CR3's tracks as a movie's, a
// RAF's raw blocks, and a TIFF-based file's strips and tiles.
func (h *rangeWriter) raw() error {
	if _, _, isRAF, _ := openRAF(h.r, h.size); isRAF {
		return h.raf()
	}
	if _, ok, err := openCR3(h.r, h.size); err != nil {
		return err
	} else if ok {
		return h.movie()
	}
	t, ok := openRAWTIFF(h.r, h.size)
	if !ok {
		return formatError(imageDataFormats)
	}
	w := tiffWalk{h: h, t: t, seen: map[uint32]bool{}}
	ifd0 := t.ifd(t.first)
	if ifd0 == nil {
		return fmt.Errorf("%w: IFD0 at %d outside the file", ErrMalformed, t.first)
	}
	var magic [12]byte
	_, err := h.r.ReadAt(magic[:], 0)
	w.rw2 = err == nil && string(magic[:4]) == "IIU\x00"
	if err == nil && string(magic[8:10]) == "CR" {
		w.kind = "CR2"
	} else if _, ok := ifd0[tagDNGVersion]; ok {
		w.kind = "DNG"
	} else if mk, _ := ascii(ifd0[0x010F]); len(mk) >= 4 && mk[:4] == "SONY" {
		w.kind = "ARW"
	}
	// IFD0, IFD1, …: the chain.
	off := t.first
	for i := 0; off != 0 && i < maxIFDChain; i++ {
		next, err := w.ifd(off, fmt.Sprintf("IFD%d", i), 0)
		if err != nil {
			return err
		}
		off = next
	}
	return nil
}

// tiffWalk writes the image data of a TIFF-based RAW's IFDs.
type tiffWalk struct {
	h    *rangeWriter
	t    *rawTIFF
	kind string // "CR2", "DNG", "ARW", or "" for the others
	rw2  bool   // a Panasonic RW2
	seen map[uint32]bool
	// compression and subfileType are the last read, in any IFD: a
	// value carries over to an IFD without the tag.
	compression, subfileType uint32
}

// ifd writes the image data under the IFD at off, named as DNG and TIFF
// readers name them ("IFD0", "SubIFD", "SubIFD1", …), and returns the
// next IFD's offset. Its SubIFDs come first, then its own data, by tag:
// StripOffsets, a RW2's RawDataOffset, TileOffsets, then
// JPEGInterchangeFormat.
func (w *tiffWalk) ifd(off uint32, name string, depth int) (uint32, error) {
	if w.seen[off] || depth > 2 {
		return 0, nil
	}
	w.seen[off] = true
	m, _ := w.t.ifdAt(off)
	if m == nil {
		return 0, nil
	}
	t := w.t
	if f, ok := m[tagCompression]; ok {
		w.compression, _ = t.uint(f)
	}
	if f, ok := m[tagNewSubfileType]; ok {
		w.subfileType, _ = t.uint(f)
	}
	compression, subfileType := w.compression, w.subfileType
	if f, ok := m[tagSubIFDs]; ok && (f.typ == typeLong || f.typ == typeIFD) {
		for i := 0; i < f.count && i < maxSubIFDs && 4*i+4 <= len(f.value); i++ {
			sub := "SubIFD"
			if i > 0 {
				sub = fmt.Sprintf("SubIFD%d", i)
			}
			if _, err := w.ifd(t.order.Uint32(f.value[4*i:]), sub, depth+1); err != nil {
				return 0, err
			}
		}
	}
	// StripOffsets, but for previews: a CR2's IFD0, and a DNG's JPEG
	// (compression 7) that isn't the full image, but in SubIFD2.
	preview := w.kind == "CR2" && name == "IFD0" ||
		w.kind == "DNG" && compression == 7 && subfileType != 0 && name != "SubIFD2"
	if !preview || compression == 34892 || compression == 52546 {
		if err := w.pairs(m, tagStripOffsets, tagStripByteCounts); err != nil {
			return 0, err
		}
	}
	if f, ok := m[tagRW2RawDataOffset]; ok && w.rw2 {
		if v, ok := t.uint(f); ok {
			if err := w.h.copy(int64(v), w.h.size-int64(v)); err != nil {
				return 0, err
			}
		}
	}
	if err := w.pairs(m, tagTileOffsets, tagTileByteCounts); err != nil {
		return 0, err
	}
	// JPEGInterchangeFormat, but for the thumbnail (IFD1) and an ARW's
	// preview in IFD0.
	thumbnail := name == "IFD1" || name == "IFD0" && w.kind == "ARW"
	if !thumbnail {
		if err := w.pairs(m, tagJPEGInterchange, tagJPEGInterchangeLen); err != nil {
			return 0, err
		}
	}
	return w.next(off), nil
}

// pairs writes the ranges an offsets tag and its byte counts give.
func (w *tiffWalk) pairs(m map[uint16]field, offTag, countTag uint16) error {
	offs, ok1 := m[offTag]
	counts, ok2 := m[countTag]
	if !ok1 || !ok2 {
		return nil
	}
	os, ns := w.t.uints(offs), w.t.uints(counts)
	for i := 0; i < len(os) && i < len(ns); i++ {
		if err := w.h.copy(int64(os[i]), int64(ns[i])); err != nil {
			return err
		}
	}
	return nil
}

// next is the offset of the IFD after the one at off, 0 without one.
func (w *tiffWalk) next(off uint32) uint32 {
	var nb [2]byte
	if _, err := w.t.r.ReadAt(nb[:], int64(off)); err != nil {
		return 0
	}
	at := int64(off) + 2 + 12*int64(w.t.order.Uint16(nb[:]))
	var b [4]byte
	if _, err := w.t.r.ReadAt(b[:], at); err != nil {
		return 0
	}
	return w.t.order.Uint32(b[:])
}

// raf writes a RAF's raw data: for each of the blocks the header points
// to (offsets and lengths at bytes 100 and 128, big-endian), the strips
// its TIFF structure gives (tags 0xF007 and 0xF008), or the whole block
// when it isn't TIFF.
func (h *rangeWriter) raf() error {
	var hd [136]byte
	if _, err := h.r.ReadAt(hd[:], 0); err != nil {
		return nil
	}
	for _, p := range []int{100, 128} {
		off, n := int64(binary.BigEndian.Uint32(hd[p:])), int64(binary.BigEndian.Uint32(hd[p+4:]))
		if off < int64(len(hd)) || n == 0 || off >= h.size {
			continue
		}
		block := io.NewSectionReader(h.r, off, min(n, h.size-off))
		t, ok := openRAWTIFF(block, block.Size())
		if !ok {
			if err := h.copy(off, n); err != nil {
				return err
			}
			continue
		}
		m := t.ifd(t.first)
		offs, counts := m[tagRAFStripOffsets], m[tagRAFStripByteCounts]
		os, ns := t.uints(offs), t.uints(counts)
		for i := 0; i < len(os) && i < len(ns); i++ {
			if err := h.copy(off+int64(os[i]), int64(ns[i])); err != nil {
				return err
			}
		}
	}
	return nil
}

// maxSampleTable caps a track's sample table boxes read.
const maxSampleTable = 64 << 20

// movie writes each video and sound track's chunks (ISO/IEC 14496-12
// 8.7): in track order, each track's chunks in the order its stco or
// co64 lists them, a chunk being its samples' sizes (stsz, stsc) from its
// offset, cut at the end of the mdat that holds it.
func (h *rangeWriter) movie() error {
	top, err := bmff.ReadBoxes(h.r, 0, h.size)
	if err != nil {
		return err
	}
	moov, ok := bmff.Find(top, bmff.T("moov"))
	if !ok {
		return nil
	}
	var mdats []bmff.Box
	for _, b := range top {
		if b.Type == bmff.T("mdat") {
			mdats = append(mdats, b)
		}
	}
	kids, err := bmff.ReadBoxes(h.r, moov.Body(), moov.End())
	if err != nil {
		return err
	}
	for _, trak := range kids {
		if trak.Type != bmff.T("trak") {
			continue
		}
		if err := h.track(trak, mdats); err != nil {
			return err
		}
	}
	return nil
}

func (h *rangeWriter) track(trak bmff.Box, mdats []bmff.Box) error {
	path := func(b bmff.Box, types ...string) (bmff.Box, bool) {
		for _, typ := range types {
			kids, err := bmff.ReadBoxes(h.r, b.Body(), b.End())
			if err != nil {
				return bmff.Box{}, false
			}
			var ok bool
			if b, ok = bmff.Find(kids, bmff.T(typ)); !ok {
				return bmff.Box{}, false
			}
		}
		return b, true
	}
	hdlr, ok := path(trak, "mdia", "hdlr")
	if !ok {
		return nil
	}
	hb, err := readBox(h.r, hdlr, maxLeafBox)
	if err != nil || len(hb) < 12 {
		return nil
	}
	if handler := string(hb[8:12]); handler != "vide" && handler != "soun" {
		return nil
	}
	stbl, ok := path(trak, "mdia", "minf", "stbl")
	if !ok {
		return nil
	}
	kids, err := bmff.ReadBoxes(h.r, stbl.Body(), stbl.End())
	if err != nil {
		return nil
	}
	read := func(typ string) []byte {
		b, ok := bmff.Find(kids, bmff.T(typ))
		if !ok {
			return nil
		}
		data, err := readBox(h.r, b, maxSampleTable)
		if err != nil {
			return nil
		}
		return data
	}
	offsets, sizes, perChunk := stcoOffsets(read("stco"), read("co64")), sampleSizes(read("stsz"), read("stz2")), read("stsc")
	if offsets == nil || sizes.n == 0 || len(perChunk) < 8 {
		return nil
	}
	// stsc: runs of (first chunk, samples per chunk, description index).
	runs := int(min(uint64(binary.BigEndian.Uint32(perChunk[4:])), uint64(len(perChunk)-8)/12))
	sample, run := 0, 0
	var per uint32
	for i, off := range offsets {
		// The runs go by first chunk, ascending.
		chunk := uint32(i + 1)
		for run < runs && binary.BigEndian.Uint32(perChunk[8+12*run:]) <= chunk {
			per = binary.BigEndian.Uint32(perChunk[8+12*run+4:])
			run++
		}
		k := int(min(uint64(per), uint64(sizes.n-sample)))
		var n int64
		if sizes.sizes == nil {
			n = int64(k) * int64(sizes.all)
		} else {
			for _, v := range sizes.sizes[sample : sample+k] {
				n += int64(v)
			}
		}
		sample += k
		end := h.size
		for _, m := range mdats {
			if int64(off) >= m.Body() && int64(off) < m.End() {
				end = m.End()
				break
			}
		}
		if int64(off) >= end {
			continue
		}
		if err := h.copy(int64(off), min(n, end-int64(off))); err != nil {
			return err
		}
	}
	return nil
}

// stcoOffsets reads stco's or co64's offsets.
func stcoOffsets(stco, co64 []byte) []uint64 {
	var out []uint64
	switch {
	case len(stco) >= 8:
		n := min(uint64(binary.BigEndian.Uint32(stco[4:])), uint64(len(stco)-8)/4)
		out = make([]uint64, n)
		for i := range out {
			out[i] = uint64(binary.BigEndian.Uint32(stco[8+4*i:]))
		}
	case len(co64) >= 8:
		n := min(uint64(binary.BigEndian.Uint32(co64[4:])), uint64(len(co64)-8)/8)
		out = make([]uint64, n)
		for i := range out {
			out[i] = binary.BigEndian.Uint64(co64[8+8*i:])
		}
	}
	return out
}

// sampleTable is a track's sample sizes: n samples, each all bytes, or
// as sizes lists them.
type sampleTable struct {
	n     int
	all   uint32
	sizes []uint32
}

// sampleSizes reads stsz's sample sizes (one size for all, or a table),
// or stz2's compact table.
func sampleSizes(stsz, stz2 []byte) sampleTable {
	t := sampleTable{sizes: sampleSizeList(stsz, stz2)}
	t.n = len(t.sizes)
	if len(stsz) >= 12 {
		if all := binary.BigEndian.Uint32(stsz[4:]); all != 0 {
			t = sampleTable{n: int(min(binary.BigEndian.Uint32(stsz[8:]), 1<<31-1)), all: all}
		}
	}
	return t
}

// sampleSizeList is stsz's or stz2's table of sizes.
func sampleSizeList(stsz, stz2 []byte) []uint32 {
	switch {
	case len(stsz) >= 12:
		n := uint64(binary.BigEndian.Uint32(stsz[8:]))
		if binary.BigEndian.Uint32(stsz[4:]) != 0 {
			return nil
		}
		n = min(n, uint64(len(stsz)-12)/4)
		out := make([]uint32, n)
		for i := range out {
			out[i] = binary.BigEndian.Uint32(stsz[12+4*i:])
		}
		return out
	case len(stz2) >= 12:
		field, n := stz2[7], uint64(binary.BigEndian.Uint32(stz2[8:]))
		data := stz2[12:]
		var out []uint32
		switch field {
		case 4:
			n = min(n, uint64(len(data))*2)
			out = make([]uint32, n)
			for i := range out {
				b := data[i/2]
				if i%2 == 0 {
					out[i] = uint32(b >> 4)
				} else {
					out[i] = uint32(b & 0x0F)
				}
			}
		case 8:
			n = min(n, uint64(len(data)))
			out = make([]uint32, n)
			for i := range out {
				out[i] = uint32(data[i])
			}
		case 16:
			n = min(n, uint64(len(data))/2)
			out = make([]uint32, n)
			for i := range out {
				out[i] = uint32(binary.BigEndian.Uint16(data[2*i:]))
			}
		}
		return out
	}
	return nil
}
