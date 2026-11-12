package mediameta

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"math"
	"os"
	"testing"
	"time"
)

// tiffEntry is one IFD entry for buildTIFF: its value as raw bytes in the
// file's byte order, placed inline or after the IFD as its size needs.
type tiffEntry struct {
	tag, typ uint16
	count    uint32
	value    []byte
}

// byteOrder can both read and append.
type byteOrder interface {
	binary.ByteOrder
	binary.AppendByteOrder
}

// tiffBuilder writes TIFF data with IFD0 and, optionally, an Exif and a
// GPS IFD that IFD0 points to.
type tiffBuilder struct {
	order          byteOrder
	ifd0, exif, gp []tiffEntry
}

func (b *tiffBuilder) short(v uint16) []byte { return b.order.AppendUint16(nil, v) }
func (b *tiffBuilder) long(v uint32) []byte  { return b.order.AppendUint32(nil, v) }

func (b *tiffBuilder) rationals(vs ...[2]uint32) []byte {
	var out []byte
	for _, v := range vs {
		out = b.order.AppendUint32(out, v[0])
		out = b.order.AppendUint32(out, v[1])
	}
	return out
}

func asciiValue(s string) (uint32, []byte) { return uint32(len(s) + 1), append([]byte(s), 0) }

// build lays the IFDs out one after another, each followed by the values
// that don't fit in its entries; the IFD pointers are filled in.
func (b *tiffBuilder) build() []byte {
	var hdr []byte
	if b.order.String() == binary.LittleEndian.String() {
		hdr = []byte("II*\x00")
	} else {
		hdr = []byte("MM\x00*")
	}
	hdr = b.order.AppendUint32(hdr, 8)
	ifds := [][]tiffEntry{b.ifd0}
	if b.exif != nil {
		ifds = append(ifds, b.exif)
	}
	if b.gp != nil {
		ifds = append(ifds, b.gp)
	}
	size := func(es []tiffEntry) int {
		n := 2 + 12*len(es) + 4
		for _, e := range es {
			if len(e.value) > 4 {
				n += len(e.value) + len(e.value)%2
			}
		}
		return n
	}
	// IFD0 gets its pointers first (as placeholders), so its size is
	// final; then the offsets, then the pointers' values.
	ifd0 := append([]tiffEntry(nil), b.ifd0...)
	var ptrs []int
	if b.exif != nil {
		ptrs = append(ptrs, len(ifd0))
		ifd0 = append(ifd0, tiffEntry{tagExifIFD, typeLong, 1, nil})
	}
	if b.gp != nil {
		ptrs = append(ptrs, len(ifd0))
		ifd0 = append(ifd0, tiffEntry{tagGPSIFD, typeLong, 1, nil})
	}
	ifds[0] = ifd0
	offs := []int{8}
	for _, es := range ifds[:len(ifds)-1] {
		offs = append(offs, offs[len(offs)-1]+size(es))
	}
	for i, at := range ptrs {
		ifd0[at].value = b.long(uint32(offs[i+1]))
	}
	ifds[0] = ifd0
	out := hdr
	for i, es := range ifds {
		for len(out) < offs[i] {
			out = append(out, 0)
		}
		start := len(out)
		extra := start + 2 + 12*len(es) + 4
		var tail []byte
		out = b.order.AppendUint16(out, uint16(len(es)))
		for _, e := range es {
			out = b.order.AppendUint16(out, e.tag)
			out = b.order.AppendUint16(out, e.typ)
			out = b.order.AppendUint32(out, e.count)
			if len(e.value) <= 4 {
				v := append(append([]byte(nil), e.value...), make([]byte, 4-len(e.value))...)
				out = append(out, v...)
			} else {
				out = b.order.AppendUint32(out, uint32(extra+len(tail)))
				tail = append(tail, e.value...)
				if len(e.value)%2 == 1 {
					tail = append(tail, 0)
				}
			}
		}
		out = b.order.AppendUint32(out, 0)
		out = append(out, tail...)
	}
	return out
}

