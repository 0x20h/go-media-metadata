package mediameta

import (
	"math"
	"strings"
)

// Tags of the camera and exposure fields, read only.
const (
	tagMake                     = 0x010F
	tagModel                    = 0x0110
	tagExposureTime             = 0x829A
	tagFNumber                  = 0x829D
	tagPhotographicSensitivity  = 0x8827 // ISOSpeedRatings before Exif 2.3
	tagSensitivityType          = 0x8830
	tagRecommendedExposureIndex = 0x8832
	tagISOSpeed                 = 0x8833
	tagFocalLength              = 0x920A
	tagFocalLengthIn35mmFilm    = 0xA405
	tagLensMake                 = 0xA433
	tagLensModel                = 0xA434
)

// Camera is the camera, lens and exposure a photo was taken with, as its
// Exif records them. It is read only: this package doesn't write capture
// data. A field the file doesn't have, or has in a form this package
// doesn't accept, is left at its zero value.
type Camera struct {
	// Make and Model are IFD0's Make and Model; LensMake and LensModel the
	// Exif IFD's. Each is the text as written, up to its NUL and trimmed
	// of spaces, placeholders such as "----" included.
	Make, Model         string
	LensMake, LensModel string
	// ISO is PhotographicSensitivity (ISOSpeedRatings), its first value.
	// Where that is 65535, Exif's "65535 or more", and SensitivityType
	// says a RecommendedExposureIndex or ISOSpeed is given, that one is
	// read instead. 0 without a value.
	ISO int
	// FNumber is the f-number (2.8 for f/2.8), ExposureTime the exposure
	// in seconds, FocalLength the lens's focal length in millimetres: each
	// positive, 0 without a valid value. FocalLengthIn35mm is the 35 mm
	// film equivalent, 0 without one.
	FNumber, ExposureTime, FocalLength float64
	FocalLengthIn35mm                  int
}

// camera reads Camera from IFD0 and the Exif IFD (nil without one).
func (t tiffReader) camera(ifd0, exif map[uint16]field) Camera {
	var c Camera
	text := func(m map[uint16]field, tag uint16) string {
		if f, ok := m[tag]; ok && f.typ == typeASCII {
			s, _ := ascii(f)
			return s
		}
		return ""
	}
	c.Make, c.Model = text(ifd0, tagMake), text(ifd0, tagModel)
	c.LensMake, c.LensModel = text(exif, tagLensMake), text(exif, tagLensModel)
	if c.LensModel == "" && strings.HasPrefix(c.Make, "Canon") {
		c.LensModel = t.canonLensModel(exif[tagMakerNote])
	}
	positive := func(tag uint16) float64 {
		v, ok := t.rationals(exposureTag(exif, ifd0, tag), 1)
		if !ok || len(v) == 0 || !(v[0] > 0) || math.IsInf(v[0], 0) {
			return 0
		}
		return v[0]
	}
	c.FNumber, c.ExposureTime, c.FocalLength = positive(tagFNumber), positive(tagExposureTime), positive(tagFocalLength)
	whole := func(tag uint16) int {
		f := exposureTag(exif, ifd0, tag)
		if f.typ != typeShort && f.typ != typeLong {
			return 0
		}
		v, _ := t.uint(f)
		return int(v)
	}
	c.FocalLengthIn35mm = whole(tagFocalLengthIn35mmFilm)
	c.ISO = whole(tagPhotographicSensitivity)
	if c.ISO == 65535 {
		// SensitivityType: 2 and 6 give a RecommendedExposureIndex, 3 and
		// 7 an ISOSpeed (4 and 5 both, the first preferred here).
		var alt uint16
		switch whole(tagSensitivityType) {
		case 2, 4, 5, 6:
			alt = tagRecommendedExposureIndex
		case 3, 7:
			alt = tagISOSpeed
		}
		if v := whole(alt); alt != 0 && v > 0 {
			c.ISO = v
		}
	}
	return c
}

// incomplete tells whether a field of c is unset.
func (c Camera) incomplete() bool {
	return c.Make == "" || c.Model == "" || c.LensMake == "" || c.LensModel == "" || c.ISO == 0 ||
		c.FNumber == 0 || c.ExposureTime == 0 || c.FocalLength == 0 || c.FocalLengthIn35mm == 0
}

// fill sets c's unset fields to from's.
func (c *Camera) fill(from Camera) {
	for _, s := range []struct {
		to *string
		v  string
	}{
		{&c.Make, from.Make}, {&c.Model, from.Model}, {&c.LensMake, from.LensMake}, {&c.LensModel, from.LensModel},
	} {
		if *s.to == "" {
			*s.to = s.v
		}
	}
	for _, f := range []struct {
		to *float64
		v  float64
	}{
		{&c.FNumber, from.FNumber}, {&c.ExposureTime, from.ExposureTime}, {&c.FocalLength, from.FocalLength},
	} {
		if *f.to == 0 {
			*f.to = f.v
		}
	}
	if c.ISO == 0 {
		c.ISO = from.ISO
	}
	if c.FocalLengthIn35mm == 0 {
		c.FocalLengthIn35mm = from.FocalLengthIn35mm
	}
}

// exposureTag is an exposure tag of the Exif IFD, else of IFD0: some
// cameras (Samsung's among them) put them there, where ExifTool reads
// them too.
func exposureTag(exif, ifd0 map[uint16]field, tag uint16) field {
	if f, ok := exif[tag]; ok {
		return f
	}
	return ifd0[tag]
}

// canonLensModel reads a Canon maker note's LensModel (tag 0x0095, ASCII),
// which Canon cameras write where Exif's own LensModel is missing, as
// ExifTool reads it. A Canon maker note is an IFD without a header, its
// values' offsets counted from the TIFF data's start, as Exif's are.
func (t tiffReader) canonLensModel(mn field) string {
	b := mn.value
	if mn.typ != typeUndef || len(b) < 2 {
		return ""
	}
	n := int(t.order.Uint16(b))
	for i := range min(n, (len(b)-2)/12) {
		e := b[2+12*i : 14+12*i]
		if t.order.Uint16(e) != 0x0095 || t.order.Uint16(e[2:]) != typeASCII {
			continue
		}
		count := uint64(t.order.Uint32(e[4:]))
		v := e[8:12]
		if count > 4 {
			at := uint64(t.order.Uint32(e[8:]))
			if count > 256 || at+count > uint64(len(t.data)) {
				return ""
			}
			v = t.data[at : at+count]
		} else {
			v = v[:count]
		}
		s, _ := ascii(field{typ: typeASCII, count: int(count), value: v})
		return s
	}
	return ""
}
