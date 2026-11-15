package mediameta

import (
	"errors"
	"io"
	"time"
)

// DateSource is the property a Taken date was read from.
type DateSource uint8

const (
	SourceNone                  DateSource = iota
	SourceExifDateTimeOriginal             // Exif DateTimeOriginal
	SourceExifDateTimeDigitized            // Exif DateTimeDigitized (ExifTool's CreateDate)
	SourceXMPDateTimeOriginal              // XMP exif:DateTimeOriginal
	SourceXMPDateCreated                   // XMP photoshop:DateCreated
	SourceXMPCreateDate                    // XMP xmp:CreateDate
	SourceIPTCDateCreated                  // IPTC 2:55 Date Created and 2:60 Time Created
)

func (s DateSource) String() string {
	switch s {
	case SourceExifDateTimeOriginal:
		return "Exif DateTimeOriginal"
	case SourceExifDateTimeDigitized:
		return "Exif DateTimeDigitized"
	case SourceXMPDateTimeOriginal:
		return "XMP exif:DateTimeOriginal"
	case SourceXMPDateCreated:
		return "XMP photoshop:DateCreated"
	case SourceXMPCreateDate:
		return "XMP xmp:CreateDate"
	case SourceIPTCDateCreated:
		return "IPTC Date Created"
	}
	return "none"
}

// Taken is when a photo was taken, and where that was read from.
type Taken struct {
	Date
	Source DateSource // SourceNone without a date
}

// TakenFrom picks the date a photo was taken from its Exif, XMP and IPTC,
// the first of, in order:
//
//  1. Exif DateTimeOriginal,
//  2. Exif DateTimeDigitized,
//  3. XMP exif:DateTimeOriginal,
//  4. XMP photoshop:DateCreated,
//  5. XMP xmp:CreateDate,
//  6. IPTC Date Created with Time Created.
//
// That is the Metadata Working Group's order for the capture date, Exif
// first, with the creation dates after it: a date that says when the file
// was made is a better guess than none. The date is taken as its source
// has it; a later source doesn't fill in what an earlier one lacks (an
// offset, say). Without any, Source is SourceNone.
func TakenFrom(e Exif, x XMP, i IPTC) Taken {
	exifDate := func(t time.Time, offset bool, src DateSource) Taken {
		return Taken{Date{Time: t, OffsetKnown: offset, Precision: PrecisionSecond}, src}
	}
	switch {
	case !e.DateTimeOriginal.IsZero():
		return exifDate(e.DateTimeOriginal, e.OffsetKnown, SourceExifDateTimeOriginal)
	case !e.DateTimeDigitized.IsZero():
		return exifDate(e.DateTimeDigitized, e.DigitizedOffsetKnown, SourceExifDateTimeDigitized)
	}
	for _, c := range []struct {
		d   *Date
		src DateSource
	}{
		{x.DateTimeOriginal, SourceXMPDateTimeOriginal},
		{x.DateCreated, SourceXMPDateCreated},
		{x.CreateDate, SourceXMPCreateDate},
		{i.DateCreated, SourceIPTCDateCreated},
	} {
		if c.d != nil {
			return Taken{*c.d, c.src}
		}
	}
	return Taken{}
}

// ReadTaken reads when the photo in r (size bytes) was taken: TakenFrom
// of its Exif, XMP and IPTC, of the formats ReadExif takes. It reads no
// more than it needs: a file with an Exif date isn't searched for XMP or
// IPTC.
//
// A part that can't be read (malformed XMP, say) is skipped if another
// gives a date; without a date, the first such error is returned. A file
// with no date at all gives SourceNone and no error.
func ReadTaken(r io.ReaderAt, size int64) (Taken, error) {
	e, err := ReadExif(r, size)
	if errors.Is(err, ErrUnknownFormat) {
		return Taken{}, err
	}
	if t := TakenFrom(e, XMP{}, IPTC{}); t.Source != SourceNone {
		return t, nil
	}
	first := err
	keep := func(err error) {
		if first == nil {
			first = err
		}
	}
	var x XMP
	if packet, err := ReadXMP(r, size); err != nil {
		keep(err)
	} else if packet != nil {
		if x, err = ParseXMP(packet); err != nil {
			keep(err)
		}
	}
	var i IPTC
	if isJPEG(r, size) {
		if iim, err := ReadIPTC(r, size); err != nil {
			keep(err)
		} else if iim != nil {
			if i, err = ParseIPTC(iim); err != nil {
				keep(err)
			}
		}
	}
	if t := TakenFrom(e, x, i); t.Source != SourceNone {
		return t, nil
	}
	return Taken{}, first
}