// photoTIFF is TIFF data with the fields ReadExif reads: orientation 6,
// taken 2025-05-04 13:14:15.25 at +02:00, at 51°30'N 0°7'30"W, 35.5 m.
func photoTIFF(order byteOrder) []byte {
	b := &tiffBuilder{order: order}
	n1, date := asciiValue("2025:05:04 13:14:15")
	n2, off := asciiValue("+02:00")
	n3, sub := asciiValue("25")
	b.ifd0 = []tiffEntry{{tagOrientation, typeShort, 1, b.short(6)}}
	b.exif = []tiffEntry{
		{tagDateTimeOriginal, typeASCII, n1, date},
		{tagOffsetTimeOriginal, typeASCII, n2, off},
		{tagSubSecTimeOriginal, typeASCII, n3, sub},
	}
	b.gp = []tiffEntry{
		{tagGPSLatitudeRef, typeASCII, 2, []byte("N\x00")},
		{tagGPSLatitude, typeRational, 3, b.rationals([2]uint32{51, 1}, [2]uint32{30, 1}, [2]uint32{0, 1})},
		{tagGPSLongitudeRef, typeASCII, 2, []byte("W\x00")},
		{tagGPSLongitude, typeRational, 3, b.rationals([2]uint32{0, 1}, [2]uint32{7, 1}, [2]uint32{3000, 100})},
		{tagGPSAltitudeRef, typeByte, 1, []byte{0}},
		{tagGPSAltitude, typeRational, 1, b.rationals([2]uint32{355, 10})},
	}
	return b.build()
}

