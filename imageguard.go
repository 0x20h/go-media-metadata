package mediameta

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math"

	"github.com/0x20h/go-media-metadata/internal/bmff"
)

// The image data guard: a metadata write leaves the image data, as
// HashImageData takes it, byte for byte. In the formats whose image data
// is found by offsets (TIFF and camera RAW strips and tiles, HEIF item
// extents, a movie's chunks) a malformed file can declare image data
// over its own metadata, or past the end of the file, where an edit or an
// append would land in it. Such an edit is refused (ErrUnsupported).
//
// The declared ranges are taken before they're cut at the end of the
// file. An edit whose changed bytes (in the source's offsets, an append
// at the end touching the byte after) stay out of every range leaves the
// image data's bytes as they were, wherever the plan moves them.

// errImageDataOverlap is the error of an edit that would touch image data.
func errImageDataOverlap(at int64) error {
	return fmt.Errorf("%w: a file whose image data overlaps its metadata (at %d)", ErrUnsupported, at)
}

// imageDataRanges are a RAW or TIFF file's image data ranges, as
// declared. None for a file whose image data doesn't walk, which is left
// to the edit's own checks.
func imageDataRanges(src io.ReaderAt, size int64) [][2]int64 {
	var ranges [][2]int64
	h := &rangeWriter{r: src, size: size, w: io.Discard, record: &ranges}
	if h.raw() != nil {
		return nil
	}
	return ranges
}

// movieImageRanges are a movie's video and sound chunks, as declared.
// The movie box is read once and walked in memory.
func movieImageRanges(src io.ReaderAt, size int64) [][2]int64 {
	top, err := bmff.ReadBoxes(src, 0, size)
	if err != nil {
		return nil
	}
	moov, ok := bmff.Find(top, bmff.T("moov"))
	if !ok || moov.Size > maxMoov {
		return nil
	}
	b := make([]byte, moov.Size)
	if _, err := src.ReadAt(b, moov.Offset); err != nil {
		return nil
	}
	return movieImageRangesIn(top, b)
}

// movieImageRangesIn is movieImageRanges of a movie whose top-level
// boxes and movie box bytes are read already: each video and
// sound track's chunks, as HashImageData takes them, read from the movie
// box in memory. A chunk in an mdat is cut at its end, as the hash cuts
// it; one outside every mdat is kept as declared. Chunks that touch are
// joined.
func movieImageRangesIn(top []bmff.Box, moov []byte) [][2]int64 {
	boxes, err := bmff.ParseBoxes(moov, 0, int64(len(moov)))
	if err != nil || len(boxes) != 1 {
		return nil
	}
	kids, err := bmff.ParseBoxes(moov, boxes[0].Body(), boxes[0].End())
	if err != nil {
		return nil
	}
	var ranges [][2]int64
	for _, trak := range kids {
		if trak.Type == bmff.T("trak") {
			ranges = trackChunkRanges(ranges, moov, trak, top)
		}
	}
	return ranges
}

// trackChunkRanges appends a track's chunk ranges to ranges, as
// rangeWriter.track walks them.
func trackChunkRanges(ranges [][2]int64, moov []byte, trak bmff.Box, top []bmff.Box) [][2]int64 {
	child := func(b bmff.Box, t bmff.Type) (bmff.Box, bool) {
		kids, err := bmff.ParseBoxes(moov, b.Body(), b.End())
		if err != nil {
			return bmff.Box{}, false
		}
		return bmff.Find(kids, t)
	}
	mdia, ok := child(trak, bmff.T("mdia"))
	if !ok {
		return ranges
	}
	hdlr, ok := child(mdia, bmff.T("hdlr"))
	if !ok || hdlr.BodyLen() > maxLeafBox || hdlr.BodyLen() < 12 {
		return ranges
	}
	if h := string(moov[hdlr.Body()+8 : hdlr.Body()+12]); h != "vide" && h != "soun" {
		return ranges
	}
	minf, ok := child(mdia, bmff.T("minf"))
	if !ok {
		return ranges
	}
	stbl, ok := child(minf, bmff.T("stbl"))
	if !ok {
		return ranges
	}
	kids, err := bmff.ParseBoxes(moov, stbl.Body(), stbl.End())
	if err != nil {
		return ranges
	}
	body := func(t bmff.Type) []byte {
		b, ok := bmff.Find(kids, t)
		if !ok || b.BodyLen() > maxSampleTable {
			return nil
		}
		return moov[b.Body():b.End()]
	}
	stco, co64, perChunk := body(bmff.T("stco")), body(bmff.T("co64")), body(bmff.T("stsc"))
	sizes := newSampleSizer(body(bmff.T("stsz")), body(bmff.T("stz2")))
	var chunks uint64
	switch {
	case len(stco) >= 8:
		chunks = min(uint64(binary.BigEndian.Uint32(stco[4:])), uint64(len(stco)-8)/4)
	case len(co64) >= 8:
		chunks = min(uint64(binary.BigEndian.Uint32(co64[4:])), uint64(len(co64)-8)/8)
	default:
		return ranges
	}
	if chunks == 0 || sizes.n == 0 || len(perChunk) < 8 {
		return ranges
	}
	runs := int(min(uint64(binary.BigEndian.Uint32(perChunk[4:])), uint64(len(perChunk)-8)/12))
	sample, run := 0, 0
	var per uint32
	for i := uint64(0); i < chunks; i++ {
		var off uint64
		if len(stco) >= 8 {
			off = uint64(binary.BigEndian.Uint32(stco[8+4*i:]))
		} else {
			off = binary.BigEndian.Uint64(co64[8+8*i:])
		}
		chunk := uint32(i + 1)
		for run < runs && binary.BigEndian.Uint32(perChunk[8+12*run:]) <= chunk {
			per = binary.BigEndian.Uint32(perChunk[8+12*run+4:])
			run++
		}
		k := int(min(uint64(per), uint64(sizes.n-sample)))
		n := sizes.sum(sample, k)
		sample += k
		if n <= 0 || off > math.MaxInt64 {
			continue
		}
		start, end := int64(off), int64(off)+n
		if end < start {
			end = math.MaxInt64
		}
		for _, m := range top {
			if m.Type == bmff.T("mdat") && start >= m.Body() && start < m.End() {
				end = min(end, m.End())
				break
			}
		}
		if last := len(ranges) - 1; last >= 0 && ranges[last][1] == start {
			ranges[last][1] = end
		} else {
			ranges = append(ranges, [2]int64{start, end})
		}
	}
	return ranges
}

