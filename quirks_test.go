package mediameta

import (
	"bytes"
	"encoding/binary"
	"math"
	"testing"

	"github.com/0x20h/go-media-metadata/internal/bmff/testbuild"
)

// quirk is a JPEG whose Exif strays from the standard the way real
// writers do, and what ReadExif reads from it. exiftool marks those
// ExifTool reads the same way (TestExifAgreesWithExifTool checks them);
// the others note how ExifTool differs.
type quirk struct {
	name     string
	file     []byte
	o        Orientation
	taken    string // clock time, "" for none
	offset   string // the zone's name when the offset is known
	gps      *GPS
	exiftool bool
}

func quirks(t testing.TB) []quirk {
	plain := plainJPEG(t)
	le := binary.LittleEndian
	b := &tiffBuilder{order: le}
	jpg := func(ifd0, exif, gp []tiffEntry) []byte {
		tb := &tiffBuilder{order: le, ifd0: ifd0, exif: exif, gp: gp}
		if tb.ifd0 == nil {
			tb.ifd0 = []tiffEntry{}
		}
		return withSegments(plain, exifSegment(tb.build()))
	}
	str := func(tag uint16, s string) tiffEntry { return tiffEntry{tag, typeASCII, uint32(len(s)), []byte(s)} }
	date := func(s string, more ...tiffEntry) []tiffEntry {
		return append([]tiffEntry{str(tagDateTimeOriginal, s)}, more...)
	}
	orient := func(v uint16) []tiffEntry { return []tiffEntry{{tagOrientation, typeShort, 1, b.short(v)}} }
	d3 := func(d, m, s uint32) [][2]uint32 { return [][2]uint32{{d, 1}, {m, 1}, {s, 1}} }
	// A latitude with the given reference ("" for none) and values, and
	// longitude 10° E.
	pos := func(ref string, typ uint16, vals [][2]uint32, more ...tiffEntry) []tiffEntry {
		var es []tiffEntry
		if ref != "" {
			es = append(es, str(tagGPSLatitudeRef, ref))
		}
		es = append(es, tiffEntry{tagGPSLatitude, typ, uint32(len(vals)), b.rationals(vals...)},
			str(tagGPSLongitudeRef, "E\x00"), tiffEntry{tagGPSLongitude, typeRational, 3, b.rationals(d3(10, 0, 0)...)})
		return append(es, more...)
	}
	alt := func(ref int) []tiffEntry {
		es := []tiffEntry{{tagGPSAltitude, typeRational, 1, b.rationals([2]uint32{100, 1})}}
		if ref >= 0 {
			es = append(es, tiffEntry{tagGPSAltitudeRef, typeByte, 1, []byte{byte(ref)}})
		}
		return es
	}
	at := func(lat, alt float64, hasAlt bool) *GPS {
		return &GPS{Latitude: lat, Longitude: 10, Altitude: alt, HasAltitude: hasAlt}
	}
	const when = "2025-05-04 13:14:15"
	// The Exif IFD pointer typed IFD (13) rather than LONG.
	ptr13 := (&tiffBuilder{order: le, ifd0: []tiffEntry{}, exif: date("2025:05:04 13:14:15\x00")}).build()
	for i := range int(le.Uint16(ptr13[8:])) {
		if e := 10 + 12*i; le.Uint16(ptr13[e:]) == tagExifIFD {
			le.PutUint16(ptr13[e+2:], typeIFD)
		}
	}
	tiffWith := func(o uint16) []byte { return (&tiffBuilder{order: le, ifd0: orient(o)}).build() }

	return []quirk{
		// Orientation in another integer type, or with two values.
		{name: "o-long", file: jpg([]tiffEntry{{tagOrientation, typeLong, 1, b.long(6)}}, nil, nil), o: 6, exiftool: true},
		{name: "o-byte", file: jpg([]tiffEntry{{tagOrientation, typeByte, 1, []byte{6}}}, nil, nil), o: 6, exiftool: true},
		// ExifTool shows "6 3"; the first is the orientation.
		{name: "o-count2", file: jpg([]tiffEntry{{tagOrientation, typeShort, 2, append(b.short(6), b.short(3)...)}}, nil, nil), o: 6},
		{name: "ptr-type13", file: withSegments(plain, exifSegment(ptr13)), taken: when, exiftool: true},

		// Segments: a pad byte of 0xFF after "Exif\0"; two Exif segments
		// (the first counts); a first one without TIFF data (passed over).
		{name: "seg-exif-ff", file: withSegments(plain, segment(markerAPP1, append([]byte("Exif\x00\xff"), tiffWith(6)...))), o: 6, exiftool: true},
		{name: "seg-two-exif", file: withSegments(plain, exifSegment(tiffWith(6)), exifSegment(tiffWith(3))), o: 6, exiftool: true},
		{name: "seg-empty-first", file: withSegments(plain, exifSegment(nil), exifSegment(tiffWith(3))), o: 3, exiftool: true},

		// Dates. ExifTool passes DateTimeOriginal on as text; these are
		// how this package reads the layouts writers use.
		{name: "d-nonul", file: jpg(nil, date("2025:05:04 13:14:15"), nil), taken: when, exiftool: true},
		{name: "d-undef-type", file: jpg(nil, []tiffEntry{{tagDateTimeOriginal, typeUndef, 20, []byte("2025:05:04 13:14:15\x00")}}, nil), taken: when, exiftool: true},
		{name: "d-dashes", file: jpg(nil, date("2025-05-04 13:14:15\x00"), nil), taken: when},
		{name: "d-T", file: jpg(nil, date("2025:05:04T13:14:15\x00"), nil), taken: when},
		{name: "d-nosec", file: jpg(nil, date("2025:05:04 13:14\x00"), nil), taken: "2025-05-04 13:14:00"},
		{name: "d-inline-offset", file: jpg(nil, date("2025:05:04 13:14:15+02:00\x00"), nil), taken: when, offset: "+02:00"},
		{name: "d-blank", file: jpg(nil, date("    :  :     :  :  \x00"), nil)},
		{name: "d-24h", file: jpg(nil, date("2025:05:04 24:00:00\x00"), nil)},
		{name: "d-feb30", file: jpg(nil, date("2025:02:30 10:00:00\x00"), nil)},

		// Fractions of a second: blanks around them are trimmed (ExifTool
		// trims trailing ones only); a non-number is left out (ExifTool
		// takes its leading digits).
		{name: "sub-trail", file: jpg(nil, date("2025:05:04 13:14:15\x00", str(tagSubSecTimeOriginal, "25  \x00")), nil), taken: when + ".25", exiftool: true},
		{name: "sub-lead", file: jpg(nil, date("2025:05:04 13:14:15\x00", str(tagSubSecTimeOriginal, "  25\x00")), nil), taken: when + ".25"},
		{name: "sub-junk", file: jpg(nil, date("2025:05:04 13:14:15\x00", str(tagSubSecTimeOriginal, "2a\x00")), nil), taken: when},

		// Offsets: a trailing blank is fine, "Z" is UTC; "+0200" and a
		// blank offset aren't offsets. The old TimeZoneOffset tag (hours)
		// isn't read: ExifTool doesn't combine it with the time either.
		{name: "off-trail", file: jpg(nil, date("2025:05:04 13:14:15\x00", str(tagOffsetTimeOriginal, "+02:00 \x00")), nil), taken: when, offset: "+02:00", exiftool: true},
		{name: "off-Z", file: jpg(nil, date("2025:05:04 13:14:15\x00", str(tagOffsetTimeOriginal, "Z\x00")), nil), taken: when, offset: "Z"},
		{name: "off-nocolon", file: jpg(nil, date("2025:05:04 13:14:15\x00", str(tagOffsetTimeOriginal, "+0200\x00")), nil), taken: when},
		{name: "off-blank", file: jpg(nil, date("2025:05:04 13:14:15\x00", str(tagOffsetTimeOriginal, "   :  \x00")), nil), taken: when},
		{name: "tzoffset-882a", file: jpg(nil, date("2025:05:04 13:14:15\x00", tiffEntry{0x882A, 8, 1, b.short(0xFFFE)}), nil), taken: when},

		// GPS references: S or W in either case, or a word; an empty one
		// is positive; none at all is no position.
		{name: "g-lower", file: jpg(nil, nil, pos("s\x00", typeRational, d3(51, 30, 0))), gps: at(-51.5, 0, false), exiftool: true},
		{name: "g-south-word", file: jpg(nil, nil, pos("South\x00", typeRational, d3(51, 30, 0))), gps: at(-51.5, 0, false), exiftool: true},
		{name: "g-emptyref", file: jpg(nil, nil, pos("\x00\x00", typeRational, d3(51, 30, 0))), gps: at(51.5, 0, false), exiftool: true},
		{name: "g-noref", file: jpg(nil, nil, pos("", typeRational, d3(51, 30, 0))), exiftool: true},
		// Coordinates as degrees alone, or degrees and minutes; signed
		// rationals; minutes past 60; zero.
		{name: "g-1val", file: jpg(nil, nil, pos("N\x00", typeRational, [][2]uint32{{515, 10}})), gps: at(51.5, 0, false), exiftool: true},
		{name: "g-2val", file: jpg(nil, nil, pos("N\x00", typeRational, [][2]uint32{{51, 1}, {30, 1}})), gps: at(51.5, 0, false), exiftool: true},
		{name: "g-srational", file: jpg(nil, nil, pos("N\x00", typeSRational, d3(51, 30, 0))), gps: at(51.5, 0, false), exiftool: true},
		{name: "g-minutes", file: jpg(nil, nil, pos("N\x00", typeRational, d3(0, 3090, 0))), gps: at(51.5, 0, false), exiftool: true},
		{name: "g-zero", file: jpg(nil, nil, pos("N\x00", typeRational, d3(0, 0, 0))), gps: at(0, 0, false), exiftool: true},
		// ExifTool shows an empty latitude for a zero denominator, and 91°
		// as it is; neither is a position here.
		{name: "g-zeroden", file: jpg(nil, nil, pos("N\x00", typeRational, [][2]uint32{{51, 0}, {30, 1}, {0, 1}}))},
		{name: "g-91", file: jpg(nil, nil, pos("N\x00", typeRational, d3(91, 0, 0)))},
		// Altitude: reference 1 or 3 (Exif 3.0) is below; ExifTool also
		// reads 2 (above sea level) as below, and leaves the altitude out
		// without a reference, where the standard's default is above.
		{name: "g-alt-ref3", file: jpg(nil, nil, pos("N\x00", typeRational, d3(51, 30, 0), alt(3)...)), gps: at(51.5, -100, true), exiftool: true},
		{name: "g-alt-ref2", file: jpg(nil, nil, pos("N\x00", typeRational, d3(51, 30, 0), alt(2)...)), gps: at(51.5, 100, true)},
		{name: "g-alt-noref", file: jpg(nil, nil, pos("N\x00", typeRational, d3(51, 30, 0), alt(-1)...)), gps: at(51.5, 100, true)},
	}
}

