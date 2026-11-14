package mediameta

import (
	"bytes"
	"fmt"
	"io"

	"github.com/0x20h/go-media-metadata/internal/bmff"
)

// heifBrands are the ftyp brands of HEIF image files (ISO/IEC 23008-12,
// with the AVIF and MIAF ones).
var heifBrands = map[bmff.Type]bool{
	bmff.T("mif1"): true, bmff.T("mif2"): true, bmff.T("msf1"): true, bmff.T("miaf"): true,
	bmff.T("heic"): true, bmff.T("heix"): true, bmff.T("heim"): true, bmff.T("heis"): true,
	bmff.T("hevc"): true, bmff.T("hevx"): true, bmff.T("avif"): true, bmff.T("avis"): true,
}

// heifFile is a parsed HEIF file: its top-level boxes and its meta box,
// as read (raw) and parsed.
type heifFile struct {
	boxes []bmff.Box
	meta  bmff.Box
	raw   []byte
	m     *bmff.Meta
}

func parseHEIF(r io.ReaderAt, size int64) (*heifFile, error) {
	if size < 8 {
		return nil, errNotHEIF
	}
	boxes, err := bmff.ReadBoxes(r, 0, size)
	if err != nil {
		return nil, err
	}
	if len(boxes) == 0 || boxes[0].Type != bmff.T("ftyp") {
		return nil, errNotHEIF
	}
	if err := checkBrands(r, boxes[0]); err != nil {
		return nil, err
	}
	f := &heifFile{boxes: boxes}
	found := false
	for _, b := range boxes {
		if b.Type != bmff.T("meta") {
			continue
		}
		if found {
			return nil, fmt.Errorf("%w: two top-level meta boxes", ErrMalformed)
		}
		found = true
		f.meta = b
	}
	if !found {
		return nil, fmt.Errorf("%w: no meta box", errNotHEIF)
	}
	if f.meta.Size > bmff.MaxMeta {
		return nil, fmt.Errorf("%w: meta box of %d bytes", ErrUnsupported, f.meta.Size)
	}
	raw := make([]byte, f.meta.Size)
	if _, err := r.ReadAt(raw, f.meta.Offset); err != nil {
		return nil, fmt.Errorf("%w: reading the meta box: %v", ErrMalformed, err)
	}
	f.raw = raw
	if f.m, err = bmff.ParseMeta(raw); err != nil {
		return nil, err
	}
	if f.m.Handler != bmff.T("pict") {
		return nil, fmt.Errorf("%w: meta handler %v", errNotHEIF, f.m.Handler)
	}
	if !f.m.HasPrimary || f.m.Prop == nil || f.m.Loc == nil {
		return nil, fmt.Errorf("%w: no primary item, item properties or item locations", ErrMalformed)
	}
	return f, nil
}

func checkBrands(r io.ReaderAt, ftyp bmff.Box) error {
	n := ftyp.BodyLen()
	if n < 8 || n > 1024 {
		return fmt.Errorf("%w: ftyp of %d bytes", errNotHEIF, n)
	}
	b := make([]byte, n)
	if _, err := r.ReadAt(b, ftyp.Body()); err != nil {
		return fmt.Errorf("%w: reading ftyp: %v", ErrMalformed, err)
	}
	var t bmff.Type
	copy(t[:], b[:4])
	if heifBrands[t] {
		return nil
	}
	for i := 8; i+4 <= len(b); i += 4 {
		copy(t[:], b[i:i+4])
		if heifBrands[t] {
			return nil
		}
	}
	return errNotHEIF
}

// irotAngle reads an irot property: anticlockwise quarter turns.
func irotAngle(p bmff.Prop) (int, error) {
	if len(p.Raw) != 9 {
		return 0, fmt.Errorf("%w: irot of %d bytes", ErrMalformed, len(p.Raw))
	}
	return int(p.Raw[8] & 3), nil
}

// imirAxis reads an imir property's axis: 0 mirrors top-bottom (flips
// the image vertically), 1 left-right (flips it horizontally). That's how
// Apple's ImageIO writes and shows it and how libheif reads it: Apple
// writes orientation 2 as irot 0 and imir 1, 4 as irot 0 and imir 0.
func imirAxis(p bmff.Prop) (int, error) {
	if len(p.Raw) != 9 {
		return 0, fmt.Errorf("%w: imir of %d bytes", ErrMalformed, len(p.Raw))
	}
	return int(p.Raw[8] & 1), nil
}