// sampleSizer reads a track's sample sizes in place: n samples, each all
// bytes, or as stsz's table (4 bytes each) or stz2's (field bits each)
// lists them.
type sampleSizer struct {
	n     int
	all   uint32
	table []byte
	field byte
}

// newSampleSizer is sampleSizes without the copy.
func newSampleSizer(stsz, stz2 []byte) sampleSizer {
	switch {
	case len(stsz) >= 12:
		if all := binary.BigEndian.Uint32(stsz[4:]); all != 0 {
			return sampleSizer{n: int(min(binary.BigEndian.Uint32(stsz[8:]), 1<<31-1)), all: all}
		}
		n := min(uint64(binary.BigEndian.Uint32(stsz[8:])), uint64(len(stsz)-12)/4)
		return sampleSizer{n: int(n), table: stsz[12:], field: 32}
	case len(stz2) >= 12:
		field, n, data := stz2[7], uint64(binary.BigEndian.Uint32(stz2[8:])), stz2[12:]
		switch field {
		case 4:
			n = min(n, uint64(len(data))*2)
		case 8:
			n = min(n, uint64(len(data)))
		case 16:
			n = min(n, uint64(len(data))/2)
		default:
			return sampleSizer{}
		}
		return sampleSizer{n: int(n), table: data, field: field}
	}
	return sampleSizer{}
}

// sum is the size of k samples from sample i.
func (s sampleSizer) sum(i, k int) int64 {
	if s.table == nil {
		return int64(k) * int64(s.all)
	}
	var n int64
	for j := i; j < i+k; j++ {
		switch s.field {
		case 32:
			n += int64(binary.BigEndian.Uint32(s.table[4*j:]))
		case 16:
			n += int64(binary.BigEndian.Uint16(s.table[2*j:]))
		case 8:
			n += int64(s.table[j])
		case 4:
			if b := s.table[j/2]; j%2 == 0 {
				n += int64(b >> 4)
			} else {
				n += int64(b & 0x0F)
			}
		}
	}
	return n
}

// heifFileExtents are a HEIF file's coded image items' extents in the
// file (construction method 0), as declared, in one allocation; idat's
// are compared by guardHEIFPlan.
func heifFileExtents(f *heifFile) [][2]int64 {
	if f.m.Loc == nil {
		return nil
	}
	n := 0
	for _, it := range f.m.Loc.Items {
		if it.ConstructionMethod == 0 && it.DataRefIndex == 0 && isHEIFImageType(f, it.ID) {
			n += len(it.Extents)
		}
	}
	ranges := make([][2]int64, 0, n)
	for _, it := range f.m.Loc.Items {
		if it.ConstructionMethod != 0 || it.DataRefIndex != 0 || !isHEIFImageType(f, it.ID) {
			continue
		}
		for _, e := range it.Extents {
			recordExtent(&ranges, it.BaseOffset, e.Offset, e.Length)
		}
	}
	return ranges
}

// refuseImageDataOverlap refuses an edit of bytes [at, end) that would
// touch one of the image data ranges.
func refuseImageDataOverlap(ranges [][2]int64, at, end int64) error {
	for _, rg := range ranges {
		if at < rg[1] && rg[0] < end {
			return errImageDataOverlap(at)
		}
	}
	return nil
}

