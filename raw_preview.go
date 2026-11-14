package mediameta

import (
	"io"
	"sort"
)

// Preview is a JPEG image a camera RAW file embeds for viewing: a
// thumbnail, a screen-sized preview, or a full-size rendering.
type Preview struct {
	// Offset and Length are where the JPEG is in the file.
	Offset, Length int64
	// Width and Height are the JPEG's own, from its frame header.
	Width, Height int
}

// TIFF tags that locate previews.
const (
	tagNewSubfileType  = 0x00FE
	tagCompression     = 0x0103
	tagStripOffsets    = 0x0111
	tagStripByteCounts = 0x0117
	tagSubIFDs         = 0x014A
	tagJPEGOffset      = 0x0201 // JPEGInterchangeFormat
	tagJPEGLength      = 0x0202 // JPEGInterchangeFormatLength
)

// Limits on the IFDs a preview search follows.
const (
	maxIFDChain = 8
	maxSubIFDs  = 16
)

// ReadPreviews finds the JPEG previews a camera RAW file embeds, largest
// first. For a TIFF-based one (DNG, CR2, NEF, ARW, ORF, RW2) it looks in
// IFD0, the IFDs chained after it, and the SubIFDs of each, at:
//
//   - a JPEGInterchangeFormat and its length (tags 513 and 514), as CR2,
//     NEF and ARW have their previews;
//   - a single strip compressed as JPEG (Compression 6, 7 or 34892), as
//     DNG has its previews in SubIFDs and CR2 its full-size one in IFD0;
//   - an RW2's JpgFromRaw (tag 46) and the thumbnail in its Exif.
//
// For a Fujifilm RAF it takes the JPEG the header points to and the
// thumbnail in its Exif; for a Canon CR3, the THMB and PRVW boxes' JPEGs
// and the first sample of each track (the full-size JPEG).
//
// A candidate counts only where it is a baseline or progressive JPEG
// inside the file: its frame header (read from its first few KB) gives
// the size. That leaves out the raw image data some files store as
// lossless JPEG. Only IFDs, boxes and the head of each candidate are read.
//
// A file without a preview gives none and no error; a file of none of
// these formats gives ErrNotRAW.
func ReadPreviews(r io.ReaderAt, size int64) ([]Preview, error) {
	var out []Preview
	seen := map[int64]bool{}
	add := func(at, length int64) {
		if at <= 0 || length < 4 || at+length > size || seen[at] {
			return
		}
		w, h, ok := jpegFrameSize(r, at, length)
		if !ok {
			return
		}
		seen[at] = true
		out = append(out, Preview{Offset: at, Length: length, Width: w, Height: h})
	}
	if at, n, isRAF, ok := openRAF(r, size); isRAF {
		if ok {
			jpegAndThumbnail(r, at, n, add)
		}
		return sortPreviews(out), nil
	}
	if _, isCR3, err := openCR3(r, size); isCR3 {
		if err != nil {
			return nil, err
		}
		if err := cr3Previews(r, size, add); err != nil {
			return nil, err
		}
		return sortPreviews(out), nil
	}
	t, ok := openRAWTIFF(r, size)
	if !ok {
		return nil, ErrNotRAW
	}
	visit := func(ifd map[uint16]field) {
		if o, ok := t.uint(ifd[tagJPEGOffset]); ok {
			if n, ok := t.uint(ifd[tagJPEGLength]); ok {
				add(int64(o), int64(n))
			}
		}
		switch c, _ := t.uint(ifd[tagCompression]); c {
		case 6, 7, 34892:
			offs, lens := t.uints(ifd[tagStripOffsets]), t.uints(ifd[tagStripByteCounts])
			if len(offs) == 1 && len(lens) == 1 {
				add(int64(offs[0]), int64(lens[0]))
			}
		}
	}
	visited := map[uint32]bool{}
	off := t.first
	for range maxIFDChain {
		if off == 0 || visited[off] {
			break
		}
		visited[off] = true
		ifd := t.ifd(off)
		if ifd == nil {
			break
		}
		visit(ifd)
		for i, sub := range t.uints(ifd[tagSubIFDs]) {
			if i >= maxSubIFDs || visited[sub] {
				break
			}
			visited[sub] = true
			if s := t.ifd(sub); s != nil {
				visit(s)
			}
		}
		off = t.nextIFD(off)
	}
	// A Panasonic RW2's JpgFromRaw, with the thumbnail in its Exif.
	if sec, ok := t.jpgFromRaw(); ok {
		_, at, n := sec.Outer()
		jpegAndThumbnail(r, at, n, add)
	}
	return sortPreviews(out), nil
}

