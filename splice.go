package mediameta

import (
	"fmt"
	"io"
	"sort"
)

// splice replaces del bytes of the source at at with data: an insert
// (del 0), a removal (no data), or a replacement.
type splice struct {
	at, del int64
	data    []byte
}

// spliceEdit is an edit of a chunked file (PNG, WebP) as splices of its
// source, sorted by offset and not overlapping. Bytes a splice removes
// aren't copied, so an old value that's replaced is gone from the result.
type spliceEdit struct {
	splices []splice
}

func (e *spliceEdit) add(s splice) { e.splices = append(e.splices, s) }

func (e *spliceEdit) sorted() error {
	sort.SliceStable(e.splices, func(i, j int) bool { return e.splices[i].at < e.splices[j].at })
	for i := 1; i < len(e.splices); i++ {
		if prev := e.splices[i-1]; prev.at+prev.del > e.splices[i].at {
			return fmt.Errorf("mediameta: overlapping edits at %d", e.splices[i].at)
		}
	}
	return nil
}

func (e *spliceEdit) delta() int64 {
	var d int64
	for _, s := range e.splices {
		d += int64(len(s.data)) - s.del
	}
	return d
}

// write streams the source with the splices applied.
func (e *spliceEdit) write(src io.ReaderAt, size int64, dst io.Writer) (int64, error) {
	var n, pos int64
	copyTo := func(end int64) error {
		m, err := io.Copy(dst, io.NewSectionReader(src, pos, end-pos))
		n += m
		return err
	}
	for _, s := range e.splices {
		if err := copyTo(s.at); err != nil {
			return n, err
		}
		m, err := dst.Write(s.data)
		n += int64(m)
		if err != nil {
			return n, err
		}
		pos = s.at + s.del
	}
	if err := copyTo(size); err != nil {
		return n, err
	}
	if want := size + e.delta(); n != want {
		return n, fmt.Errorf("mediameta: wrote %d bytes, want %d", n, want)
	}
	return n, nil
}

// plan is the Plan of the edit: in place when every splice keeps its
// length, its patches the splices.
func (e *spliceEdit) plan(src io.ReaderAt, size int64) *Plan {
	p := &Plan{Size: size, NewSize: size + e.delta(), src: src, splice: e}
	p.InPlace = true
	for _, s := range e.splices {
		if int64(len(s.data)) != s.del {
			p.InPlace = false
		}
	}
	if p.InPlace {
		for _, s := range e.splices {
			if len(s.data) > 0 {
				p.Patches = append(p.Patches, Patch{Offset: s.at, Data: s.data})
			}
		}
		p.Patches = joinPatches(p.Patches)
	}
	return p
}

// joinPatches joins sorted patches that touch.
func joinPatches(ps []Patch) []Patch {
	var out []Patch
	for _, p := range ps {
		if n := len(out); n > 0 && out[n-1].Offset+int64(len(out[n-1].Data)) == p.Offset {
			out[n-1].Data = append(append([]byte(nil), out[n-1].Data...), p.Data...)
			continue
		}
		out = append(out, p)
	}
	return out
}

// padTo is b padded to n bytes with pad; ok is false when b is longer.
func padTo(b []byte, n int, pad byte) ([]byte, bool) {
	if len(b) > n {
		return nil, false
	}
	out := make([]byte, n)
	copy(out, b)
	for i := len(b); i < n; i++ {
		out[i] = pad
	}
	return out, true
}

// planChunked plans a PNG's or WebP's Exif or XMP edit; ok is false for a
// file that is neither.
func planChunked(src io.ReaderAt, size int64, kind metadataKind, edit func(old []byte) ([]byte, error)) (*Plan, bool, error) {
	switch {
	case isPNG(src, size):
		p, err := planPNGChunk(src, size, kind, edit)
		return p, true, err
	case isWebP(src, size):
		p, err := planWebPChunk(src, size, kind, edit)
		return p, true, err
	}
	return nil, false, nil
}
