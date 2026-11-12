package mediameta

import (
	"errors"
	"fmt"
	"io"
	"sort"
)

// ErrNotInPlace is returned by Plan.Apply for an edit that changes the
// file's size: only Plan.WriteTo can make it.
var ErrNotInPlace = errors.New("mediameta: the edit changes the file's size; it can't be applied in place")

// Patch replaces len(Data) bytes of a file at Offset.
type Patch struct {
	Offset int64
	Data   []byte
}

// Plan is an edit of one file, computed against its source and not yet
// written. It is either applied in place to a copy of the source (Apply),
// or streamed as a new file (WriteTo).
type Plan struct {
	// InPlace is true when the edit keeps the file's size and every byte
	// it changes is in Patches.
	InPlace bool
	// Patches, when InPlace, are the edit: sorted by Offset, not
	// overlapping, each inside [0, Size). They are also the only ranges
	// in which the result differs from the source. Nil otherwise.
	Patches []Patch
	// Size is the source's size, NewSize the result's.
	Size, NewSize int64

	src   io.ReaderAt
	f     *heifFile
	meta  []byte // the new meta box
	delta int64
	out   []patch // at output offsets, for WriteTo
}

// mergeGap is how many unchanged bytes between two changed runs are
// rewritten to make one patch of them.
const mergeGap = 8

// plan makes the Plan for writing meta in place of f's meta box with the
// patches (at output offsets) applied over the result.
func (f *heifFile) plan(src io.ReaderAt, size int64, meta []byte, delta int64, out []patch) *Plan {
	p := &Plan{Size: size, NewSize: size + delta, src: src, f: f, meta: meta, delta: delta, out: out}
	if delta != 0 {
		return p
	}
	// Same size, so output offsets are source offsets. The meta box as it
	// will be written, with the patches that fall inside it (in idat),
	// compared with the one read: its changed runs, and the patches
	// outside it, are the edit.
	start, end := f.meta.Offset, f.meta.End()
	final := append([]byte(nil), meta...)
	var ps []Patch
	for _, pt := range out {
		for i, c := range pt.data {
			at := pt.at + int64(i)
			if at >= start && at < end {
				final[at-start] = c
			} else {
				ps = append(ps, Patch{Offset: at, Data: []byte{c}})
			}
		}
	}
	for i := 0; i < len(final); {
		if final[i] == f.raw[i] {
			i++
			continue
		}
		j := i + 1
		for j < len(final) && final[j] != f.raw[j] {
			j++
		}
		ps = append(ps, Patch{Offset: start + int64(i), Data: final[i:j]})
		i = j
	}
	p.Patches = merge(ps, func(off int64, n int) []byte {
		if off >= start && off+int64(n) <= end {
			return final[off-start : off-start+int64(n)]
		}
		return nil
	})
	p.InPlace = true
	return p
}

// merge sorts patches and joins those that touch or overlap, or whose gap
// lies in the meta box and is at most mergeGap bytes (gap gives its bytes
// as they'll be written, nil when they can't be had without a read).
// A later patch wins where two overlap.
func merge(ps []Patch, gap func(off int64, n int) []byte) []Patch {
	sort.SliceStable(ps, func(i, j int) bool { return ps[i].Offset < ps[j].Offset })
	var out []Patch
	for _, p := range ps {
		if len(out) > 0 {
			last := &out[len(out)-1]
			end := last.Offset + int64(len(last.Data))
			switch {
			case p.Offset <= end:
				data := append([]byte(nil), last.Data...)
				if pEnd := p.Offset + int64(len(p.Data)); pEnd > end {
					data = append(data, make([]byte, pEnd-end)...)
				}
				copy(data[p.Offset-last.Offset:], p.Data)
				last.Data = data
				continue
			case p.Offset-end <= mergeGap:
				if g := gap(end, int(p.Offset-end)); g != nil {
					data := append(append(append([]byte(nil), last.Data...), g...), p.Data...)
					last.Data = data
					continue
				}
			}
		}
		out = append(out, Patch{Offset: p.Offset, Data: p.Data})
	}
	return out
}

// Apply writes the plan's patches to w, which must hold the bytes of the
// source the plan was made from, such as a copy or a clone of it. Nothing
// else of w is written. For a plan that isn't InPlace it returns
// ErrNotInPlace and writes nothing.
//
// A failed Apply can leave w partly patched: apply to a copy, and replace
// the original with it only on success.
func (p *Plan) Apply(w io.WriterAt) error {
	if !p.InPlace {
		return ErrNotInPlace
	}
	for _, pt := range p.Patches {
		if pt.Offset < 0 || pt.Offset+int64(len(pt.Data)) > p.Size {
			return fmt.Errorf("mediameta: patch [%d, %d) outside the file of %d bytes", pt.Offset, pt.Offset+int64(len(pt.Data)), p.Size)
		}
	}
	for _, pt := range p.Patches {
		if _, err := w.WriteAt(pt.Data, pt.Offset); err != nil {
			return err
		}
	}
	return nil
}

// WriteTo writes the whole edited file to dst, copying everything the edit
// doesn't change from the source, which must not have changed since the
// plan was made. It works for every plan, in place or not.
func (p *Plan) WriteTo(dst io.Writer) (int64, error) {
	return p.f.write(p.src, p.Size, dst, p.meta, p.delta, p.out)
}
