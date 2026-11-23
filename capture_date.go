package mediameta

import (
	"fmt"
	"io"
	"time"
)

// A file's capture date can be in several fields at once: Exif's
// DateTimeOriginal, XMP's photoshop:DateCreated, xmp:CreateDate and
// exif:DateTimeOriginal, IPTC-IIM's Date and Time Created, a movie's
// creation dates. SetCaptureDate writes one date into all of those the
// file has, so none disagrees afterwards, and adds a field only where the
// file has none: the format's standard one.

// Capture date fields, as CaptureDates and CaptureDateChange name them.
const (
	FieldExifDateTimeOriginal  = "Exif DateTimeOriginal"
	FieldExifDateTimeDigitized = "Exif DateTimeDigitized"
	FieldXMPDateCreated        = "XMP photoshop:DateCreated"
	FieldXMPCreateDate         = "XMP xmp:CreateDate"
	FieldXMPDateTimeOriginal   = "XMP exif:DateTimeOriginal"
	FieldIPTCDateCreated       = "IPTC Date Created"
)

// CaptureDate is a capture date field a file has, and its value.
type CaptureDate struct {
	Field string
	Date  Date
}

// CaptureDateChange is a field SetCaptureDate changes: Before is the zero
// Date where it adds the field.
type CaptureDateChange struct {
	Field         string
	Before, After Date
}

// CaptureDates lists every capture date field the file in r (size bytes)
// has, with its value, in the order above: a caller sees where they
// disagree before writing. Exif's DateTimeDigitized (ExifTool's
// CreateDate) is listed too, though it isn't a capture date: when a scan
// was made, say.
func CaptureDates(r io.ReaderAt, size int64) ([]CaptureDate, error) {
	m, err := ReadAll(r, size)
	if err != nil {
		return nil, err
	}
	return imageCaptureDates(m), nil
}

func imageCaptureDates(m Metadata) []CaptureDate {
	var out []CaptureDate
	add := func(field string, d *Date) {
		if d != nil && !d.Time.IsZero() {
			out = append(out, CaptureDate{Field: field, Date: *d})
		}
	}
	exifDate := func(t time.Time, known bool) *Date {
		if t.IsZero() {
			return nil
		}
		return &Date{Time: t, OffsetKnown: known, Precision: PrecisionSecond}
	}
	add(FieldExifDateTimeOriginal, exifDate(m.Exif.DateTimeOriginal, m.Exif.OffsetKnown))
	add(FieldExifDateTimeDigitized, exifDate(m.Exif.DateTimeDigitized, m.Exif.DigitizedOffsetKnown))
	add(FieldXMPDateCreated, m.XMP.DateCreated)
	add(FieldXMPCreateDate, m.XMP.CreateDate)
	add(FieldXMPDateTimeOriginal, m.XMP.DateTimeOriginal)
	add(FieldIPTCDateCreated, m.IPTC.DateCreated)
	return out
}

// PlanCaptureDate plans setting the file's capture date to d:
//   - every capture date field the file has gets d, so none disagrees
//     afterwards; one it doesn't have isn't added;
//   - a file with none gets its format's standard field: Exif
//     DateTimeOriginal (with OffsetTimeOriginal where d's offset is known)
//     for JPEG, HEIF and TIFF-based files, XMP photoshop:DateCreated for
//     PNG and WebP;
//   - Exif DateTimeDigitized is never added, and is changed only where it
//     equals the old capture date (it means when the image was digitized,
//     a scan's date, say).
//
// It returns the plan and every field it changes, before and after.
func PlanCaptureDate(src io.ReaderAt, size int64, d Date) (*Plan, []CaptureDateChange, error) {
	if d.Time.IsZero() {
		return nil, nil, fmt.Errorf("mediameta: a capture date without a time")
	}
	if d.Precision == 0 {
		d.Precision = PrecisionSecond
	}
	m, err := ReadAll(src, size)
	if err != nil {
		return nil, nil, err
	}
	have := imageCaptureDates(m)
	var edit MetadataEdit
	var changes []CaptureDateChange
	exifTime := d.Time
	setExif := func() *ExifEdit {
		if edit.Exif == nil {
			edit.Exif = &ExifEdit{}
		}
		return edit.Exif
	}
	setXMP := func() *XMPEdit {
		if edit.XMP == nil {
			edit.XMP = &XMPEdit{}
		}
		return edit.XMP
	}
	// The capture date before: the first capture field (not Digitized).
	var old *Date
	for _, h := range have {
		if h.Field != FieldExifDateTimeDigitized {
			o := h.Date
			old = &o
			break
		}
	}
	nd := d
	for _, h := range have {
		switch h.Field {
		case FieldExifDateTimeOriginal:
			e := setExif()
			e.DateTimeOriginal, e.OffsetKnown = &exifTime, d.OffsetKnown
		case FieldExifDateTimeDigitized:
			if old == nil || !sameCaptureDate(h.Date, *old) {
				continue
			}
			e := setExif()
			e.DateTimeDigitized, e.OffsetKnown = &exifTime, d.OffsetKnown
		case FieldXMPDateCreated:
			setXMP().DateCreated = &nd
		case FieldXMPCreateDate:
			setXMP().CreateDate = &nd
		case FieldXMPDateTimeOriginal:
			setXMP().DateTimeOriginal = &nd
		case FieldIPTCDateCreated:
			edit.IPTC = &IPTCEdit{DateCreated: &nd}
		}
		if !sameCaptureDate(h.Date, d) {
			changes = append(changes, CaptureDateChange{Field: h.Field, Before: h.Date, After: d})
		}
	}
	if old == nil {
		// No capture date: the format's standard field.
		switch detectFormat(src, size) {
		case formatPNG, formatWebP:
			setXMP().DateCreated = &nd
			changes = append(changes, CaptureDateChange{Field: FieldXMPDateCreated, After: d})
		default:
			e := setExif()
			e.DateTimeOriginal, e.OffsetKnown = &exifTime, d.OffsetKnown
			changes = append(changes, CaptureDateChange{Field: FieldExifDateTimeOriginal, After: d})
		}
	}
	p, err := PlanMetadata(src, size, edit)
	if err != nil {
		return nil, nil, err
	}
	return p, changes, nil
}

// SetCaptureDate writes to dst the file in src with its capture date set
// to d, as PlanCaptureDate plans it, and returns the fields it changed.
func SetCaptureDate(src io.ReaderAt, size int64, dst io.Writer, d Date) ([]CaptureDateChange, error) {
	p, changes, err := PlanCaptureDate(src, size, d)
	if err != nil {
		return nil, err
	}
	if _, err := p.WriteTo(dst); err != nil {
		return nil, err
	}
	return changes, nil
}

// sameCaptureDate is whether two dates are the same instant (or clock time,
// without an offset) and both know their offset or neither does.
func sameCaptureDate(a, b Date) bool {
	return a.OffsetKnown == b.OffsetKnown && a.Time.Equal(b.Time)
}