// imirTransform is what an imir of axis does (see imirAxis).
func imirTransform(axis int) transform {
	if axis == 0 {
		return mirrorTopBottom
	}
	return mirrorLeftRight
}

// orientation is the primary item's orientation.
func (f *heifFile) orientation() (Orientation, error) {
	return f.itemOrientation(f.m.Primary)
}

// itemOrientation is an item's orientation from its transformative
// properties, applied in the order they're associated (ISO/IEC 23008-12
// 6.5.10, 6.5.12).
func (f *heifFile) itemOrientation(id uint32) (Orientation, error) {
	_, entry := f.m.Prop.Entry(id)
	if entry == nil {
		return 1, nil
	}
	t := identity
	for _, ref := range entry.Props {
		if ref.Index == 0 {
			continue
		}
		p := f.m.Prop.Props[ref.Index-1]
		switch p.Type {
		case bmff.T("irot"):
			a, err := irotAngle(p)
			if err != nil {
				return 0, err
			}
			t = t.then(rotationCW(-a))
		case bmff.T("imir"):
			axis, err := imirAxis(p)
			if err != nil {
				return 0, err
			}
			t = t.then(imirTransform(axis))
		}
	}
	return orientationOf(t), nil
}

// ReadOrientation returns how the image or video in r (size bytes) is
// shown.
//
// For HEIF it's the primary image's irot and imir properties as an
// Orientation, 1 when it has neither. The Exif Orientation tag isn't
// consulted: HEIF readers apply the properties.
//
// For JPEG and camera RAW (TIFF-based, or CR3) it's the Exif Orientation
// tag, 1 without one (or with a value outside 1 to 8, which readers
// ignore).
//
// For PNG and WebP it's the Exif Orientation tag too, and for a RAF the
// tag in its embedded JPEG. For JPEG XL it's the codestream header's
// orientation, which decoders apply. For QuickTime and MP4 it's the first
// video track's matrix: ErrUnsupported for a movie without a video track,
// or with a matrix that is no rotation or mirroring.
//
// A file of none of these formats gives ErrUnknownFormat.
func ReadOrientation(r io.ReaderAt, size int64) (Orientation, error) {
	f, err := parseHEIF(r, size)
	if err != nil {
		// JPEG, PNG, WebP and RAW are checked only once HEIF fails, so a
		// HEIF read costs no more than it did before they were supported.
		var x Exif
		switch {
		case isJPEG(r, size):
			x, err = readJPEGExif(r, size)
		case isPNG(r, size):
			x, err = readPNGExif(r, size)
		case isWebP(r, size):
			x, err = readWebPExif(r, size)
		case isJXL(r, size):
			return readJXLOrientation(r, size)
		case detectFormat(r, size) == formatMovie:
			v, err := ReadVideo(r, size)
			if err != nil {
				return 0, err
			}
			if !v.Orientation.Valid() {
				return 0, fmt.Errorf("%w: no video track, or a track matrix that isn't one of the eight orientations", ErrUnsupported)
			}
			return v.Orientation, nil
		default:
			rx, ok, rerr := readRAWExif(r, size)
			if !ok {
				// The HEIF parser's error says what's wrong with an ISO
				// base media file; anything else is no format of ours.
				if startsWithFtyp(r, size) {
					return 0, err
				}
				return 0, ErrUnknownFormat
			}
			x, err = rx, rerr
		}
		if err != nil {
			return 0, err
		}
		if x.Orientation == 0 {
			return 1, nil
		}
		return x.Orientation, nil
	}
	return f.orientation()
}