func TestReadExifQuirks(t *testing.T) {
	for _, q := range quirks(t) {
		x := readExif(t, q.file)
		if x.Orientation != q.o {
			t.Errorf("%s: orientation %d, want %d", q.name, x.Orientation, q.o)
		}
		var taken string
		if !x.DateTimeOriginal.IsZero() {
			taken = x.DateTimeOriginal.Format("2006-01-02 15:04:05.999999999")
		}
		if taken != q.taken {
			t.Errorf("%s: taken %q, want %q", q.name, taken, q.taken)
		}
		var offset string
		if x.OffsetKnown {
			offset = x.DateTimeOriginal.Location().String()
		}
		if offset != q.offset {
			t.Errorf("%s: offset %q, want %q", q.name, offset, q.offset)
		}
		switch {
		case (x.GPS == nil) != (q.gps == nil):
			t.Errorf("%s: GPS %+v, want %+v", q.name, x.GPS, q.gps)
		case x.GPS != nil && (math.Abs(x.GPS.Latitude-q.gps.Latitude) > 1e-9 || math.Abs(x.GPS.Longitude-q.gps.Longitude) > 1e-9 ||
			x.GPS.HasAltitude != q.gps.HasAltitude || x.GPS.Altitude != q.gps.Altitude):
			t.Errorf("%s: GPS %+v, want %+v", q.name, *x.GPS, *q.gps)
		}
	}
}