// sortPreviews orders previews largest first: by pixels, then length.
func sortPreviews(out []Preview) []Preview {
	sort.SliceStable(out, func(i, j int) bool {
		ai, aj := out[i].Width*out[i].Height, out[j].Width*out[j].Height
		if ai != aj {
			return ai > aj
		}
		return out[i].Length > out[j].Length
	})
	return out
}

// uints are a SHORT, LONG or IFD field's values; nil for another type.
func (t tiffReader) uints(f field) []uint32 {
	var w int
	switch f.typ {
	case typeShort:
		w = 2
	case typeLong, typeIFD:
		w = 4
	default:
		return nil
	}
	if len(f.value) < w*f.count {
		return nil
	}
	out := make([]uint32, f.count)
	for i := range out {
		if w == 2 {
			out[i] = uint32(t.order.Uint16(f.value[2*i:]))
		} else {
			out[i] = t.order.Uint32(f.value[4*i:])
		}
	}
	return out
}

// nextIFD is the offset of the IFD after the one at off, 0 at the end of
// the chain or for one that can't be read.
func (t *rawTIFF) nextIFD(off uint32) uint32 {
	var nb [2]byte
	if _, err := t.r.ReadAt(nb[:], int64(off)); err != nil {
		return 0
	}
	at := int64(off) + 2 + 12*int64(t.order.Uint16(nb[:]))
	var next [4]byte
	if at+4 > t.size {
		return 0
	}
	if _, err := t.r.ReadAt(next[:], at); err != nil {
		return 0
	}
	return t.order.Uint32(next[:])
}

// How much of a preview is read to find its frame header: its marker
// segments come first, usually within a few KB, but an Exif segment with
// a maker note can push it further.
const (
	previewHead    = 4 << 10
	maxPreviewHead = 128 << 10
)

// jpegFrameSize reads the frame header of the JPEG at off (n bytes): its
// width and height, ok only for a baseline, extended or progressive
// Huffman JPEG (SOF0, SOF1, SOF2), not a lossless one. It reads the first
// few KB, and more only when the header lies further in.
func jpegFrameSize(r io.ReaderAt, off, n int64) (w, h int, ok bool) {
	for _, limit := range []int64{previewHead, maxPreviewHead} {
		head := make([]byte, min(n, limit))
		if _, err := r.ReadAt(head, off); err != nil && err != io.EOF {
			return 0, 0, false
		}
		w, h, ok, more := frameSize(head)
		if !more || int64(len(head)) == n {
			return w, h, ok
		}
	}
	return 0, 0, false
}

// frameSize parses a JPEG's marker segments in head up to its frame
// header; more is true when head ends before it.
func frameSize(head []byte) (w, h int, ok, more bool) {
	if len(head) < 4 || head[0] != 0xFF || head[1] != markerSOI {
		return 0, 0, false, false
	}
	for at := 2; ; {
		if at+4 > len(head) {
			return 0, 0, false, true
		}
		if head[at] != 0xFF {
			return 0, 0, false, false
		}
		m := head[at+1]
		if m == 0xFF {
			at++
			continue
		}
		seg := int(head[at+2])<<8 | int(head[at+3])
		if seg < 2 {
			return 0, 0, false, false
		}
		switch m {
		case 0xC0, 0xC1, 0xC2:
			if at+9 > len(head) {
				return 0, 0, false, true
			}
			h = int(head[at+5])<<8 | int(head[at+6])
			w = int(head[at+7])<<8 | int(head[at+8])
			return w, h, w > 0 && h > 0, false
		case 0xC3, 0xC5, 0xC6, 0xC7, 0xC9, 0xCA, 0xCB, 0xCD, 0xCE, 0xCF, markerSOS, markerEOI:
			// Lossless, hierarchical or arithmetic, or no frame first.
			return 0, 0, false, false
		}
		at += 2 + seg
	}
}
