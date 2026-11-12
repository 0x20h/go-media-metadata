package mediameta

import (
	"bytes"
	"encoding/binary"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// richTIFF is TIFF data with more than this package edits: a Make and an
// unknown tag in IFD0, a maker note with offsets of its own in the Exif
// IFD, a GPS IFD with a timestamp, and the date fields.
func richTIFF(order byteOrder) []byte {
	b := &tiffBuilder{order: order}
	nm, make_ := asciiValue("Phonecam")
	n1, date := asciiValue("2019:09:09 09:09:09")
	n2, off := asciiValue("+09:30")
	maker := make([]byte, 64)
	for i := range maker {
		maker[i] = byte(0xA0 + i%16)
	}
	b.ifd0 = []tiffEntry{
		{0x010F, typeASCII, nm, make_},
		{tagOrientation, typeShort, 1, b.short(1)},
		{0xC000, typeUndef, 40, bytes.Repeat([]byte{0x5A}, 40)},
	}
	b.exif = []tiffEntry{
		{tagDateTimeOriginal, typeASCII, n1, date},
		{tagOffsetTimeOriginal, typeASCII, n2, off},
		{tagMakerNote, typeUndef, uint32(len(maker)), maker},
	}
	b.gp = []tiffEntry{
		{tagGPSLatitudeRef, typeASCII, 2, []byte("N\x00")},
		{tagGPSLatitude, typeRational, 3, b.rationals([2]uint32{51, 1}, [2]uint32{30, 1}, [2]uint32{2659, 100})},
		{tagGPSLongitudeRef, typeASCII, 2, []byte("W\x00")},
		{tagGPSLongitude, typeRational, 3, b.rationals([2]uint32{0, 1}, [2]uint32{7, 1}, [2]uint32{3000, 100})},
		{7, typeRational, 3, b.rationals([2]uint32{12, 1}, [2]uint32{34, 1}, [2]uint32{56, 1})}, // GPSTimeStamp
	}
	return b.build()
}

// tiffTags reads every entry of IFD0 and the Exif and GPS IFDs: tag (with
// its IFD) to type, count, value bytes and, for an out-of-line value, its
// offset.
type tiffTag struct {
	typ   uint16
	count int
	value string
	at    int64
}

func tiffTags(t *testing.T, data []byte) map[[2]uint16]tiffTag {
	t.Helper()
	r, first, ok := newTIFF(data)
	if !ok {
		t.Fatal("not TIFF")
	}
	out := map[[2]uint16]tiffTag{}
	var read func(ifd uint16, off uint32)
	read = func(ifd uint16, off uint32) {
		n := int(r.order.Uint16(data[off:]))
		for i := range n {
			e := data[int(off)+2+12*i:]
			tag, typ, count := r.order.Uint16(e), r.order.Uint16(e[2:]), r.order.Uint32(e[4:])
			size := typeSize(typ) * int(count)
			v, at := e[8:8+min(size, 4)], int64(-1)
			if size > 4 {
				at = int64(r.order.Uint32(e[8:]))
				v = data[at : at+int64(size)]
			}
			out[[2]uint16{ifd, tag}] = tiffTag{typ, int(count), string(v), at}
			if ifd == 0 && (tag == tagExifIFD || tag == tagGPSIFD) {
				read(tag, r.order.Uint32(e[8:]))
			}
		}
	}
	read(0, first)
	return out
}

var editAll = ExifEdit{
	Orientation:      8,
	DateTimeOriginal: ptr(time.Date(2024, 2, 29, 8, 7, 6, 500_000_000, time.FixedZone("", 5*3600+1800))),
	OffsetKnown:      true,
	GPS:              &GPS{Latitude: -33.8568, Longitude: 151.2153, Altitude: -12.5, HasAltitude: true},
}

func ptr[T any](v T) *T { return &v }

func checkEdited(t *testing.T, data []byte, e ExifEdit) {
	t.Helper()
	x := parseExif(data)
	if e.Orientation != 0 && x.Orientation != e.Orientation {
		t.Errorf("orientation %d, want %d", x.Orientation, e.Orientation)
	}
	if d := e.DateTimeOriginal; d != nil {
		if x.DateTimeOriginal.Format("2006-01-02 15:04:05.999999999") != d.Format("2006-01-02 15:04:05.999999999") {
			t.Errorf("taken %v, want %v", x.DateTimeOriginal, d)
		}
		if x.OffsetKnown != e.OffsetKnown {
			t.Errorf("offset known %v, want %v", x.OffsetKnown, e.OffsetKnown)
		} else if e.OffsetKnown {
			_, got := x.DateTimeOriginal.Zone()
			_, want := d.Zone()
			if got != want {
				t.Errorf("offset %d, want %d", got, want)
			}
		}
	}
	if g := e.GPS; g != nil {
		if x.GPS == nil {
			t.Fatal("no GPS read back")
		}
		if math.Abs(x.GPS.Latitude-g.Latitude) > 1e-7 || math.Abs(x.GPS.Longitude-g.Longitude) > 1e-7 {
			t.Errorf("position %v, %v; want %v, %v", x.GPS.Latitude, x.GPS.Longitude, g.Latitude, g.Longitude)
		}
		if x.GPS.HasAltitude != g.HasAltitude || math.Abs(x.GPS.Altitude-g.Altitude) > 1e-3 {
			t.Errorf("altitude %v (%v), want %v (%v)", x.GPS.Altitude, x.GPS.HasAltitude, g.Altitude, g.HasAltitude)
		}
	}
	if e.RemoveGPS && x.GPS != nil {
		t.Errorf("GPS %+v after removing it", x.GPS)
	}
}

// Every edit reads back, on data with and without the IFDs it needs, in
// both byte orders, and on no data at all.
func TestEditExif(t *testing.T) {
	inputs := map[string][]byte{
		"photo II": photoTIFF(binary.LittleEndian),
		"photo MM": photoTIFF(binary.BigEndian),
		"rich II":  richTIFF(binary.LittleEndian),
		"rich MM":  richTIFF(binary.BigEndian),
		"none":     nil,
		"bare": (&tiffBuilder{order: binary.LittleEndian, ifd0: []tiffEntry{
			{tagOrientation, typeShort, 1, binary.LittleEndian.AppendUint16(nil, 3)}}}).build(),
	}
	edits := map[string]ExifEdit{
		"all":            editAll,
		"orientation":    {Orientation: 5},
		"date no offset": {DateTimeOriginal: ptr(time.Date(2001, 1, 2, 3, 4, 5, 0, time.UTC))},
		"gps no alt":     {GPS: &GPS{Latitude: 89.9999, Longitude: -179.5}},
		"remove gps":     {RemoveGPS: true},
	}
	for in, data := range inputs {
		for en, e := range edits {
			t.Run(in+"/"+en, func(t *testing.T) {
				out, err := EditExif(data, e)
				if err != nil {
					t.Fatal(err)
				}
				checkEdited(t, out, e)
				// Twice is once: the same edit again changes nothing more.
				again, err := EditExif(out, e)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(parseExif(again), parseExif(out)) {
					t.Error("editing again reads differently")
				}
				checkEdited(t, again, e)
			})
		}
	}
}

// What the edit doesn't touch stays as it was, at the same offsets: the
// unknown tags, the GPS timestamp, and the maker note, whose own offsets
// count from the start of the data.
func TestEditExifKeepsTheRest(t *testing.T) {
	for _, order := range []byteOrder{binary.LittleEndian, binary.BigEndian} {
		data := richTIFF(order)
		before := tiffTags(t, data)
		out, err := EditExif(data, editAll)
		if err != nil {
			t.Fatal(err)
		}
		after := tiffTags(t, out)
		edited := map[uint16]bool{tagOrientation: true, tagExifIFD: true, tagGPSIFD: true,
			tagDateTimeOriginal: true, tagOffsetTimeOriginal: true, tagSubSecTimeOriginal: true,
			tagGPSLatitudeRef: true, tagGPSLatitude: true, tagGPSLongitudeRef: true, tagGPSLongitude: true,
			tagGPSAltitudeRef: true, tagGPSAltitude: true, tagGPSVersionID: true}
		for k, v := range before {
			if edited[k[1]] {
				continue
			}
			if after[k] != v {
				t.Errorf("%v: tag %#x changed: %+v → %+v", order, k[1], v, after[k])
			}
		}
		mn := before[[2]uint16{tagExifIFD, tagMakerNote}]
		if !bytes.Equal(out[mn.at:mn.at+int64(len(mn.value))], data[mn.at:mn.at+int64(len(mn.value))]) {
			t.Errorf("%v: the maker note's bytes moved", order)
		}
	}
}

// A date of the same length is written where the old one was: the data
// keeps its length, so a HEIF Exif item can be patched in place.
func TestEditExifSameLength(t *testing.T) {
	data := photoTIFF(binary.BigEndian)
	d := time.Date(2030, 12, 24, 18, 0, 1, 750_000_000, time.FixedZone("", -4*3600))
	out, err := EditExif(data, ExifEdit{DateTimeOriginal: &d, OffsetKnown: true, Orientation: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != len(data) {
		t.Errorf("%d bytes, was %d", len(out), len(data))
	}
	if n := differing(out, data); n > 20+3+7+1 {
		t.Errorf("%d bytes differ", n)
	}
}

// Old values aren't left in the data: a removed offset and GPS position,
// and an IFD that moved, are zeroed.
func TestEditExifZeroesOldValues(t *testing.T) {
	data := richTIFF(binary.LittleEndian)
	lat := binary.LittleEndian.AppendUint32(binary.LittleEndian.AppendUint32(nil, 2659), 100)
	if !bytes.Contains(data, []byte("+09:30")) || !bytes.Contains(data, lat) {
		t.Fatal("the fixture lacks its marks")
	}
	out, err := EditExif(data, ExifEdit{
		DateTimeOriginal: ptr(time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)), // offset removed
		RemoveGPS:        true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(out, []byte("+09:30")) {
		t.Error("the removed offset is still in the data")
	}
	if bytes.Contains(out, lat) {
		t.Error("the removed latitude is still in the data")
	}
	if bytes.Contains(out, []byte("2019:09:09")) {
		t.Error("the old date is still in the data")
	}
	// IFD0 moves when it gains an entry: its old place is zeroed.
	bare := (&tiffBuilder{order: binary.LittleEndian, ifd0: []tiffEntry{
		{0x010F, typeASCII, 4, []byte("Cam\x00")}, {tagOrientation, typeShort, 1, binary.LittleEndian.AppendUint16(nil, 3)}}}).build()
	out, err = EditExif(bare, ExifEdit{GPS: &GPS{Latitude: 1, Longitude: 2}})
	if err != nil {
		t.Fatal(err)
	}
	if first := binary.LittleEndian.Uint32(out[4:]); first == 8 {
		t.Fatal("IFD0 didn't move")
	}
	if !bytes.Equal(out[8:8+2+12*2+4], make([]byte, 2+12*2+4)) {
		t.Error("IFD0's old place isn't zeroed")
	}
	checkEdited(t, out, ExifEdit{GPS: &GPS{Latitude: 1, Longitude: 2}})
}

func TestEditExifErrors(t *testing.T) {
	data := photoTIFF(binary.LittleEndian)
	for name, e := range map[string]ExifEdit{
		"orientation 9":  {Orientation: 9},
		"latitude 91":    {GPS: &GPS{Latitude: 91}},
		"NaN":            {GPS: &GPS{Latitude: math.NaN()}},
		"set and remove": {GPS: &GPS{}, RemoveGPS: true},
		"year 10000":     {DateTimeOriginal: ptr(time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC))},
	} {
		if _, err := EditExif(data, e); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	for name, bad := range map[string][]byte{
		"not TIFF":          []byte("Exif\x00\x00II*\x00"),
		"IFD0 outside":      {'I', 'I', 42, 0, 0xFF, 0, 0, 0},
		"truncated entries": {'I', 'I', 42, 0, 8, 0, 0, 0, 5, 0, 1, 2},
	} {
		if _, err := EditExif(bad, ExifEdit{Orientation: 1}); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

// ExifTool reads the edits from a JPEG's Exif segment and finds nothing
// to warn about in the structure. Runs where exiftool is installed.
func TestEditExifAgreesWithExifTool(t *testing.T) {
	tool, err := exec.LookPath("exiftool")
	if err != nil {
		t.Skip("exiftool not installed")
	}
	dir := t.TempDir()
	for _, order := range []byteOrder{binary.LittleEndian, binary.BigEndian} {
		for _, start := range [][]byte{richTIFF(order), nil} {
			out, err := EditExif(start, editAll)
			if err != nil {
				t.Fatal(err)
			}
			p := filepath.Join(dir, "x.jpg")
			if err := os.WriteFile(p, withSegments(plainJPEG(t), exifSegment(out)), 0o644); err != nil {
				t.Fatal(err)
			}
			got, err := exec.Command(tool, "-validate", "-warning", "-error", "-a", "-s", "-n",
				"-EXIF:Orientation", "-EXIF:DateTimeOriginal", "-EXIF:OffsetTimeOriginal", "-EXIF:SubSecTimeOriginal",
				"-Composite:GPSLatitude", "-Composite:GPSLongitude", "-Composite:GPSAltitude", p).CombinedOutput()
			if err != nil {
				t.Fatalf("exiftool: %v\n%s", err, got)
			}
			s := string(got)
			for _, want := range []string{"Orientation                     : 8", "2024:02:29 08:07:06", "+05:30",
				"SubSecTimeOriginal              : 5", "GPSLatitude                     : -33.8568", "GPSLongitude                    : 151.2153"} {
				if !strings.Contains(s, want) {
					t.Errorf("%v: ExifTool lacks %q:\n%s", order, want, s)
				}
			}
			for _, line := range strings.Split(s, "\n") {
				// The fixture's own gaps (tags a camera's JPEG has, an
				// unknown tag and maker note) are ExifTool's minor
				// warnings; anything about the structure is not.
				if strings.Contains(line, "[minor]") || strings.Contains(line, "Missing required JPEG") {
					continue
				}
				if strings.HasPrefix(line, "Warning") || strings.HasPrefix(line, "Error") {
					t.Errorf("%v: ExifTool: %s", order, line)
				}
			}
		}
	}
}

func FuzzEditExif(f *testing.F) {
	f.Add(photoTIFF(binary.LittleEndian))
	f.Add(photoTIFF(binary.BigEndian))
	f.Add(richTIFF(binary.LittleEndian))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, data []byte) {
		withinDeadline(t, func() {
			for _, e := range []ExifEdit{editAll, {RemoveGPS: true}, {Orientation: 2}} {
				out, err := EditExif(data, e)
				if err != nil {
					continue
				}
				checkEdited(t, out, e)
			}
		})
	})
}

// The edit reaches what a reader reads: a GPS IFD pointer stored as SHORT
// (which ReadExif accepts) and a GPS tag repeated in its IFD.
func TestEditExifMatchesTheReader(t *testing.T) {
	order := binary.LittleEndian
	ph := photoTIFF(order)
	r, first, _ := newTIFF(ph)
	gpsOff, _ := r.uint(r.ifd(first)[tagGPSIFD])
	var gps []tiffEntry
	for tag, f := range r.ifd(gpsOff) {
		gps = append(gps, tiffEntry{tag, f.typ, uint32(f.count), f.value})
	}
	sortEntries(gps)
	// The latitude twice: the first copy invalid (no value), the second
	// the one a reader takes.
	lat := gps[1]
	gps = append([]tiffEntry{gps[0], {lat.tag, typeRational, 0, nil}}, gps[1:]...)
	b := &tiffBuilder{order: order, gp: gps,
		ifd0: []tiffEntry{{tagOrientation, typeShort, 1, binary.LittleEndian.AppendUint16(nil, 1)}}}
	data := b.build()
	// The builder writes the GPS pointer as LONG; make it SHORT.
	_, f0, _ := newTIFF(data)
	n := int(order.Uint16(data[f0:]))
	for i := range n {
		e := data[int(f0)+2+12*i:]
		if order.Uint16(e) == tagGPSIFD {
			off := order.Uint32(e[8:])
			order.PutUint16(e[2:], typeShort)
			order.PutUint32(e[8:], 0)
			order.PutUint16(e[8:], uint16(off))
		}
	}
	if parseExif(data).GPS == nil {
		t.Fatal("the fixture's GPS doesn't read")
	}
	out, err := EditExif(data, ExifEdit{RemoveGPS: true})
	if err != nil {
		t.Fatal(err)
	}
	if g := parseExif(out).GPS; g != nil {
		t.Errorf("GPS %+v after removing it", g)
	}
	out, err = EditExif(data, ExifEdit{GPS: &GPS{Latitude: -12.5, Longitude: 33.25}})
	if err != nil {
		t.Fatal(err)
	}
	if g := parseExif(out).GPS; g == nil || g.Latitude != -12.5 || g.Longitude != 33.25 {
		t.Errorf("GPS %+v after setting it", g)
	}
}

func sortEntries(es []tiffEntry) {
	for i := 1; i < len(es); i++ {
		for j := i; j > 0 && es[j].tag < es[j-1].tag; j-- {
			es[j], es[j-1] = es[j-1], es[j]
		}
	}
}
