package mediameta

import (
	"errors"
	"fmt"
	"io"
	"reflect"
	"time"
)

// The read-back check: a metadata write changes what it says it changes
// and nothing else a reader sees. The edited file (or the source with the
// edit laid on top) is read as ReadAll reads it and compared with the
// source's read, the fields the edit may change left out. Any other
// difference, which a malformed file whose structures overlap can make,
// refuses the write (ErrUnsupported).

// readsAsBeforeExcept compares ReadAll of src and of edited, each with
// mayChange applied, and refuses the edit where they differ. Which parts
// fail to read must match; their messages, which may name offsets,
// needn't. check, if not nil, sees the two reads first, as read. A source
// that doesn't read at all is left to the edit's own checks.
func readsAsBeforeExcept(src io.ReaderAt, size int64, edited io.ReaderAt, editedSize int64, mayChange func(*Metadata), check func(before, after Metadata) error) error {
	before, err := ReadAll(src, size)
	if err != nil {
		return nil
	}
	return readsAsRead(before, edited, editedSize, mayChange, check)
}

// readsAsRead is readsAsBeforeExcept with the source's read taken
// already (before).
func readsAsRead(before Metadata, edited io.ReaderAt, editedSize int64, mayChange func(*Metadata), check func(before, after Metadata) error) error {
	after, err := ReadAll(edited, editedSize)
	if err != nil {
		return fmt.Errorf("%w: a file the edit would leave unreadable", ErrUnsupported)
	}
	if check != nil {
		if err := check(before, after); err != nil {
			return err
		}
	}
	for _, m := range []*Metadata{&before, &after} {
		mayChange(m)
		e := &m.Errors
		for _, err := range []*error{&e.Exif, &e.XMP, &e.IPTC, &e.Size, &e.Orientation, &e.Video} {
			if *err != nil {
				*err = errReadFailed
			}
		}
	}
	if !reflect.DeepEqual(before, after) {
		return fmt.Errorf("%w: a file the edit would change beyond what it sets", ErrUnsupported)
	}
	return nil
}

// errReadFailed stands for any read error where only whether a part read
// is compared.
var errReadFailed = errors.New("read failed")

// orientationChanges clears what an orientation edit changes: the
// orientation, in each place it's read.
func orientationChanges(m *Metadata) {
	m.Orientation, m.Size.Orientation, m.Exif.Orientation, m.XMP.Orientation = 0, 0, 0, 0
}

// exifEditChanges is what edit may change in a read, cleared: each Exif
// value it sets, and the summary fields that come from it.
func exifEditChanges(edit ExifEdit) func(*Metadata) {
	return func(m *Metadata) {
		e := &m.Exif
		if edit.Orientation != 0 {
			orientationChanges(m)
		}
		if edit.DateTimeOriginal != nil {
			e.DateTimeOriginal, e.OffsetKnown, m.Taken = time.Time{}, false, Taken{}
		}
		if edit.DateTimeDigitized != nil {
			e.DateTimeDigitized, e.DigitizedOffsetKnown, m.Taken = time.Time{}, false, Taken{}
		}
		if edit.GPS != nil || edit.RemoveGPS {
			e.GPS, m.GPS = nil, nil
		}
		if edit.ImageDescription != nil {
			e.ImageDescription, m.Description = "", ""
		}
		if edit.XPTitle != nil {
			e.XPTitle, m.Title = "", ""
		}
		if edit.XPKeywords != nil {
			e.XPKeywords, m.Keywords = "", nil
		}
	}
}

// xmpEditChanges is what edit may change in a read, cleared: each XMP
// property it sets, the summary fields that come from it, and whether the
// XMP read without an error (a packet that read repaired is written
// clean).
func xmpEditChanges(edit XMPEdit) func(*Metadata) {
	return func(m *Metadata) {
		x := &m.XMP
		m.Errors.XMP = nil
		if edit.Title != nil {
			x.Title, m.Title = "", ""
		}
		if edit.Description != nil {
			x.Description, m.Description = "", ""
		}
		if edit.Keywords != nil || edit.HierarchicalKeywords != nil {
			x.Keywords, x.HierarchicalKeywords, m.Keywords = nil, nil, nil
		}
		if edit.Rating != nil || edit.RemoveRating {
			x.Rating, x.HasRating, m.Rating, m.HasRating = 0, false, 0, false
		}
		if edit.PersonInImage != nil {
			x.PersonInImage, m.PersonInImage = nil, nil
		}
		if edit.Regions != nil {
			x.Regions, m.Regions = nil, nil
		}
		for _, d := range []struct {
			set *Date
			to  **Date
		}{{edit.DateCreated, &x.DateCreated}, {edit.CreateDate, &x.CreateDate}, {edit.DateTimeOriginal, &x.DateTimeOriginal}, {edit.ModifyDate, &x.ModifyDate}} {
			if d.set != nil {
				*d.to, m.Taken = nil, Taken{}
			}
		}
		if edit.GPS != nil || edit.RemoveGPS {
			x.GPS, m.GPS = nil, nil
		}
	}
}

