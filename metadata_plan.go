package mediameta

import (
	"bytes"
	"fmt"
	"io"
	"slices"
)

// MetadataEdit is a change to a file's metadata in each standard that
// holds it: its Exif, its XMP packet and its IPTC-IIM data. A nil part
// leaves that standard as it is, as does a part with every field left at
// its zero value.
type MetadataEdit struct {
	Exif *ExifEdit
	XMP  *XMPEdit
	// IPTC is written in JPEG only; another format refuses it with
	// ErrUnsupported.
	IPTC *IPTCEdit
}

// SetMetadata writes to dst the file in src (size bytes) with edit
// applied, as PlanMetadata plans it.
func SetMetadata(src io.ReaderAt, size int64, dst io.Writer, edit MetadataEdit) error {
	p, err := PlanMetadata(src, size, edit)
	if err != nil {
		return err
	}
	_, err = p.WriteTo(dst)
	return err
}

// PlanMetadata plans every part of edit as one edit of the JPEG, HEIF,
// PNG or WebP file in src (size bytes): the Exif part as PlanExifEdit,
// the IPTC part as EditIPTC applied to the file's IPTC data, the XMP part
// as PlanXMPEdit, in that order, each on the file the parts before it
// make. Nothing is written until the plan is: either every part is
// written or, on an error, none.
//
// The plan is in place when every part is. Its patches are then every
// byte any part changes, as the last part leaves it, so Apply on a copy
// of the source gives what WriteTo gives. Otherwise WriteTo writes the
// whole file. A part that changes the file's size is held in memory (the
// file as it makes it) for the parts after it and for WriteTo.
func PlanMetadata(src io.ReaderAt, size int64, edit MetadataEdit) (*Plan, error) {
	type part func(io.ReaderAt, int64) (*Plan, error)
	var parts []part
	if e := edit.Exif; e != nil && *e != (ExifEdit{}) {
		parts = append(parts, func(r io.ReaderAt, n int64) (*Plan, error) { return PlanExifEdit(r, n, *e) })
	}
	if e := edit.IPTC; e != nil && *e != (IPTCEdit{}) {
		parts = append(parts, func(r io.ReaderAt, n int64) (*Plan, error) { return planIPTCEdit(r, n, *e) })
	}
	if e := edit.XMP; e != nil && *e != (XMPEdit{}) {
		parts = append(parts, func(r io.ReaderAt, n int64) (*Plan, error) { return PlanXMPEdit(r, n, *e) })
	}
	if len(parts) == 0 {
		return &Plan{InPlace: true, Size: size, NewSize: size, src: src}, nil
	}

	view, n := src, size // the file as the parts so far make it
	inPlace := true
	var changed [][2]int64 // the ranges in-place parts patched
	var last *Plan
	for _, plan := range parts {
		p, err := plan(view, n)
		if err != nil {
			return nil, err
		}
		last = p
		if p.InPlace {
			for _, pt := range p.Patches {
				changed = append(changed, [2]int64{pt.Offset, pt.Offset + int64(len(pt.Data))})
			}
			view = &patchedReader{r: view, size: n, patches: p.Patches}
			continue
		}
		inPlace = false
		var b bytes.Buffer
		b.Grow(int(p.NewSize))
		if _, err := p.WriteTo(&b); err != nil {
			return nil, err
		}
		view, n = bytes.NewReader(b.Bytes()), int64(b.Len())
	}
	if len(parts) == 1 {
		return last, nil
	}
	if !inPlace {
		// The last part's plan writes the whole file, from the view the
		// parts before it make.
		return &Plan{Size: size, NewSize: n, src: src, chain: last}, nil
	}
	patches, err := readRanges(view, mergeRanges(changed))
	if err != nil {
		return nil, err
	}
	// WriteTo writes the source with the same patches.
	internal := make([]patch, len(patches))
	for i, pt := range patches {
		internal[i] = patch{at: pt.Offset, data: pt.Data}
	}
	return &Plan{InPlace: true, Patches: patches, Size: size, NewSize: size, src: src, patches: internal}, nil
}

// planIPTCEdit plans edit applied to a JPEG's IPTC-IIM data (new data
// where it has none).
func planIPTCEdit(src io.ReaderAt, size int64, edit IPTCEdit) (*Plan, error) {
	if !isJPEG(src, size) {
		return nil, fmt.Errorf("%w: IPTC-IIM in a file that isn't a JPEG", ErrUnsupported)
	}
	old, err := ReadIPTC(src, size)
	if err != nil {
		return nil, err
	}
	iim, err := EditIPTC(old, edit)
	if err != nil {
		return nil, err
	}
	return PlanIPTC(src, size, iim)
}

// patchedReader reads r (size bytes) with patches written over it.
type patchedReader struct {
	r       io.ReaderAt
	size    int64
	patches []Patch
}

func (p *patchedReader) ReadAt(b []byte, off int64) (int, error) {
	n, err := p.r.ReadAt(b, off)
	end := off + int64(n)
	for _, pt := range p.patches {
		lo, hi := max(pt.Offset, off), min(pt.Offset+int64(len(pt.Data)), end)
		if lo < hi {
			copy(b[lo-off:hi-off], pt.Data[lo-pt.Offset:hi-pt.Offset])
		}
	}
	return n, err
}

// mergeRanges sorts ranges and joins those that overlap or touch.
func mergeRanges(rs [][2]int64) [][2]int64 {
	slices.SortFunc(rs, func(a, b [2]int64) int {
		switch {
		case a[0] < b[0]:
			return -1
		case a[0] > b[0]:
			return 1
		}
		return 0
	})
	var out [][2]int64
	for _, r := range rs {
		if r[0] >= r[1] {
			continue
		}
		if k := len(out) - 1; k >= 0 && r[0] <= out[k][1] {
			out[k][1] = max(out[k][1], r[1])
			continue
		}
		out = append(out, r)
	}
	return out
}

// readRanges reads each range of r as a patch.
func readRanges(r io.ReaderAt, rs [][2]int64) ([]Patch, error) {
	out := make([]Patch, 0, len(rs))
	for _, rg := range rs {
		b := make([]byte, rg[1]-rg[0])
		if _, err := r.ReadAt(b, rg[0]); err != nil && err != io.EOF {
			return nil, err
		}
		out = append(out, Patch{Offset: rg[0], Data: b})
	}
	return out, nil
}