// An offset after the time counts only without OffsetTimeOriginal.
func TestOffsetTimeOriginalWins(t *testing.T) {
	b := &tiffBuilder{order: binary.BigEndian, ifd0: []tiffEntry{}}
	n1, date := asciiValue("2025:05:04 13:14:15+02:00")
	n2, off := asciiValue("-05:00")
	b.exif = []tiffEntry{{tagDateTimeOriginal, typeASCII, n1, date}, {tagOffsetTimeOriginal, typeASCII, n2, off}}
	x := readExif(t, withSegments(plainJPEG(t), exifSegment(b.build())))
	if _, secs := x.DateTimeOriginal.Zone(); !x.OffsetKnown || secs != -5*3600 {
		t.Errorf("offset %d (known %v), want -05:00", secs, x.OffsetKnown)
	}
}

// With two Exif items, the one that describes the primary image (cdsc)
// is read, else the lowest item id; never the map's random pick.
func TestReadExifPicksTheExifItem(t *testing.T) {
	for _, c := range []struct {
		name string
		edit func(*testbuild.Options)
		want Orientation
	}{
		{"the primary's, though the other has a lower id", func(o *testbuild.Options) {
			o.SecondExif, o.SecondExifOrientation, o.SecondExifOfThumbnail = true, 3, true
		}, 6},
		{"neither linked: the lowest id", func(o *testbuild.Options) {
			o.ExifNoCdsc = true
			o.SecondExif, o.SecondExifOrientation = true, 3
		}, 3},
		{"only the other linked, to the thumbnail: still the lowest id", func(o *testbuild.Options) {
			o.ExifNoCdsc = true
			o.SecondExif, o.SecondExifOrientation, o.SecondExifOfThumbnail = true, 3, true
		}, 3},
	} {
		o := testbuild.Default()
		c.edit(&o)
		file, _ := testbuild.Build(o)
		for range 20 { // the map order changes between runs
			x, err := ReadExif(bytes.NewReader(file), int64(len(file)))
			if err != nil || x.Orientation != c.want {
				t.Fatalf("%s: orientation %d, %v; want %d", c.name, x.Orientation, err, c.want)
			}
		}
	}
}
