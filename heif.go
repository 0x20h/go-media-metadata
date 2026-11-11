package mediameta

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	"github.com/0x20h/go-media-metadata/internal/bmff"
)

var (
	// ErrNotHEIF is returned for a file that isn't a HEIF image file.
	ErrNotHEIF = errors.New("not a HEIF image file")
	// ErrUnsupported is returned for a HEIF file this package can't
	// change safely: the file is left as it is rather than risk a wrong
	// result.
	ErrUnsupported = errors.New("unsupported HEIF structure")
	// ErrMalformed is returned for input that breaks the box structure.
	ErrMalformed = bmff.ErrMalformed
)

// heifBrands are the ftyp brands of HEIF image files (ISO/IEC 23008-12,
// with the AVIF and MIAF ones).
var heifBrands = map[bmff.Type]bool{
	bmff.T("mif1"): true, bmff.T("mif2"): true, bmff.T("msf1"): true, bmff.T("miaf"): true,
	bmff.T("heic"): true, bmff.T("heix"): true, bmff.T("heim"): true, bmff.T("heis"): true,
	bmff.T("hevc"): true, bmff.T("hevx"): true, bmff.T("avif"): true, bmff.T("avis"): true,
}

// heifFile is a parsed HEIF file: its top-level boxes and its meta box.
type heifFile struct {
	boxes []bmff.Box
	meta  bmff.Box
	m     *bmff.Meta
}

func parseHEIF(r io.ReaderAt, size int64) (*heifFile, error) {
	if size < 8 {
		return nil, ErrNotHEIF
	}
	boxes, err := bmff.ReadBoxes(r, 0, size)
	if err != nil {
		return nil, err
	}
	if len(boxes) == 0 || boxes[0].Type != bmff.T("ftyp") {
		return nil, ErrNotHEIF
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
		return nil, fmt.Errorf("%w: no meta box", ErrNotHEIF)
	}
	if f.meta.Size > bmff.MaxMeta {
		return nil, fmt.Errorf("%w: meta box of %d bytes", ErrUnsupported, f.meta.Size)
	}
	raw := make([]byte, f.meta.Size)
	if _, err := r.ReadAt(raw, f.meta.Offset); err != nil {
		return nil, fmt.Errorf("%w: reading the meta box: %v", ErrMalformed, err)
	}
	if f.m, err = bmff.ParseMeta(raw); err != nil {
		return nil, err
	}
	if f.m.Handler != bmff.T("pict") {
		return nil, fmt.Errorf("%w: meta handler %v", ErrNotHEIF, f.m.Handler)
	}
	if !f.m.HasPrimary || f.m.Prop == nil || f.m.Loc == nil {
		return nil, fmt.Errorf("%w: no primary item, item properties or item locations", ErrMalformed)
	}
	return f, nil
}

func checkBrands(r io.ReaderAt, ftyp bmff.Box) error {
	n := ftyp.BodyLen()
	if n < 8 || n > 1024 {
		return fmt.Errorf("%w: ftyp of %d bytes", ErrNotHEIF, n)
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
	return ErrNotHEIF
}

// irotAngle reads an irot property: anticlockwise quarter turns.
func irotAngle(p bmff.Prop) (int, error) {
	if len(p.Raw) != 9 {
		return 0, fmt.Errorf("%w: irot of %d bytes", ErrMalformed, len(p.Raw))
	}
	return int(p.Raw[8] & 3), nil
}

// imirAxis reads an imir property: 0 mirrors about the vertical axis
// (left-right), 1 about the horizontal one (top-bottom).
func imirAxis(p bmff.Prop) (int, error) {
	if len(p.Raw) != 9 {
		return 0, fmt.Errorf("%w: imir of %d bytes", ErrMalformed, len(p.Raw))
	}
	return int(p.Raw[8] & 1), nil
}

// orientation is the primary item's orientation from its transformative
// properties, applied in the order they're associated (ISO/IEC 23008-12
// 6.5.10, 6.5.12).
func (f *heifFile) orientation() (Orientation, error) {
	_, entry := f.m.Prop.Entry(f.m.Primary)
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
			if axis == 0 {
				t = t.then(mirrorLeftRight)
			} else {
				t = t.then(mirrorTopBottom)
			}
		}
	}
	return orientationOf(t), nil
}

// ReadOrientation returns how the primary image of the HEIF file in r
// (size bytes) is shown: its irot and imir properties as an Orientation,
// 1 when it has neither. The Exif Orientation tag isn't consulted: HEIF
// readers apply the properties.
func ReadOrientation(r io.ReaderAt, size int64) (Orientation, error) {
	f, err := parseHEIF(r, size)
	if err != nil {
		return 0, err
	}
	return f.orientation()
}

