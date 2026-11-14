package mediameta

import (
	"bytes"
	"encoding/binary"
	"io"

	"github.com/0x20h/go-media-metadata/internal/bmff"
)

// rafMagic starts a Fujifilm RAF file.
var rafMagic = []byte("FUJIFILMCCD-RAW ")

// openRAF finds the JPEG a Fujifilm RAF embeds: its offset and length,
// big-endian at bytes 84 and 88 of the header. ok is false for a file that
// isn't a RAF, or whose JPEG isn't in the file.
func openRAF(r io.ReaderAt, size int64) (at, n int64, isRAF, ok bool) {
	var h [92]byte
	if size < int64(len(h)) {
		return 0, 0, false, false
	}
	if _, err := r.ReadAt(h[:], 0); err != nil || !bytes.HasPrefix(h[:], rafMagic) {
		return 0, 0, false, false
	}
	at, n = int64(binary.BigEndian.Uint32(h[84:])), int64(binary.BigEndian.Uint32(h[88:]))
	if at < int64(len(h)) || n < 4 || at+n > size || !startsJPEG(r, at) {
		return 0, 0, true, false
	}
	return at, n, true, true
}

// readRAFExif reads a RAF's Exif from the JPEG it embeds.
func readRAFExif(r io.ReaderAt, size int64) (Exif, bool, error) {
	at, n, isRAF, ok := openRAF(r, size)
	if !isRAF {
		return Exif{}, false, nil
	}
	if !ok {
		return Exif{}, true, nil
	}
	x, err := readJPEGExif(io.NewSectionReader(r, at, n), n)
	return x, true, err
}

// readRAFXMP reads a RAF's XMP from the JPEG it embeds.
func readRAFXMP(r io.ReaderAt, size int64) ([]byte, bool, error) {
	at, n, isRAF, ok := openRAF(r, size)
	if !isRAF {
		return nil, false, nil
	}
	if !ok {
		return nil, true, nil
	}
	b, err := readJPEGXMP(io.NewSectionReader(r, at, n), n)
	return b, true, err
}

// jpegAndThumbnail are the previews of a JPEG a RAW embeds at (n bytes):
// the JPEG itself and, where its Exif has one, the thumbnail of its IFD1.
// Offsets are the file's.
func jpegAndThumbnail(r io.ReaderAt, at, n int64, add func(off, n int64)) {
	add(at, n)
	sec := io.NewSectionReader(r, at, n)
	f, err := parseJPEG(sec, n)
	if err != nil {
		return
	}
	seg, payload, ok, err := f.exifSegment(sec)
	if err != nil || !ok || len(payload) < len(exifHeader)+8 {
		return
	}
	data := payload[len(exifHeader):]
	t, first, ok := newTIFF(data)
	if !ok || int64(first)+2 > int64(len(data)) {
		return
	}
	next := int64(first) + 2 + 12*int64(t.order.Uint16(data[first:]))
	if next+4 > int64(len(data)) {
		return
	}
	ifd1 := t.ifd(t.order.Uint32(data[next:]))
	o, ok1 := t.uint(ifd1[tagJPEGOffset])
	l, ok2 := t.uint(ifd1[tagJPEGLength])
	if ok1 && ok2 {
		add(at+seg.body+int64(len(exifHeader))+int64(o), int64(l))
	}
}

// The uuid box holding a CR3's preview (PRVW).
var cr3PreviewUUID = [16]byte{0xea, 0xf4, 0x2b, 0x5e, 0x1c, 0x98, 0x4b, 0x88, 0xb9, 0xfb, 0xb7, 0xdc, 0x40, 0x6e, 0x4d, 0x16}

