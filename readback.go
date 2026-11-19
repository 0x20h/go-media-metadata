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
