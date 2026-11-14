package mediameta

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"io"
	"slices"

	"github.com/0x20h/go-media-metadata/internal/bmff"
)

// ErrNotInPlace is returned by Plan.Apply for an edit that changes the
// file's size: only Plan.WriteTo can make that file.
var ErrNotInPlace = errors.New("mediameta: the edit changes the file's size")

// Patch replaces len(Data) bytes at Offset.
type Patch struct {
	Offset int64
	Data   []byte
}

// Plan is an edit of one file, computed against its source. It can be
// written as a new file (WriteTo), or, when InPlace, applied to a copy of
// the source (Apply): a clone of the file edited in place keeps its
// unchanged blocks shared, and the write doesn't depend on the file's
// size.
type Plan struct {
	// InPlace is true when the edit keeps the file's size and every
	// change is in Patches. Otherwise only WriteTo can make the result.
	InPlace bool
	// Patches are sorted by Offset, don't overlap and lie in [0, Size)
	// when InPlace. They are also the ranges a caller verifies. Empty
	// when the edit changes nothing, or when it isn't InPlace.
	Patches []Patch
	// Size is the source's size, NewSize the result's.
	Size, NewSize int64

	src     io.ReaderAt
	f       *heifFile
	meta    []byte  // the new meta box
	patches []patch // the Exif patches, at output offsets
	tail    []byte  // appended after the source's last byte
	jpeg    *jpegEdit
	splice  *spliceEdit // a PNG's or WebP's edit
	chain   *Plan       // PlanMetadata's last part, on the file the parts before it make
}

// patchMergeGap is how many unchanged bytes may separate two runs of
// changed ones and still make one patch: fewer, longer writes.
const patchMergeGap = 8

// PlanRotation is SetRotation's edit, computed but not written: the file
// in src (size bytes), of any format SetRotation takes, rotated degrees
// clockwise. It reads the metadata, never the image or media data. A
// camera RAW's and a movie's edit is always in place.
//
// For a HEIF file the edit is in place when the meta box keeps its size: in the usual
// case (an irot only the rotated images use, as in iPhone and ImageIO
// files) one byte of it and the two bytes of the Exif Orientation tag.
// An appended irot, a widened ipma, or item offsets that move change the
// size. For a JPEG it's in place where the Exif Orientation tag exists:
// one patch of the tag's bytes.
func PlanRotation(src io.ReaderAt, size int64, degrees int) (*Plan, error) {
	if degrees%90 != 0 {
		return nil, fmt.Errorf("mediameta: rotation of %d°: not a multiple of 90", degrees)
	}
	// HEIF first, at no extra cost; the format is looked at only when
	// that fails.
	r, herr := planRotation(src, size, degrees)
	if herr == nil {
		return r.plan(src, size), nil
	}
	switch detectFormat(src, size) {
	case formatHEIF:
		return nil, herr
	case formatJPEG:
		e, _, err := jpegRotation(src, size, degrees)
		if err != nil {
			return nil, err
		}
		return e.plan(src, size), nil
	case formatMovie:
		return planVideoRotation(src, size, degrees)
	case formatUnknown:
		return nil, ErrUnknownFormat
	}
	o, err := ReadOrientation(src, size)
	if err != nil {
		return nil, err
	}
	return PlanOrientation(src, size, orientationFor(o.Mirrored(), degrees))
}

// PlanOrientation is SetOrientation's edit, computed but not written, as
// PlanRotation is SetRotation's. For a HEIF file it is in place when the meta box keeps
// its size: a rotation that keeps the mirroring (or its absence) changes
// the irot byte as PlanRotation does; adding or removing a mirror changes
// an association, and with it the size.
func PlanOrientation(src io.ReaderAt, size int64, o Orientation) (*Plan, error) {
	if !o.Valid() {
		return nil, fmt.Errorf("mediameta: orientation %d: not 1 to 8", uint8(o))
	}
	r, herr := planOrientation(src, size, o)
	if herr == nil {
		return r.plan(src, size), nil
	}
	switch detectFormat(src, size) {
	case formatHEIF:
		return nil, herr
	case formatJPEG:
		e, _, err := jpegOrientation(src, size, o)
		if err != nil {
			return nil, err
		}
		return e.plan(src, size), nil
	case formatPNG, formatWebP:
		return planExifEdit(src, size, ExifEdit{Orientation: o})
	case formatRAW:
		if _, _, isRAF, _ := openRAF(src, size); isRAF {
			return nil, fmt.Errorf("%w: a RAF isn't written", ErrUnsupported)
		}
		return planRAWOrientation(src, size, o)
	case formatMovie:
		return planVideoOrientation(src, size, o)
	case formatJXL:
		return nil, fmt.Errorf("%w: a JPEG XL codestream's orientation isn't written", ErrUnsupported)
	}
	return nil, ErrUnknownFormat
}