// guardPlan refuses p where a byte it changes, in the source's offsets,
// is in one of the image data ranges, or where it changes the file's size
// and a range runs past the end.
func guardPlan(p *Plan, ranges [][2]int64) error {
	if len(ranges) == 0 {
		return nil
	}
	if p.NewSize != p.Size {
		// A range past the end of the file is cut there: bytes that a
		// write puts after the source's end fall into it. (A range
		// within the file moves with its bytes where data moves.)
		for _, rg := range ranges {
			if rg[1] > p.Size {
				return errImageDataOverlap(rg[0])
			}
		}
	}
	check := func(at, end int64) error { return refuseImageDataOverlap(ranges, at, end) }
	switch {
	case p.splice != nil:
		for _, s := range p.splice.splices {
			// An insert touches the byte after it.
			if err := check(s.at, s.at+max(s.del, 1)); err != nil {
				return err
			}
		}
	case p.f != nil:
		// A HEIF edit: the meta box replaced, patches at the new file's
		// offsets, and data appended.
		metaAt, oldEnd := p.f.meta.Offset, p.f.meta.End()
		newEnd := metaAt + int64(len(p.meta))
		if !bytes.Equal(p.meta, p.f.raw) {
			if err := check(metaAt, oldEnd); err != nil {
				return err
			}
		}
		for _, pt := range p.patches {
			at, end := pt.at, pt.at+int64(len(pt.data))
			switch {
			case at >= newEnd:
				at, end = at-newEnd+oldEnd, end-newEnd+oldEnd
			case end > metaAt:
				// In the meta box: its change is checked above.
				continue
			}
			if err := check(at, end); err != nil {
				return err
			}
		}
		if len(p.tail) > 0 {
			if err := check(p.Size, p.Size+1); err != nil {
				return err
			}
		}
	default:
		for _, pt := range p.patches {
			if err := check(pt.at, pt.at+int64(len(pt.data))); err != nil {
				return err
			}
		}
	}
	return nil
}

// heifImages is a HEIF file's coded image data as parsed, before an
// edit moves its item offsets: the extents in the file, as declared, and
// those in idat (construction method 1), as [start, end) in idat's
// payload, cut at its end.
type heifImages struct {
	ranges [][2]int64
	idat   [][2]int64
}

// snapshotImages records f's image data where the source has it.
func (f *heifFile) snapshotImages() {
	s := &heifImages{ranges: heifFileExtents(f)}
	if f.m.Loc != nil {
		if _, n := idatPayload(f.raw); n > 0 {
			for _, it := range f.m.Loc.Items {
				if it.ConstructionMethod != 1 || it.DataRefIndex != 0 || !isHEIFImageType(f, it.ID) {
					continue
				}
				for _, e := range it.Extents {
					start := it.BaseOffset + e.Offset
					if start < it.BaseOffset || start >= uint64(n) {
						continue
					}
					end := uint64(n)
					if e.Length != 0 && start+e.Length > start {
						end = min(end, start+e.Length)
					}
					s.idat = append(s.idat, [2]int64{int64(start), int64(end)})
				}
			}
		}
	}
	f.images = s
}

// guardHEIFPlan refuses a HEIF plan that touches the image data f had
// when it was parsed: an extent in the file, or the bytes of one in idat,
// which moves with the rewritten meta box but must keep its bytes.
func guardHEIFPlan(p *Plan) error {
	f := p.f
	if f == nil || f.images == nil {
		return nil
	}
	if err := guardPlan(p, f.images.ranges); err != nil {
		return err
	}
	if len(f.images.idat) == 0 {
		return nil
	}
	oldAt, oldN := idatPayload(f.raw)
	newAt, newN := idatPayload(p.meta)
	if newAt < 0 {
		return errImageDataOverlap(f.meta.Offset)
	}
	oldIdat, newIdat := f.raw[oldAt:oldAt+oldN], p.meta[newAt:newAt+newN]
	for _, e := range f.images.idat {
		if e[1] > newN || !bytes.Equal(oldIdat[e[0]:e[1]], newIdat[e[0]:e[1]]) {
			return errImageDataOverlap(f.meta.Offset + oldAt + e[0])
		}
		// An Exif or XMP patch written into idat, at the new file's
		// offsets.
		outAt, outEnd := f.meta.Offset+newAt+e[0], f.meta.Offset+newAt+e[1]
		for _, pt := range p.patches {
			if pt.at < outEnd && outAt < pt.at+int64(len(pt.data)) {
				return errImageDataOverlap(pt.at)
			}
		}
	}
	return nil
}

// isHEIFImageType tells whether item id is a coded image item.
func isHEIFImageType(f *heifFile, id uint32) bool {
	t := f.m.ItemTypes[id]
	for _, it := range heifImageTypes {
		if t == it {
			return true
		}
	}
	return false
}
