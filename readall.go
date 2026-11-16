package mediameta

import (
	"errors"
	"io"
	"strings"
)

// Metadata is everything ReadAll reads from a file: the fields a media
// library sorts, places, shows and searches files by, each taken from the
// first standard that has it, and the parsed standards themselves for
// what the summary leaves out.
type Metadata struct {
	// Format is the file's format: "JPEG", "HEIF", "PNG", "WebP",
	// "JPEG XL", "RAW", "GIF", "BMP", "QuickTime" (QuickTime and MP4),
	// "MPEG-TS" (TS, MTS, M2TS) or "Matroska" (MKV, WebM).
	Format string

	// Taken is when the photo was taken, and where that was read: as
	// TakenFrom picks it from the Exif, XMP and IPTC. For a movie it is
	// Video.Created, with Source SourceMovie.
	Taken Taken
	// GPS is where: the Exif GPS IFD's position, else the XMP's (for a
	// movie, Video.GPS). Nil without one.
	GPS *GPS
	// Camera is the camera, lens and exposure (Exif); for a movie, its
	// make and model.
	Camera Camera

	// Size is the image's size as stored, and with Size.Shown as shown;
	// Size.Orientation is Orientation.
	Size ImageSize
	// Orientation is how the image is shown, as ReadOrientation reads it.
	Orientation Orientation

	// Title, Description, Keywords and Rating: each from the first of the
	// file's XMP, its IPTC-IIM, its Exif (XPTitle; ImageDescription, else
	// XPComment, else XPSubject; XPKeywords split at ";") and, for a GIF,
	// its comment (the description). For a movie, Video's.
	Title, Description string
	Keywords           []string
	Rating             int
	HasRating          bool

	// Regions and PersonInImage are the XMP's face regions and the people
	// shown.
	Regions       *Regions
	PersonInImage []string

	// ContentIdentifier pairs a Live Photo's still with its movie: the
	// Apple maker note's (Exif), or a movie's Keys entry (Video).
	ContentIdentifier string

	// Video is what ReadVideo reads from a movie, nil for an image.
	Video *Video

	// Exif, XMP and IPTC are the standards as parsed, empty where the file
	// has none or one that couldn't be read (see Errors).
	Exif Exif
	XMP  XMP
	IPTC IPTC

	// Errors are each part's error: one broken part doesn't lose the
	// others.
	Errors ReadErrors
}

// ReadErrors are the errors of the parts of a ReadAll, nil where the part
// was read or the file hasn't it.
type ReadErrors struct {
	Exif, XMP, IPTC, Size, Orientation, Video error
}

// Err is the parts' errors joined (errors.Join), nil when every part was
// read.
func (e ReadErrors) Err() error {
	return errors.Join(e.Exif, e.XMP, e.IPTC, e.Size, e.Orientation, e.Video)
}

// SourceMovie is a movie's recording date: Video.Created.
const SourceMovie DateSource = 100

// ReadAll reads everything this package reads from the file in r (size
// bytes) in one call. It tells the format once and, for JPEG, HEIF, PNG
// and GIF, parses the file once for all of its parts; for the other
// formats each part is read as its own function reads it. What it gives
// equals what the separate functions give (ReadExif, ReadXMP and
// ParseXMP, ReadIPTC and ParseIPTC, ReadImageSize, ReadOrientation,
// TakenFrom, ReadVideo).
//
// The error is that of a file of none of the formats this package reads
// (ErrUnknownFormat); a part that can't be read has its error in
// Metadata.Errors, and the others are read all the same.
func ReadAll(r io.ReaderAt, size int64) (Metadata, error) {
	var m Metadata
	var exifErr, xmpErr, iptcErr error
	var packet, iim []byte
	if f, herr := parseHEIF(r, size); herr == nil {
		m.Format = "HEIF"
		m.Exif, exifErr = f.exif(r, size)
		packet, xmpErr = f.xmp(r, size)
		m.Size, m.Errors.Size = f.imageSize()
		m.Orientation, m.Errors.Orientation = f.orientation()
	} else {
		switch detectFormat(r, size) {
		case formatJPEG:
			m.Format = "JPEG"
			f, err := parseJPEG(r, size)
			if err != nil {
				m.Errors.Exif, m.Errors.Size = err, err
				return m, nil
			}
			m.Exif, exifErr = f.exif(r)
			packet, xmpErr = f.xmp(r)
			iim, iptcErr = f.iptc(r)
			m.Size, m.Errors.Size = f.imageSize(r)
			m.Orientation = orientationOfExif(m.Exif, exifErr, &m.Errors)
		case formatPNG:
			m.Format = "PNG"
			pm, err := readPNGMetadata(r, size)
			if err != nil {
				exifErr, xmpErr = err, err
			} else {
				m.Exif, exifErr = pm.readExif(r)
				packet, xmpErr = pm.readXMP(r)
			}
			m.Size, m.Errors.Size = pngImageSize(r, size)
			m.Orientation = orientationOfExif(m.Exif, exifErr, &m.Errors)
		case formatGIF:
			m.Format = "GIF"
			gm, err := readGIFMeta(r, size)
			packet, xmpErr = gm.xmp, err
			m.Description = strings.Join(gm.comments, "\n")
			m.Size, m.Errors.Size = gifImageSize(r, size)
			m.Orientation = 1
		case formatBMP:
			m.Format = "BMP"
			m.Size, m.Errors.Size = bmpImageSize(r, size)
			m.Orientation = 1
		case formatWebP, formatJXL, formatRAW:
			m.Format = map[format]string{formatWebP: "WebP", formatJXL: "JPEG XL", formatRAW: "RAW"}[detectFormat(r, size)]
			m.Exif, exifErr = ReadExif(r, size)
			packet, xmpErr = ReadXMP(r, size)
			m.Size, m.Errors.Size = ReadImageSize(r, size)
			m.Orientation, m.Errors.Orientation = ReadOrientation(r, size)
		case formatMovie, formatTS, formatMatroska, formatAVI, formatMPEGPS:
			return readAllVideo(r, size)
		default:
			if startsWithFtyp(r, size) && errors.Is(herr, ErrMalformed) {
				return m, herr
			}
			return m, formatError("JPEG, HEIF, PNG, WebP, JPEG XL, camera RAW, GIF, BMP, QuickTime, MP4, MPEG-TS or Matroska file")
		}
	}
	if m.Size.Width > 0 && m.Errors.Size == nil {
		m.Size.Orientation = m.Orientation
	} else if m.Errors.Size == nil {
		m.Size = ImageSize{}
	}
	m.Errors.Exif = exifErr
	if packet != nil && xmpErr == nil {
		m.XMP, xmpErr = ParseXMP(packet)
	}
	m.Errors.XMP = xmpErr
	if iim != nil && iptcErr == nil {
		m.IPTC, iptcErr = ParseIPTC(iim)
	}
	m.Errors.IPTC = iptcErr
	m.summarize()
	return m, nil
}