// partChanges clears what replacing one metadata part (kind's: the Exif
// or the XMP) may change in a read: that part, whether it read, and the
// summary fields that follow from the parts. The image size, the
// orientation the format keeps, and the other parts must read as before.
func partChanges(kind metadataKind) func(*Metadata) {
	return func(m *Metadata) {
		switch kind {
		case kindExif:
			m.Exif, m.Errors.Exif = Exif{}, nil
		case kindXMP:
			m.XMP, m.Errors.XMP = XMP{}, nil
		}
		m.Taken, m.GPS, m.Camera = Taken{}, nil, Camera{}
		m.Title, m.Description, m.Keywords = "", "", nil
		m.Rating, m.HasRating = 0, false
		m.Regions, m.PersonInImage, m.ContentIdentifier = nil, nil, ""
	}
}

// heifPlanReader reads what a HEIF plan writes, without writing it: the
// source before the meta box, the new meta box, the source after it, the
// data appended, with the patches (at the new file's offsets) over them.
func heifPlanReader(p *Plan) (io.ReaderAt, int64) {
	f := p.f
	metaAt, metaEnd := f.meta.Offset, f.meta.End()
	parts := []planPart{
		{n: metaAt, src: 0},
		{n: int64(len(p.meta)), data: p.meta},
		{n: p.Size - metaEnd, src: metaEnd},
		{n: int64(len(p.tail)), data: p.tail},
	}
	r := &partsReader{src: p.src, parts: parts}
	for _, pt := range parts {
		r.size += pt.n
	}
	patches := make([]Patch, len(p.patches))
	for i, pt := range p.patches {
		patches[i] = Patch{Offset: pt.at, Data: pt.data}
	}
	return &patchedReader{r: r, size: r.size, patches: patches}, r.size
}

// planPart is a run of an edited file: n bytes, from the source at src or
// from data.
type planPart struct {
	n    int64
	src  int64
	data []byte
}

// partsReader reads parts one after another as one file.
type partsReader struct {
	src   io.ReaderAt
	parts []planPart
	size  int64
}

func (r *partsReader) ReadAt(b []byte, off int64) (int, error) {
	if off < 0 {
		return 0, fmt.Errorf("mediameta: read at %d", off)
	}
	n := 0
	at := int64(0)
	for _, pt := range r.parts {
		if n == len(b) {
			break
		}
		if off+int64(n) >= at+pt.n {
			at += pt.n
			continue
		}
		in := off + int64(n) - at // where in this part
		k := min(int64(len(b)-n), pt.n-in)
		if pt.data != nil {
			copy(b[n:n+int(k)], pt.data[in:in+k])
		} else if m, err := r.src.ReadAt(b[n:n+int(k)], pt.src+in); int64(m) < k {
			if err == nil {
				err = io.ErrUnexpectedEOF
			}
			return n + m, err
		}
		n += int(k)
		at += pt.n
	}
	if n < len(b) {
		return n, io.EOF
	}
	return n, nil
}

// videoEditChanges is what edit may change in a movie's read, cleared:
// the values it sets, in Video and in the summary fields that come from
// them. An XMP edit may change any of them, as a movie's values can come
// from its XMP.
func videoEditChanges(edit VideoEdit) func(*Metadata) {
	return func(m *Metadata) {
		v := m.Video
		if v == nil {
			return
		}
		x := edit.XMP != nil
		if edit.Created != nil || x {
			v.Created, v.OffsetKnown, v.MovieCreated, m.Taken = time.Time{}, false, time.Time{}, Taken{}
		}
		if edit.GPS != nil || edit.RemoveGPS || x {
			v.GPS, m.GPS = nil, nil
		}
		if edit.Title != nil || x {
			v.Title, m.Title = "", ""
		}
		if edit.Description != nil || x {
			v.Description, m.Description = "", ""
		}
		if edit.Keywords != nil || x {
			v.Keywords, m.Keywords = nil, nil
		}
		if edit.Rating != nil || edit.RemoveRating || x {
			v.Rating, v.HasRating, m.Rating, m.HasRating = 0, false, 0, false
		}
		if x {
			xmpEditChanges(*edit.XMP)(m)
		}
	}
}

// splicePlanReader reads what a splice plan writes, without writing it:
// the source between the splices, each splice's data in its place.
func splicePlanReader(p *Plan) (io.ReaderAt, int64) {
	var parts []planPart
	pos := int64(0)
	for _, s := range p.splice.splices {
		parts = append(parts, planPart{n: s.at - pos, src: pos}, planPart{n: int64(len(s.data)), data: s.data})
		pos = s.at + s.del
	}
	parts = append(parts, planPart{n: p.Size - pos, src: pos})
	r := &partsReader{src: p.src, parts: parts}
	for _, pt := range parts {
		r.size += pt.n
	}
	return r, r.size
}