// plan is the Plan of an edit.
func (r rotation) plan(src io.ReaderAt, size int64) *Plan {
	p := &Plan{Size: size, NewSize: size + r.delta, src: src, f: r.f, meta: r.meta, patches: r.patches}
	if r.delta == 0 {
		p.InPlace = true
		p.Patches = inPlacePatches(r.f.meta.Offset, r.f.raw, r.meta, r.patches)
	}
	return p
}

// rotation is an orientation edit as WriteTo needs it: the parsed file (raw
// still the source's meta box), the new meta box, its change in size, and
// the Exif patches at output offsets.
type rotation struct {
	f       *heifFile
	meta    []byte
	delta   int64
	patches []patch
}

// planRotation computes a rotation's edit, shared by PlanRotation and
// SetRotation; SetRotation streams it without the patches a Plan lists.
// The mirroring stays as it is: a mirrored image is mirrored, then
// rotated degrees clockwise (the Exif numbering's order).
func planRotation(src io.ReaderAt, size int64, degrees int) (rotation, error) {
	if degrees%90 != 0 {
		return rotation{}, fmt.Errorf("mediameta: rotation of %d°: not a multiple of 90", degrees)
	}
	return planEdit(src, size, func(f *heifFile) error {
		before, err := f.orientation()
		if err != nil {
			return err
		}
		return f.setOrientation(orientationFor(before.Mirrored(), degrees))
	})
}

// planOrientation computes the edit that shows the primary image at o.
func planOrientation(src io.ReaderAt, size int64, o Orientation) (rotation, error) {
	if !o.Valid() {
		return rotation{}, fmt.Errorf("mediameta: orientation %d: not 1 to 8", uint8(o))
	}
	return planEdit(src, size, func(f *heifFile) error { return f.setOrientation(o) })
}

// planEdit parses the file, applies edit to its properties, and places
// the result: the new meta box, the item offsets it moves and the Exif
// Orientation patches.
func planEdit(src io.ReaderAt, size int64, edit func(*heifFile) error) (rotation, error) {
	f, err := parseHEIF(src, size)
	if err != nil {
		return rotation{}, err
	}
	// The meta box changes; no image's data may lie in it.
	if err := f.noDataInMeta(); err != nil {
		return rotation{}, err
	}
	if err := edit(f); err != nil {
		return rotation{}, err
	}
	newMeta, err := f.m.Encode()
	if err != nil {
		return rotation{}, err
	}
	delta := int64(len(newMeta)) - f.meta.Size
	shown, err := f.orientation()
	if err != nil {
		return rotation{}, err
	}
	// Before the offsets move: the patches are placed from the old ones.
	// idat's place in the new meta box doesn't depend on offset values.
	patches, err := f.exifOrientationPatches(src, size, newMeta, delta, shown)
	if err != nil {
		return rotation{}, err
	}
	if delta != 0 {
		if err := f.shiftOffsets(delta); err != nil {
			return rotation{}, err
		}
		if newMeta, err = f.m.Encode(); err != nil {
			if bmff.ErrFieldTooSmall(err) {
				return rotation{}, fmt.Errorf("%w: an item offset outgrows its field", ErrUnsupported)
			}
			return rotation{}, err
		}
		if int64(len(newMeta))-f.meta.Size != delta {
			return rotation{}, fmt.Errorf("%w: the meta box changed size twice", ErrUnsupported)
		}
	}
	return rotation{f: f, meta: newMeta, delta: delta, patches: patches}, nil
}