// SetRotation writes to dst the HEIF file in src (size bytes) with its
// primary image rotated degrees clockwise (0, 90, 180 or 270; other
// multiples of 90 are taken modulo 360): its irot property is set to
// that, replacing any rotation it had, and a mirroring (imir) is kept.
// The image data, and every byte outside the meta box other than item
// offsets the meta box's new size moves and the Exif Orientation tag, is
// copied as it is.
//
// Where the file has an Exif item whose first IFD has an Orientation tag,
// the tag is set to the orientation the file now shows, so readers that
// use it agree with those that apply irot. A missing tag stays missing.
//
// On an error nothing useful has been written to dst: write to a
// temporary file and rename it over the original only on success.
func SetRotation(src io.ReaderAt, size int64, dst io.Writer, degrees int) error {
	if degrees%90 != 0 {
		return fmt.Errorf("mediameta: rotation of %d°: not a multiple of 90", degrees)
	}
	f, err := parseHEIF(src, size)
	if err != nil {
		return err
	}
	quarters := ((-degrees/90)%4 + 4) % 4 // irot counts anticlockwise
	if err := f.setIrot(quarters); err != nil {
		return err
	}
	newMeta, err := f.m.Encode()
	if err != nil {
		return err
	}
	delta := int64(len(newMeta)) - f.meta.Size
	shown, err := f.orientation()
	if err != nil {
		return err
	}
	// Before the offsets move: the patches are placed from the old ones.
	// idat's place in the new meta box doesn't depend on offset values.
	patches, err := f.exifOrientationPatches(src, size, newMeta, delta, shown)
	if err != nil {
		return err
	}
	if delta != 0 {
		if err := f.shiftOffsets(delta); err != nil {
			return err
		}
		if newMeta, err = f.m.Encode(); err != nil {
			if bmff.ErrFieldTooSmall(err) {
				return fmt.Errorf("%w: an item offset outgrows its field", ErrUnsupported)
			}
			return err
		}
		if int64(len(newMeta))-f.meta.Size != delta {
			return fmt.Errorf("%w: the meta box changed size twice", ErrUnsupported)
		}
	}
	return f.write(src, size, dst, newMeta, delta, patches)
}

// setIrot gives the primary item an irot of quarters anticlockwise turns.
// An irot only it uses is changed in place, so nothing moves; a shared one
// is replaced by another (an existing one of that angle, or a new one); a
// rotation of 0 without an irot changes nothing.
func (f *heifFile) setIrot(quarters int) error {
	p := f.m.Prop
	ipma, entry := p.Entry(f.m.Primary)
	if entry == nil {
		return fmt.Errorf("%w: the primary item has no properties", ErrUnsupported)
	}
	at := -1
	for i, ref := range entry.Props {
		if ref.Index != 0 && p.Props[ref.Index-1].Type == bmff.T("irot") {
			if at >= 0 {
				return fmt.Errorf("%w: two irot properties on the primary item", ErrUnsupported)
			}
			at = i
		}
	}
	if at >= 0 {
		idx := entry.Props[at].Index
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
		return fmt.Errorf("%w: the primary item has 255 properties", ErrUnsupported)
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

// patch replaces bytes at an offset of the output.
type patch struct {
	at   int64
	data []byte
}

// write copies src to dst with the meta box replaced and the patches
// applied.
func (f *heifFile) write(src io.ReaderAt, size int64, dst io.Writer, meta []byte, delta int64, patches []patch) error {
	w := &patchWriter{w: dst, patches: patches}
	if _, err := io.Copy(w, io.NewSectionReader(src, 0, f.meta.Offset)); err != nil {
		return err
	}
	if _, err := w.Write(meta); err != nil {
		return err
	}
	if _, err := io.Copy(w, io.NewSectionReader(src, f.meta.End(), size-f.meta.End())); err != nil {
		return err
	}
	if w.written != size+delta {
		return fmt.Errorf("mediameta: wrote %d bytes, want %d", w.written, size+delta)
	}
	return nil
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
	for _, pt := range p.patches {
		for i, c := range pt.data {
			at := pt.at + int64(i) - start
			if at < 0 || at >= int64(len(b)) {
				continue
			}
			if buf == nil {
				buf = bytes.Clone(b)
			}
			buf[at] = c
		}
	}
	if buf == nil {
		buf = b
	}
	n, err := p.w.Write(buf)
	p.written += int64(n)
	return n, err
}