// cr3Previews finds a CR3's JPEGs: the thumbnail (THMB, in the metadata
// uuid box), the preview (PRVW, in its own uuid box), and the full-size
// JPEG, which is the first sample of a track (found through its sample
// tables). Each of the first two has a 16-byte header before its JPEG,
// whose length it gives.
func cr3Previews(r io.ReaderAt, size int64, add func(off, n int64)) error {
	top, err := bmff.ReadBoxes(r, 0, size)
	if err != nil {
		return err
	}
	uuidOf := func(b bmff.Box) [16]byte {
		var u [16]byte
		if b.Type == bmff.T("uuid") && b.HeaderLen >= 24 {
			_, _ = r.ReadAt(u[:], b.Body()-16)
		}
		return u
	}
	// headed adds the JPEG after a box's 16-byte header, its length at
	// lenAt in the payload.
	headed := func(b bmff.Box, lenAt int64) {
		var h [16]byte
		if b.BodyLen() < 16+4 {
			return
		}
		if _, err := r.ReadAt(h[:], b.Body()); err != nil {
			return
		}
		n := int64(binary.BigEndian.Uint32(h[lenAt:]))
		if n <= 0 || n > b.BodyLen()-16 {
			n = b.BodyLen() - 16
		}
		add(b.Body()+16, n)
	}
	for _, b := range top {
		switch {
		case b.Type == bmff.T("uuid") && uuidOf(b) == cr3PreviewUUID:
			// 8 bytes, then the PRVW box.
			kids, err := bmff.ReadBoxes(r, b.Body()+8, b.End())
			if err != nil {
				continue
			}
			if p, ok := bmff.Find(kids, bmff.T("PRVW")); ok {
				headed(p, 12)
			}
		case b.Type == bmff.T("moov"):
			kids, err := bmff.ReadBoxes(r, b.Body(), b.End())
			if err != nil {
				return err
			}
			for _, k := range kids {
				switch {
				case k.Type == bmff.T("uuid") && uuidOf(k) == cr3MetaUUID:
					meta, err := bmff.ReadBoxes(r, k.Body(), k.End())
					if err != nil {
						continue
					}
					if th, ok := bmff.Find(meta, bmff.T("THMB")); ok {
						headed(th, 8)
					}
				case k.Type == bmff.T("trak"):
					if off, n, ok := firstSample(r, k); ok {
						add(off, n)
					}
				}
			}
		}
	}
	return nil
}

// firstSample is where a track's first sample is: its first chunk's
// offset (stco or co64) and its size (stsz).
func firstSample(r io.ReaderAt, trak bmff.Box) (off, n int64, ok bool) {
	stbl, ok := boxPath(r, trak, "mdia", "minf", "stbl")
	if !ok {
		return 0, 0, false
	}
	kids, err := bmff.ReadBoxes(r, stbl.Body(), stbl.End())
	if err != nil {
		return 0, 0, false
	}
	var b [16]byte
	if sz, ok := bmff.Find(kids, bmff.T("stsz")); ok && sz.BodyLen() >= 16 {
		if _, err := r.ReadAt(b[:], sz.Body()); err != nil {
			return 0, 0, false
		}
		n = int64(binary.BigEndian.Uint32(b[4:]))
		if n == 0 && binary.BigEndian.Uint32(b[8:]) > 0 {
			n = int64(binary.BigEndian.Uint32(b[12:]))
		}
	}
	if co, ok := bmff.Find(kids, bmff.T("stco")); ok && co.BodyLen() >= 12 {
		if _, err := r.ReadAt(b[:12], co.Body()); err == nil && binary.BigEndian.Uint32(b[4:]) > 0 {
			off = int64(binary.BigEndian.Uint32(b[8:]))
		}
	} else if co, ok := bmff.Find(kids, bmff.T("co64")); ok && co.BodyLen() >= 16 {
		if _, err := r.ReadAt(b[:16], co.Body()); err == nil && binary.BigEndian.Uint32(b[4:]) > 0 {
			off = int64(binary.BigEndian.Uint64(b[8:]))
		}
	}
	return off, n, off > 0 && n > 0
}

// boxPath follows child boxes of these types from b.
func boxPath(r io.ReaderAt, b bmff.Box, types ...string) (bmff.Box, bool) {
	for _, t := range types {
		kids, err := bmff.ReadBoxes(r, b.Body(), b.End())
		if err != nil {
			return bmff.Box{}, false
		}
		var ok bool
		if b, ok = bmff.Find(kids, bmff.T(t)); !ok {
			return bmff.Box{}, false
		}
	}
	return b, true
}