// inPlacePatches are the changes of a same-size edit as source patches:
// the runs of bytes that differ between the old and the new meta box
// (at the meta box's offset), with the Exif patches that land inside it
// applied first, plus the Exif patches outside it. Diffing the encoded box
// rather than tracking each change makes every changed byte a patch's,
// whatever the edit.
func inPlacePatches(metaAt int64, oldMeta, newMeta []byte, exif []patch) []Patch {
	meta := bytes.Clone(newMeta)
	metaEnd := metaAt + int64(len(meta))
	var out []Patch
	for _, p := range exif {
		for i, c := range p.data {
			at := p.at + int64(i)
			if at >= metaAt && at < metaEnd {
				meta[at-metaAt] = c
			} else {
				out = append(out, Patch{Offset: at, Data: []byte{c}})
			}
		}
	}
	for i := 0; i < len(meta); {
		if meta[i] == oldMeta[i] {
			i++
			continue
		}
		start, end := i, i+1
		for j := end; j < len(meta) && j-end <= patchMergeGap; j++ {
			if meta[j] != oldMeta[j] {
				end = j + 1
			}
		}
		out = append(out, Patch{Offset: metaAt + int64(start), Data: bytes.Clone(meta[start:end])})
		i = end
	}
	slices.SortFunc(out, func(a, b Patch) int { return cmp.Compare(a.Offset, b.Offset) })
	// Join patches that touch; a later one wins where two overlap (two
	// Exif items can't share bytes, so they don't).
	var joined []Patch
	for _, p := range out {
		if n := len(joined); n > 0 {
			last := &joined[n-1]
			lastEnd := last.Offset + int64(len(last.Data))
			if p.Offset <= lastEnd {
				over := lastEnd - p.Offset
				if over > int64(len(p.Data)) {
					copy(last.Data[p.Offset-last.Offset:], p.Data)
					continue
				}
				copy(last.Data[p.Offset-last.Offset:], p.Data[:over])
				last.Data = append(last.Data, p.Data[over:]...)
				continue
			}
		}
		joined = append(joined, p)
	}
	return joined
}

// Apply writes the patches to w, which must hold the bytes of the source,
// e.g. a clone of it. It returns ErrNotInPlace when !p.InPlace, and
// writes nothing then.
func (p *Plan) Apply(w io.WriterAt) error {
	if !p.InPlace {
		return ErrNotInPlace
	}
	for _, pt := range p.Patches {
		if pt.Offset < 0 || pt.Offset+int64(len(pt.Data)) > p.Size {
			return fmt.Errorf("mediameta: a patch at %d outside the file", pt.Offset)
		}
	}
	for _, pt := range p.Patches {
		if _, err := w.WriteAt(pt.Data, pt.Offset); err != nil {
			return err
		}
	}
	return nil
}

// WriteTo streams the edited file to dst (io.WriterTo): NewSize bytes.
// The source must not have changed since the plan was made. On an error
// nothing useful has been written to dst.
func (p *Plan) WriteTo(dst io.Writer) (int64, error) {
	if p.chain != nil {
		return p.chain.WriteTo(dst)
	}
	if p.splice != nil {
		return p.splice.write(p.src, p.Size, dst)
	}
	if p.jpeg != nil {
		return p.jpeg.write(p.src, p.Size, dst)
	}
	if p.f == nil {
		// A plan of patches alone (a RAW file's): the source, patched.
		w := &patchWriter{w: dst, patches: p.patches}
		n, err := io.Copy(w, io.NewSectionReader(p.src, 0, p.Size))
		if err == nil && n != p.Size {
			err = fmt.Errorf("mediameta: wrote %d bytes, want %d", n, p.Size)
		}
		return n, err
	}
	n, err := p.f.write(p.src, p.Size, dst, p.meta, p.NewSize-p.Size-int64(len(p.tail)), p.patches)
	if err != nil || len(p.tail) == 0 {
		return n, err
	}
	m, err := dst.Write(p.tail)
	return n + int64(m), err
}