// plainJPEG is a small baseline JPEG from image/jpeg: a gradient with no
// metadata segments other than what the encoder writes.
func plainJPEG(t testing.TB) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 32, 24))
	for y := range 24 {
		for x := range 32 {
			img.Set(x, y, color.RGBA{uint8(x * 8), uint8(y * 10), 128, 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// segment is a marker segment with its length.
func segment(marker byte, body []byte) []byte {
	out := []byte{0xFF, marker}
	out = binary.BigEndian.AppendUint16(out, uint16(len(body)+2))
	return append(out, body...)
}

// withSegments puts segments right after a JPEG's SOI marker.
func withSegments(file []byte, segs ...[]byte) []byte {
	out := append([]byte(nil), file[:2]...)
	for _, s := range segs {
		out = append(out, s...)
	}
	return append(out, file[2:]...)
}

func exifSegment(tiff []byte) []byte {
	return segment(markerAPP1, append(append([]byte(nil), exifHeader...), tiff...))
}

func readExif(t *testing.T, file []byte) Exif {
	t.Helper()
	x, err := ReadExif(bytes.NewReader(file), int64(len(file)))
	if err != nil {
		t.Fatalf("ReadExif: %v", err)
	}
	return x
}

func TestReadExifJPEG(t *testing.T) {
	for _, order := range []byteOrder{binary.LittleEndian, binary.BigEndian} {
		t.Run(order.String(), func(t *testing.T) {
			file := withSegments(plainJPEG(t), exifSegment(photoTIFF(order)))
			if _, err := jpeg.Decode(bytes.NewReader(file)); err != nil {
				t.Fatalf("the test file doesn't decode: %v", err)
			}
			x := readExif(t, file)
			if x.Orientation != 6 {
				t.Errorf("orientation %d, want 6", x.Orientation)
			}
			want := time.Date(2025, 5, 4, 13, 14, 15, 250e6, time.FixedZone("+02:00", 2*3600))
			if !x.OffsetKnown || !x.DateTimeOriginal.Equal(want) {
				t.Errorf("taken %v (offset known %v), want %v", x.DateTimeOriginal, x.OffsetKnown, want)
			}
			if _, off := x.DateTimeOriginal.Zone(); off != 2*3600 {
				t.Errorf("zone offset %d, want 7200", off)
			}
			if x.GPS == nil {
				t.Fatal("no GPS")
			}
			lat, lon := 51.5, -(7.0/60 + 30.0/3600)
			if math.Abs(x.GPS.Latitude-lat) > 1e-9 || math.Abs(x.GPS.Longitude-lon) > 1e-9 {
				t.Errorf("position %v, %v, want %v, %v", x.GPS.Latitude, x.GPS.Longitude, lat, lon)
			}
			if !x.GPS.HasAltitude || x.GPS.Altitude != 35.5 {
				t.Errorf("altitude %v (%v), want 35.5", x.GPS.Altitude, x.GPS.HasAltitude)
			}
			if o, err := ReadOrientation(bytes.NewReader(file), int64(len(file))); err != nil || o != 6 {
				t.Errorf("ReadOrientation: %v, %v; want 6", o, err)
			}
		})
	}
}

// Without OffsetTimeOriginal the camera's clock time comes back as is, in
// UTC, with OffsetKnown false; without a fraction there is none.
func TestReadExifDateWithoutOffset(t *testing.T) {
	b := &tiffBuilder{order: binary.BigEndian}
	n, date := asciiValue("2019:12:31 23:59:59")
	b.ifd0 = []tiffEntry{}
	b.exif = []tiffEntry{{tagDateTimeOriginal, typeASCII, n, date}}
	x := readExif(t, withSegments(plainJPEG(t), exifSegment(b.build())))
	want := time.Date(2019, 12, 31, 23, 59, 59, 0, time.UTC)
	if x.OffsetKnown || !x.DateTimeOriginal.Equal(want) || x.DateTimeOriginal.Location() != time.UTC {
		t.Errorf("taken %v (offset known %v), want %v without an offset", x.DateTimeOriginal, x.OffsetKnown, want)
	}
	if x.Orientation != 0 || x.GPS != nil {
		t.Errorf("orientation %d, GPS %v: want neither", x.Orientation, x.GPS)
	}
}

// Fields that are there but unusable read as missing.
func TestReadExifRejectsBadFields(t *testing.T) {
	b := &tiffBuilder{order: binary.LittleEndian}
	n1, date := asciiValue("0000:00:00 00:00:00")
	n2, off := asciiValue("+2:00")
	b.ifd0 = []tiffEntry{{tagOrientation, typeShort, 1, b.short(9)}}
	b.exif = []tiffEntry{{tagDateTimeOriginal, typeASCII, n1, date}, {tagOffsetTimeOriginal, typeASCII, n2, off}}
	b.gp = []tiffEntry{
		{tagGPSLatitudeRef, typeASCII, 2, []byte("N\x00")},
		{tagGPSLatitude, typeRational, 3, b.rationals([2]uint32{91, 1}, [2]uint32{0, 1}, [2]uint32{0, 1})},
		{tagGPSLongitudeRef, typeASCII, 2, []byte("E\x00")},
		{tagGPSLongitude, typeRational, 3, b.rationals([2]uint32{1, 0}, [2]uint32{0, 1}, [2]uint32{0, 1})},
	}
	file := withSegments(plainJPEG(t), exifSegment(b.build()))
	x := readExif(t, file)
	if x.Orientation != 0 || !x.DateTimeOriginal.IsZero() || x.OffsetKnown || x.GPS != nil {
		t.Errorf("got %+v, want an empty Exif", x)
	}
	if o, err := ReadOrientation(bytes.NewReader(file), int64(len(file))); err != nil || o != 1 {
		t.Errorf("ReadOrientation: %v, %v; want 1 for a tag of 9", o, err)
	}
}

func TestParseOffset(t *testing.T) {
	for s, want := range map[string]int{"Z": 0, "+02:00": 7200, "-03:30": -12600, "+00:00": 0, "+14:00": 50400} {
		if got, ok := parseOffset(s); !ok || got != want {
			t.Errorf("%q: %d, %v; want %d", s, got, ok, want)
		}
	}
	for _, s := range []string{"", "z", "+2:00", "+02:0", "02:00+", "+15:00", "+02:60", "++2:00", "+-1:00", "+02:-1", "+0200 "} {
		if _, ok := parseOffset(s); ok {
			t.Errorf("%q accepted", s)
		}
	}
}

func TestReadExifWithoutExif(t *testing.T) {
	file := plainJPEG(t)
	if x := readExif(t, file); x != (Exif{}) {
		t.Errorf("got %+v, want an empty Exif", x)
	}
	if o, err := ReadOrientation(bytes.NewReader(file), int64(len(file))); err != nil || o != 1 {
		t.Errorf("ReadOrientation: %v, %v; want 1", o, err)
	}
	// An APP1 that holds XMP, not Exif, is passed over.
	xmp := segment(markerAPP1, []byte("http://ns.adobe.com/xap/1.0/\x00<x:xmpmeta/>"))
	if x := readExif(t, withSegments(file, xmp, exifSegment(photoTIFF(binary.BigEndian)))); x.Orientation != 6 {
		t.Errorf("orientation %d after an XMP APP1, want 6", x.Orientation)
	}
}

// IFD pointers that loop back, or point outside the data, read as no IFD.
func TestReadExifHostileIFDs(t *testing.T) {
	order := binary.BigEndian
	tiff := []byte("MM\x00*\x00\x00\x00\x08")
	tiff = order.AppendUint16(tiff, 3)
	for _, e := range []struct {
		tag uint16
		at  uint32
	}{{tagExifIFD, 8}, {tagGPSIFD, 0xFFFFFFF0}, {tagOrientation, 0}} {
		tiff = order.AppendUint16(tiff, e.tag)
		if e.tag == tagOrientation {
			tiff = order.AppendUint16(tiff, typeShort)
			tiff = order.AppendUint32(tiff, 0xFFFFFFFF) // a count far past the data
			tiff = order.AppendUint32(tiff, 0)
			continue
		}
		tiff = order.AppendUint16(tiff, typeLong)
		tiff = order.AppendUint32(tiff, 1)
		tiff = order.AppendUint32(tiff, e.at)
	}
	tiff = order.AppendUint32(tiff, 0)
	if x := readExif(t, withSegments(plainJPEG(t), exifSegment(tiff))); x != (Exif{}) {
		t.Errorf("got %+v, want an empty Exif", x)
	}
}

func TestParseJPEGErrors(t *testing.T) {
	good := withSegments(plainJPEG(t), exifSegment(photoTIFF(binary.BigEndian)))
	sos := bytes.Index(good, []byte{0xFF, markerSOS})
	for name, c := range map[string]struct {
		file []byte
		want error
	}{
		"empty":                 {nil, ErrNotJPEG},
		"PNG":                   {[]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\x0dIHDR"), ErrNotJPEG},
		"SOI only":              {[]byte{0xFF, 0xD8, 0xFF}, ErrMalformed},
		"before the scan":       {good[:sos], ErrMalformed},
		"EOI before the scan":   {[]byte{0xFF, 0xD8, 0xFF, 0xD9}, ErrMalformed},
		"second SOI":            {[]byte{0xFF, 0xD8, 0xFF, 0xD8, 0xFF, 0xDA, 0, 2}, ErrMalformed},
		"length below 2":        {[]byte{0xFF, 0xD8, 0xFF, 0xE1, 0, 1, 0xFF, 0xDA, 0, 2}, ErrMalformed},
		"length past the end":   {[]byte{0xFF, 0xD8, 0xFF, 0xE1, 0x10, 0, 0xFF, 0xDA, 0, 2}, ErrMalformed},
		"garbage between marks": {[]byte{0xFF, 0xD8, 0xFF, 0xE1, 0, 2, 0x12, 0xFF, 0xDA, 0, 2}, ErrMalformed},
	} {
		_, err := ReadExif(bytes.NewReader(c.file), int64(len(c.file)))
		if !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", name, err, c.want)
		}
	}
	// Neither format: the error is both, so callers that check for a HEIF
	// file keep working.
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\x0dIHDR")
	if _, err := ReadExif(bytes.NewReader(png), int64(len(png))); !errors.Is(err, ErrNotHEIF) {
		t.Errorf("ReadExif(PNG): %v, want ErrNotHEIF too", err)
	}
	// Fill bytes before a marker and a restart marker are fine.
	fill := withSegments(good, []byte{0xFF, 0xFF, 0xFF, markerRST0})
	if x := readExif(t, fill); x.Orientation != 6 {
		t.Errorf("with fill bytes: orientation %d, want 6", x.Orientation)
	}
}

// The Exif item of a HEIF file reads the same way.
func TestReadExifHEIF(t *testing.T) {
	for _, c := range []struct {
		path string
		o    Orientation
	}{{"testdata/iphone/grid-gainmap-orientation6.heic", 6}, {"testdata/imageio/b-orientation6.heic", 6}} {
		file := readFile(t, c.path)
		x := readExif(t, file)
		if x.Orientation != c.o {
			t.Errorf("%s: orientation %d, want %d", c.path, x.Orientation, c.o)
		}
	}
	file := readFile(t, "testdata/iphone/grid-gainmap-orientation6.heic")
	if x := readExif(t, file); x.DateTimeOriginal.IsZero() || !x.OffsetKnown {
		t.Errorf("iPhone file: taken %v (offset known %v), want a time with its offset", x.DateTimeOriginal, x.OffsetKnown)
	}
}

func jpegSeeds(f *testing.F) {
	plain := plainJPEG(f)
	f.Add(plain)
	f.Add(withSegments(plain, exifSegment(photoTIFF(binary.LittleEndian))))
	f.Add(withSegments(plain, exifSegment(photoTIFF(binary.BigEndian))))
	for _, q := range quirks(f) {
		f.Add(q.file)
	}
}

func FuzzReadExif(f *testing.F) {
	seeds(f)
	jpegSeeds(f)
	f.Fuzz(func(t *testing.T, data []byte) {
		withinDeadline(t, func() {
			x, err := ReadExif(bytes.NewReader(data), int64(len(data)))
			if err != nil {
				return
			}
			if x.Orientation != 0 && !x.Orientation.Valid() {
				t.Errorf("orientation %d", x.Orientation)
			}
			if g := x.GPS; g != nil && (math.Abs(g.Latitude) > 90 || math.Abs(g.Longitude) > 180 || math.IsNaN(g.Altitude)) {
				t.Errorf("position %+v", *g)
			}
			o, err := ReadOrientation(bytes.NewReader(data), int64(len(data)))
			if isJPEG(bytes.NewReader(data), int64(len(data))) && (err != nil || !o.Valid()) {
				t.Errorf("ReadExif read the JPEG, ReadOrientation: %v, %v", o, err)
			}
		})
	})
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