// SetRotation writes to dst the file in src (size bytes) with its image or
// video rotated degrees clockwise (0, 90, 180 or 270; other multiples of
// 90 are taken modulo 360), replacing any rotation it had and keeping a
// mirroring. Every format this package writes is taken; it's told from
// the file.
//
// For a HEIF file it's the primary image: its irot property is set so the
// image shows that, replacing any rotation it had. A mirroring (imir) is
// kept, and comes first as in the Exif numbering: a mirrored image
// rotated 90 shows orientation 7, Orientation.Degrees() of what it
// shows is degrees either way.
// The images that belong to it, its thumbnails and auxiliary images such
// as an HDR gain map, turn with it wherever they showed its orientation.
// The image data, and every byte outside the meta box other than item
// offsets the meta box's new size moves and the Exif Orientation tag, is
// copied as it is.
//
// Where the file has an Exif item whose first IFD has an Orientation tag,
// the tag is set to the orientation the file now shows, so readers that
// use it agree with those that apply irot. A missing tag stays missing.
//
// A JPEG file's orientation is its Exif Orientation tag, which is set
// (the mirroring kept, as above): changed in place where the first Exif
// segment has one, so the file keeps its size and only the tag's bytes
// change; in a new Exif segment after SOI and any JFIF segment where the
// file has none. A JPEG whose Exif segment lacks the tag gets it through
// EditExif, which adds the entry to IFD0 and keeps every other byte's
// offset.
//
// The other formats, for SetRotation and SetOrientation alike:
//
//   - PNG and WebP: the Exif Orientation tag, in the eXIf or EXIF chunk.
//   - Camera RAW (TIFF-based, CR3): IFD0's Orientation value where it
//     is, and an XMP packet's tiff:Orientation, in place. A file without
//     the tag, and a RAF, are refused (ErrUnsupported).
//   - QuickTime and MP4: each video track's matrix, in place.
//   - JPEG XL: refused (ErrUnsupported); its orientation is in the
//     codestream header.
//
// A file of no format this package reads gives ErrUnknownFormat.
//
// On an error nothing useful has been written to dst: write to a
// temporary file and rename it over the original only on success.
// PlanRotation computes the same edit and can apply it in place.
func SetRotation(src io.ReaderAt, size int64, dst io.Writer, degrees int) error {
	// HEIF is streamed without building a plan.
	r, err := planRotation(src, size, degrees)
	if err == nil {
		_, err = r.f.write(src, size, dst, r.meta, r.delta, r.patches)
		return err
	}
	if detectFormat(src, size) == formatHEIF {
		return err
	}
	p, err := PlanRotation(src, size, degrees)
	if err != nil {
		return err
	}
	_, err = p.WriteTo(dst)
	return err
}

// related are the images that belong to the primary one: its thumbnails
// (thmb) and auxiliary images (auxl: an HDR gain map, alpha, depth), which
// are shown or applied with it.
func (f *heifFile) related() []uint32 {
	var out []uint32
	seen := map[uint32]bool{f.m.Primary: true}
	for _, r := range f.m.Refs {
		if r.Type != bmff.T("thmb") && r.Type != bmff.T("auxl") {
			continue
		}
		for _, to := range r.To {
			if to == f.m.Primary && !seen[r.From] {
				seen[r.From] = true
				out = append(out, r.From)
			}
		}
	}
	return out
}

// irotRef is where an item's irot is associated: its index in the item's
// associations (-1 without one) and the property's index.
func (f *heifFile) irotRef(id uint32) (at int, idx uint16, err error) {
	p := f.m.Prop
	_, entry := p.Entry(id)
	if entry == nil {
		return -1, 0, nil
	}
	at = -1
	for i, ref := range entry.Props {
		if ref.Index != 0 && p.Props[ref.Index-1].Type == bmff.T("irot") {
			if at >= 0 {
				return -1, 0, fmt.Errorf("%w: two irot properties on item %d", ErrUnsupported, id)
			}
			at, idx = i, ref.Index
		}
	}
	return at, idx, nil
}

// setIrot gives the targets (the primary image first, then the related
// images in step with it) quarters anticlockwise turns. When they share
// one irot only they use, that irot is changed in place and nothing moves;
// otherwise each gets an irot of the new angle.
func (f *heifFile) setIrot(targets []uint32, quarters int) error {
	// One irot shared by exactly these images: change it in place.
	_, idx, err := f.irotRef(f.m.Primary)
	if err != nil {
		return err
	}
	if idx != 0 {
		shared := true
		for _, id := range targets[1:] {
			_, i, err := f.irotRef(id)
			if err != nil {
				return err
			}
			if i != idx {
				shared = false
			}
		}
		if shared && f.m.Prop.UsesOf(idx) == len(targets) {
			raw := append([]byte(nil), f.m.Prop.Props[idx-1].Raw...)
			raw[8] = raw[8]&^3 | byte(quarters)
			f.m.Prop.Props[idx-1].Raw = raw
			return nil
		}
	}
	for _, id := range targets {
		if err := f.setItemIrot(id, quarters); err != nil {
			return err
		}
	}
	return nil
}

