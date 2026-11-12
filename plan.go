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
}

// patchMergeGap is how many unchanged bytes may separate two runs of
// changed ones and still make one patch: fewer, longer writes.
const patchMergeGap = 8

// PlanRotation is SetRotation's edit, computed but not written: the HEIF
// file in src (size bytes) with its primary image rotated degrees
// clockwise. It reads the boxes before the image data and the head of the
// Exif item, never the image data.
//
// The edit is in place when the meta box keeps its size: in the usual
// case (an irot only the rotated images use, as in iPhone and ImageIO
// files) one byte of it and the two bytes of the Exif Orientation tag.
// An appended irot, a widened ipma, or item offsets that move change the
// size.
func PlanRotation(src io.ReaderAt, size int64, degrees int) (*Plan, error) {
	if degrees%90 != 0 {
		return nil, fmt.Errorf("mediameta: rotation of %d°: not a multiple of 90", degrees)
	}
	f, err := parseHEIF(src, size)
	if err != nil {
		return nil, err
	}
	oldMeta := bytes.Clone(f.raw)
	quarters := ((-degrees/90)%4 + 4) % 4 // irot counts anticlockwise
	if err := f.setIrot(quarters); err != nil {
		return nil, err
	}
	newMeta, err := f.m.Encode()
	if err != nil {
		return nil, err
	}
	delta := int64(len(newMeta)) - f.meta.Size
	shown, err := f.orientation()
	if err != nil {
		return nil, err
	}
	// Before the offsets move: the patches are placed from the old ones.
	// idat's place in the new meta box doesn't depend on offset values.
	patches, err := f.exifOrientationPatches(src, size, newMeta, delta, shown)
	if err != nil {
		return nil, err
	}
	if delta != 0 {
		if err := f.shiftOffsets(delta); err != nil {
			return nil, err
		}
		if newMeta, err = f.m.Encode(); err != nil {
			if bmff.ErrFieldTooSmall(err) {
				return nil, fmt.Errorf("%w: an item offset outgrows its field", ErrUnsupported)
			}
			return nil, err
		}
		if int64(len(newMeta))-f.meta.Size != delta {
			return nil, fmt.Errorf("%w: the meta box changed size twice", ErrUnsupported)
		}
	}
	p := &Plan{Size: size, NewSize: size + delta, src: src, f: f, meta: newMeta, patches: patches}
	if delta == 0 {
		p.InPlace = true
		p.Patches = inPlacePatches(f.meta.Offset, oldMeta, newMeta, patches)
	}
	return p, nil
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
	return p.f.write(p.src, p.Size, dst, p.meta, p.NewSize-p.Size, p.patches)
}
