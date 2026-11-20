package mediameta

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"time"
)

// ErrNoDateField is the error of a date edit of a file that has no date
// field the library writes: one isn't added.
var ErrNoDateField = fmt.Errorf("%w: a file without a date field to change", ErrUnsupported)

// ErrNoRoom is the error of an edit whose new value doesn't fit where the
// old one is, in a file where making room would move the media data.
var ErrNoRoom = fmt.Errorf("%w: no room for the new value without moving the media data", ErrUnsupported)

// errAVIEditField is the error of an AVI edit of a field other than the
// date.
var errAVIEditField = fmt.Errorf("%w: an AVI file's date is the only field written", ErrUnsupported)

// aviDateLayouts are the date forms an IDIT or ICRD is rewritten in: the
// one its old value reads as, written back the same way.
var aviDateLayouts = [...]string{
	"Mon Jan 02 15:04:05 2006", "Mon Jan _2 15:04:05 2006", "Mon Jan 2 15:04:05 2006",
	"2006:01:02 15:04:05", "2006-01-02 15:04:05", "2006-01-02T15:04:05", "2006/01/02 15:04:05",
	"2006-01-02", "2006:01:02", "2006/01/02", "2006",
}

// nikonAVIDateLen is the length of a Nikon nctg date: DateTimeOriginal
// (0x13) and CreateDate (0x14), in the Exif form.
const nikonAVIDateLen = len("2006:01:02 15:04:05")

// planAVIVideo plans an AVI edit: only the date, rewritten where the file
// has it (IDIT, INFO's ICRD, Nikon's nctg DateTimeOriginal and
// CreateDate), in place. A value that grows takes the bytes from a JUNK
// chunk right after it; nothing else moves, so the indexes' offsets stay
// right.
func planAVIVideo(src io.ReaderAt, size int64, edit VideoEdit) (*Plan, error) {
	if edit.GPS != nil || edit.RemoveGPS || edit.textChange() || edit.XMP != nil {
		return nil, errAVIEditField
	}
	if edit.Created == nil {
		return &Plan{InPlace: true, Size: size, NewSize: size, src: src}, nil
	}
	t := *edit.Created
	if t.Year() < 1 || t.Year() > 9999 {
		return nil, fmt.Errorf("mediameta: date %v: not in the years 1 to 9999", t)
	}
	riffEnd := min(size, 8+int64(binary.LittleEndian.Uint32(mustRead(src, 4, 4))))
	top, err := aviChunks(src, 12, riffEnd)
	if err != nil {
		return nil, err
	}
	var patches []Patch
	found := false
	date := func(sibs []aviChunk, i int) error {
		found = true
		pt, err := aviDatePatch(src, sibs, i, t)
		if err == nil {
			patches = append(patches, pt...)
		}
		return err
	}
	for i, c := range top {
		switch {
		case c.id == "IDIT" && !c.list:
			err = date(top, i)
		case c.list && (c.id == "hdrl" || c.id == "INFO"):
			kids, kerr := aviChunks(src, c.at, c.at+c.n)
			if kerr != nil {
				return nil, kerr
			}
			for j, k := range kids {
				if !k.list && (c.id == "hdrl" && k.id == "IDIT" || c.id == "INFO" && k.id == "ICRD") {
					if err = date(kids, j); err != nil {
						break
					}
				}
			}
		case c.list && c.id == "ncdt":
			kids, kerr := aviChunks(src, c.at, c.at+c.n)
			if kerr != nil {
				return nil, kerr
			}
			for _, k := range kids {
				if k.id != "nctg" || k.list {
					continue
				}
				pt, ok, nerr := nikonAVIDatePatches(src, k, t)
				if nerr != nil {
					return nil, nerr
				}
				found = found || ok
				patches = append(patches, pt...)
			}
		}
		if err != nil {
			return nil, err
		}
	}
	if !found {
		return nil, ErrNoDateField
	}
	sortPatches(patches)
	for i := 1; i < len(patches); i++ {
		if patches[i].Offset < patches[i-1].Offset+int64(len(patches[i-1].Data)) {
			return nil, fmt.Errorf("%w: AVI chunks that overlap", ErrMalformed)
		}
	}
	internal := make([]patch, len(patches))
	for i, pt := range patches {
		internal[i] = patch{at: pt.Offset, data: pt.Data}
	}
	p := &Plan{InPlace: true, Patches: patches, Size: size, NewSize: size, src: src, patches: internal}
	// Read back: nothing but the date may read otherwise, and the date
	// must read as set.
	want := time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), 0, time.UTC)
	check := func(before, after Metadata) error {
		if before.Video != nil && before.Video.Created.IsZero() && after.Video != nil && after.Video.Created.IsZero() {
			return nil // a date the library doesn't read (an ICRD of a year alone)
		}
		if after.Video == nil || !after.Video.Created.Equal(want) && !dateOnlyMatch(after.Video.Created, want) {
			return fmt.Errorf("%w: an AVI date that wouldn't read as set", ErrUnsupported)
		}
		return nil
	}
	if err := readsAsBeforeExcept(src, size, &patchedReader{r: src, size: size, patches: patches}, size, videoEditChanges(edit), check); err != nil {
		return nil, err
	}
	return p, nil
}