// setItemIrot gives one item an irot of quarters anticlockwise turns. An
// irot only it uses is changed in place; a shared one is replaced by
// another (an existing one of that angle, or a new one); a rotation of 0
// without an irot changes nothing.
func (f *heifFile) setItemIrot(id uint32, quarters int) error {
	p := f.m.Prop
	ipma, entry := p.Entry(id)
	at, idx, err := f.irotRef(id)
	if err != nil {
		return err
	}
	if at >= 0 {
		cur, err := irotAngle(p.Props[idx-1])
		if err != nil {
			return err
		}
		if cur == quarters {
			return nil
		}
		if p.UsesOf(idx) == 1 {
			raw := append([]byte(nil), p.Props[idx-1].Raw...)
			raw[8] = raw[8]&^3 | byte(quarters)
			p.Props[idx-1].Raw = raw
			return nil
		}
		if quarters == 0 {
			entry.Props = append(entry.Props[:at], entry.Props[at+1:]...)
			return nil
		}
		ni, err := f.irotIndex(quarters)
		if err != nil {
			return err
		}
		entry.Props[at].Index = ni
		return f.widen(ipma, ni)
	}
	if quarters == 0 {
		return nil
	}
	ni, err := f.irotIndex(quarters)
	if err != nil {
		return err
	}
	if len(entry.Props) >= 255 {
		return fmt.Errorf("%w: item %d has 255 properties", ErrUnsupported, id)
	}
	// Transformative properties go after the descriptive ones, in the
	// order clap, irot, imir: before the first imir, else last.
	pos := len(entry.Props)
	for i, ref := range entry.Props {
		if ref.Index != 0 && p.Props[ref.Index-1].Type == bmff.T("imir") {
			pos = i
			break
		}
	}
	ref := bmff.PropRef{Essential: true, Index: ni}
	entry.Props = append(entry.Props[:pos], append([]bmff.PropRef{ref}, entry.Props[pos:]...)...)
	return f.widen(ipma, ni)
}

// irotIndex is the 1-based index of an irot of quarters turns: an existing
// one, else a new one appended to ipco.
func (f *heifFile) irotIndex(quarters int) (uint16, error) {
	p := f.m.Prop
	for i, pr := range p.Props {
		if pr.Type == bmff.T("irot") {
			if a, err := irotAngle(pr); err == nil && a == quarters && pr.Raw[8]&^3 == 0 {
				return uint16(i + 1), nil
			}
		}
	}
	if len(p.Props) >= 0x7FFF {
		return 0, fmt.Errorf("%w: too many properties", ErrUnsupported)
	}
	p.Props = append(p.Props, bmff.Prop{Type: bmff.T("irot"), Raw: []byte{0, 0, 0, 9, 'i', 'r', 'o', 't', byte(quarters)}})
	return uint16(len(p.Props)), nil
}

// widen switches an ipma to 15-bit property indices when idx needs them.
func (f *heifFile) widen(ipma *bmff.IPMA, idx uint16) error {
	if idx > 0x7F {
		ipma.Flags |= 1
	}
	return nil
}