// orientationOfExif is the orientation of a format that keeps it in Exif: the
// tag, 1 without one, as ReadOrientation gives it; on an Exif error, that
// error is the orientation's too.
func orientationOfExif(x Exif, exifErr error, errs *ReadErrors) Orientation {
	if exifErr != nil {
		errs.Orientation = exifErr
		return 0
	}
	if x.Orientation == 0 {
		return 1
	}
	return x.Orientation
}

// summarize fills the summary fields from Exif, XMP and IPTC.
func (m *Metadata) summarize() {
	x, i, e := m.XMP, m.IPTC, m.Exif
	m.Taken = TakenFrom(e, x, i)
	m.GPS = e.GPS
	if m.GPS == nil {
		m.GPS = x.GPS
	}
	m.Camera = e.Camera
	m.Title = firstOf(x.Title, i.Title, e.XPTitle)
	m.Description = firstOf(x.Description, i.Description, e.ImageDescription, e.XPComment, e.XPSubject, m.Description)
	switch {
	case x.Keywords != nil:
		m.Keywords = x.Keywords
	case i.Keywords != nil:
		m.Keywords = i.Keywords
	case e.XPKeywords != "":
		for _, k := range strings.Split(e.XPKeywords, ";") {
			if k = strings.TrimSpace(k); k != "" {
				m.Keywords = append(m.Keywords, k)
			}
		}
	}
	m.Rating, m.HasRating = x.Rating, x.HasRating
	m.Regions, m.PersonInImage = x.Regions, x.PersonInImage
	m.ContentIdentifier = e.ContentIdentifier
}

// readAllVideo is ReadAll of a movie or a transport stream: ReadVideo's.
func readAllVideo(r io.ReaderAt, size int64) (Metadata, error) {
	m := Metadata{Format: "QuickTime"}
	switch detectFormat(r, size) {
	case formatTS:
		m.Format = "MPEG-TS"
	case formatMatroska:
		m.Format = "Matroska"
	case formatAVI:
		m.Format = "AVI"
	case formatMPEGPS:
		m.Format = "MPEG-PS"
	}
	v, err := ReadVideo(r, size)
	if err != nil {
		m.Errors.Video = err
		return m, nil
	}
	m.Video = &v
	if !v.Created.IsZero() {
		m.Taken = Taken{Date{Time: v.Created, OffsetKnown: v.OffsetKnown, Precision: PrecisionSecond}, SourceMovie}
	}
	m.GPS = v.GPS
	m.Camera = Camera{Make: v.Make, Model: v.Model}
	m.Orientation = v.Orientation
	if v.Width > 0 && v.Height > 0 {
		m.Size = ImageSize{Width: v.Width, Height: v.Height, Orientation: v.Orientation}
	}
	m.Title, m.Description, m.Keywords = v.Title, v.Description, v.Keywords
	m.Rating, m.HasRating = v.Rating, v.HasRating
	m.ContentIdentifier = v.ContentIdentifier
	return m, nil
}

func firstOf(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}