// dateOnlyMatch is whether got is want's day: a file whose only date is
// an ICRD of a whole day.
func dateOnlyMatch(got, want time.Time) bool {
	return got.Hour() == 0 && got.Minute() == 0 && got.Second() == 0 &&
		got.Year() == want.Year() && got.Month() == want.Month() && got.Day() == want.Day()
}

// aviDatePatch rewrites the date chunk sibs[i] with t, in the form of its
// old value. A value that doesn't fit the chunk grows it into a JUNK
// chunk right after it.
func aviDatePatch(src io.ReaderAt, sibs []aviChunk, i int, t time.Time) ([]Patch, error) {
	c := sibs[i]
	b, err := readAVIChunk(src, c)
	if err != nil {
		return nil, err
	}
	text := b
	nul := bytes.IndexByte(b, 0)
	if nul >= 0 {
		text = b[:nul]
	}
	core := bytes.TrimRight(text, " \t\r\n")
	suffix := text[len(core):]
	lead := len(core) - len(bytes.TrimLeft(core, " \t"))
	layout := ""
	for _, l := range aviDateLayouts {
		if d, err := time.Parse(l, string(core[lead:])); err == nil && d.Format(l) == string(core[lead:]) {
			layout = l
			break
		}
	}
	if layout == "" {
		return nil, fmt.Errorf("%w: an AVI %s date %q in a form not written", ErrUnsupported, c.id, core)
	}
	val := make([]byte, 0, len(b)+8)
	val = append(val, core[:lead]...)
	val = t.AppendFormat(val, layout)
	val = append(val, suffix...)
	need := len(val)
	if nul >= 0 {
		need++ // keep a NUL ending the text
	}
	if int64(need) <= c.n {
		// In the chunk: the new text, NULs after it up to the old text's end.
		out := make([]byte, max(len(val), len(text)))
		copy(out, val)
		if bytes.Equal(out, b[:len(out)]) {
			return nil, nil
		}
		return []Patch{{Offset: c.at, Data: out}}, nil
	}
	// Grow into the JUNK chunk after it: the two chunks keep their bytes
	// between them, so the list around them keeps its size.
	end := c.at + c.n + c.n&1
	if i+1 >= len(sibs) || sibs[i+1].id != "JUNK" || sibs[i+1].list || sibs[i+1].at != end+8 {
		return nil, fmt.Errorf("%w (an AVI %s date)", ErrNoRoom, c.id)
	}
	j := sibs[i+1]
	region := 8 + c.n + c.n&1 + 8 + j.n + j.n&1
	n := int64(need + need&1)
	junk := region - 8 - n - 8
	if junk < 0 {
		return nil, fmt.Errorf("%w (an AVI %s date)", ErrNoRoom, c.id)
	}
	out := make([]byte, 8+n+8)
	copy(out, c.id)
	binary.LittleEndian.PutUint32(out[4:], uint32(n))
	copy(out[8:], val)
	copy(out[8+n:], "JUNK")
	binary.LittleEndian.PutUint32(out[8+n+4:], uint32(junk))
	return []Patch{{Offset: c.at - 8, Data: out}}, nil
}

// nikonAVIDatePatches rewrites the date records of a Nikon nctg chunk:
// DateTimeOriginal (0x13) and CreateDate (0x14), each in its own value's
// bytes. ok is whether the chunk has one.
func nikonAVIDatePatches(src io.ReaderAt, c aviChunk, t time.Time) (out []Patch, ok bool, err error) {
	b, err := readAVIChunk(src, c)
	if err != nil {
		return nil, false, err
	}
	for at := 0; at+4 <= len(b); {
		tag := binary.LittleEndian.Uint16(b[at:])
		n := int(binary.LittleEndian.Uint16(b[at+2:]))
		at += 4
		if at+n > len(b) {
			break
		}
		if tag == 0x13 || tag == 0x14 {
			ok = true
			if n < nikonAVIDateLen {
				return nil, true, fmt.Errorf("%w (a Nikon AVI date of %d bytes)", ErrNoRoom, n)
			}
			v := make([]byte, n)
			t.AppendFormat(v[:0], "2006:01:02 15:04:05")
			if !bytes.Equal(v, b[at:at+n]) {
				out = append(out, Patch{Offset: c.at + int64(at), Data: v})
			}
		}
		at += n
	}
	return out, ok, nil
}

// sortPatches sorts patches by offset: few, so an insertion sort.
func sortPatches(p []Patch) {
	for i := 1; i < len(p); i++ {
		for j := i; j > 0 && p[j].Offset < p[j-1].Offset; j-- {
			p[j], p[j-1] = p[j-1], p[j]
		}
	}
}