// shiftOffsets moves the file offsets that point past the meta box by
// delta, the meta box's change in size. Data before it doesn't move; data
// inside it (other than through idat) or a movie box (whose chunk offsets
// this package doesn't rewrite) makes the change unsafe.
func (f *heifFile) shiftOffsets(delta int64) error {
	if _, ok := bmff.Find(f.boxes, bmff.T("moov")); ok {
		return fmt.Errorf("%w: a movie box with offsets this package doesn't move", ErrUnsupported)
	}
	start, end := uint64(f.meta.Offset), uint64(f.meta.End())
	for i := range f.m.Loc.Items {
		it := &f.m.Loc.Items[i]
		if it.ConstructionMethod != 0 || it.DataRefIndex != 0 {
			continue
		}
		for j := range it.Extents {
			e := &it.Extents[j]
			abs := it.BaseOffset + e.Offset
			if abs < it.BaseOffset {
				return fmt.Errorf("%w: item %d offset overflows", ErrMalformed, it.ID)
			}
			switch {
			case abs >= end:
				if f.m.Loc.OffsetSize == 0 && len(it.Extents) > 1 {
					return fmt.Errorf("%w: item %d has extents without offsets", ErrUnsupported, it.ID)
				}
			case abs >= start && e.Length != 0:
				return fmt.Errorf("%w: item %d points into the meta box", ErrUnsupported, it.ID)
			default:
				continue
			}
			if f.m.Loc.OffsetSize > 0 {
				n := int64(e.Offset) + delta
				if n < 0 {
					return fmt.Errorf("%w: item %d offset below its base", ErrUnsupported, it.ID)
				}
				e.Offset = uint64(n)
			} else {
				n := int64(it.BaseOffset) + delta
				if n < 0 || f.m.Loc.BaseOffsetSize == 0 {
					return fmt.Errorf("%w: item %d has no offset field to move", ErrUnsupported, it.ID)
				}
				it.BaseOffset = uint64(n)
			}
		}
	}
	return nil
}

// noDataInMeta checks that no image's data (construction method 0)
// overlaps the meta box, which an edit rewrites: data starting inside it,
// or before it and running into it (or to the end of the file, across
// it), would change with it. Exif and XMP items aren't images: their
// bytes there may change, as an edit may change them anyway.
func (f *heifFile) noDataInMeta() error {
	start, end := uint64(f.meta.Offset), uint64(f.meta.End())
	for _, it := range f.m.Loc.Items {
		t := f.m.ItemTypes[it.ID]
		if t == bmff.T("Exif") || t == bmff.T("mime") || it.ConstructionMethod != 0 || it.DataRefIndex != 0 {
			continue
		}
		for _, e := range it.Extents {
			abs := it.BaseOffset + e.Offset
			if abs < it.BaseOffset || abs >= end {
				continue
			}
			if e.Length == 0 || abs+e.Length > start || abs+e.Length < abs {
				return fmt.Errorf("%w: item %d's data overlaps the meta box", ErrUnsupported, it.ID)
			}
		}
	}
	return nil
}

// patch replaces bytes at an offset of the output.
type patch struct {
	at   int64
	data []byte
}

// write copies src to dst with the meta box replaced and the patches
// applied, and answers how many bytes it wrote.
func (f *heifFile) write(src io.ReaderAt, size int64, dst io.Writer, meta []byte, delta int64, patches []patch) (int64, error) {
	w := &patchWriter{w: dst, patches: patches}
	if _, err := io.Copy(w, io.NewSectionReader(src, 0, f.meta.Offset)); err != nil {
		return w.written, err
	}
	if _, err := w.Write(meta); err != nil {
		return w.written, err
	}
	if _, err := io.Copy(w, io.NewSectionReader(src, f.meta.End(), size-f.meta.End())); err != nil {
		return w.written, err
	}
	if w.written != size+delta {
		return w.written, fmt.Errorf("mediameta: wrote %d bytes, want %d", w.written, size+delta)
	}
	return w.written, nil
}

// patchWriter writes through, replacing the bytes at the patches' output
// offsets.
type patchWriter struct {
	w       io.Writer
	patches []patch
	written int64
}

func (p *patchWriter) Write(b []byte) (int, error) {
	start := p.written
	var buf []byte
	end := start + int64(len(b))
	for _, pt := range p.patches {
		lo, hi := max(pt.at, start), min(pt.at+int64(len(pt.data)), end)
		if lo >= hi {
			continue
		}
		if buf == nil {
			buf = bytes.Clone(b)
		}
		copy(buf[lo-start:hi-start], pt.data[lo-pt.at:hi-pt.at])
	}
	if buf == nil {
		buf = b
	}
	n, err := p.w.Write(buf)
	p.written += int64(n)
	return n, err
}
