package mediameta

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// exiftoolCorpus are the files ReadExif is compared with ExifTool on: the
// synthetic JPEGs of the tests, and the sample HEIF files.
func exiftoolCorpus(t *testing.T) map[string][]byte {
	t.Helper()
	plain := plainJPEG(t)
	noOffset := &tiffBuilder{order: binary.LittleEndian}
	n, date := asciiValue("2019:12:31 23:59:59")
	noOffset.ifd0 = []tiffEntry{{tagOrientation, typeShort, 1, noOffset.short(3)}}
	noOffset.exif = []tiffEntry{{tagDateTimeOriginal, typeASCII, n, date}}
	files := map[string][]byte{
		"plain.jpg":     plain,
		"photo-ii.jpg":  withSegments(plain, exifSegment(photoTIFF(binary.LittleEndian))),
		"photo-mm.jpg":  withSegments(plain, exifSegment(photoTIFF(binary.BigEndian))),
		"no-offset.jpg": withSegments(plain, exifSegment(noOffset.build())),
	}
	for _, p := range []string{
		"testdata/iphone/grid-gainmap-orientation6.heic",
		"testdata/imageio/a-orientation1.heic",
		"testdata/imageio/b-orientation6.heic",
	} {
		files[filepath.Base(p)] = readFile(t, p)
	}
	for _, q := range quirks(t) {
		if q.exiftool {
			files["quirk-"+q.name+".jpg"] = q.file
		}
	}
	return files
}

// exiftoolFields is ExifTool's JSON for one file with -n (numbers as
// numbers, GPS signed).
type exiftoolFields struct {
	SourceFile         string
	Orientation        *int
	DateTimeOriginal   string
	OffsetTimeOriginal string
	SubSecTimeOriginal json.Number
	GPSLatitude        *float64
	GPSLongitude       *float64
	GPSAltitude        *float64
}

// ReadExif agrees with ExifTool on every file of the corpus. Runs where
// exiftool is installed; skipped elsewhere (CI).
func TestExifAgreesWithExifTool(t *testing.T) {
	tool, err := exec.LookPath("exiftool")
	if err != nil {
		t.Skip("exiftool not installed")
	}
	dir := t.TempDir()
	files := exiftoolCorpus(t)
	for name, b := range files {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out, err := exec.Command(tool, "-j", "-n", "-q",
		"-EXIF:Orientation", "-EXIF:DateTimeOriginal", "-EXIF:OffsetTimeOriginal", "-EXIF:SubSecTimeOriginal",
		"-Composite:GPSLatitude", "-Composite:GPSLongitude", "-Composite:GPSAltitude", dir).Output()
	if err != nil {
		t.Fatalf("exiftool: %v", err)
	}
	var got []exiftoolFields
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("exiftool's JSON: %v\n%s", err, out)
	}
	if len(got) != len(files) {
		t.Fatalf("exiftool read %d files, want %d", len(got), len(files))
	}
	for _, e := range got {
		name := filepath.Base(e.SourceFile)
		x := readExif(t, files[name])
		want := Orientation(0)
		if e.Orientation != nil && Orientation(*e.Orientation).Valid() {
			want = Orientation(*e.Orientation)
		}
		if x.Orientation != want {
			t.Errorf("%s: orientation %d, ExifTool %d", name, x.Orientation, want)
		}
		var taken time.Time
		if e.DateTimeOriginal != "" {
			taken, _ = time.ParseInLocation(exifDateLayout, e.DateTimeOriginal, time.UTC)
			if s := e.SubSecTimeOriginal.String(); s != "" {
				d, _ := time.ParseDuration("0." + s + "s")
				taken = taken.Add(d)
			}
		}
		if wall := x.DateTimeOriginal.Format("2006-01-02T15:04:05.999999999"); wall != taken.Format("2006-01-02T15:04:05.999999999") {
			t.Errorf("%s: taken %s, ExifTool %s", name, wall, taken)
		}
		if x.OffsetKnown != (e.OffsetTimeOriginal != "") {
			t.Errorf("%s: offset known %v, ExifTool %q", name, x.OffsetKnown, e.OffsetTimeOriginal)
		} else if x.OffsetKnown && x.DateTimeOriginal.Location().String() != strings.TrimSpace(e.OffsetTimeOriginal) {
			t.Errorf("%s: offset %s, ExifTool %s", name, x.DateTimeOriginal.Location(), e.OffsetTimeOriginal)
		}
		if (x.GPS != nil) != (e.GPSLatitude != nil && e.GPSLongitude != nil) {
			t.Errorf("%s: GPS %+v, ExifTool %v, %v", name, x.GPS, e.GPSLatitude, e.GPSLongitude)
			continue
		}
		if x.GPS == nil {
			continue
		}
		if math.Abs(x.GPS.Latitude-*e.GPSLatitude) > 1e-6 || math.Abs(x.GPS.Longitude-*e.GPSLongitude) > 1e-6 {
			t.Errorf("%s: position %v, %v; ExifTool %v, %v", name, x.GPS.Latitude, x.GPS.Longitude, *e.GPSLatitude, *e.GPSLongitude)
		}
		if x.GPS.HasAltitude != (e.GPSAltitude != nil) || (e.GPSAltitude != nil && math.Abs(x.GPS.Altitude-*e.GPSAltitude) > 1e-6) {
			t.Errorf("%s: altitude %v (%v), ExifTool %v", name, x.GPS.Altitude, x.GPS.HasAltitude, e.GPSAltitude)
		}
	}
}

// The JPEGs SetOrientation writes, the tag patched in place or a new Exif
// segment, read as that orientation in ExifTool and pass its validation.
func TestJPEGWritesAgreeWithExifTool(t *testing.T) {
	tool, err := exec.LookPath("exiftool")
	if err != nil {
		t.Skip("exiftool not installed")
	}
	dir := t.TempDir()
	jfif := segment(markerAPP0, []byte("JFIF\x00\x01\x02\x00\x00\x01\x00\x01\x00\x00"))
	want := map[string]int{}
	for name, src := range map[string][]byte{
		"patched-ii": orientationJPEG(t, binary.LittleEndian, typeShort, 1),
		"patched-mm": orientationJPEG(t, binary.BigEndian, typeShort, 1),
		"inserted":   plainJPEG(t),
		"after-jfif": withSegments(plainJPEG(t), jfif),
	} {
		if err := os.WriteFile(filepath.Join(dir, name+".jpg"), src, 0o644); err != nil {
			t.Fatal(err)
		}
		want[name+".jpg"] = 0 // the source
		for _, o := range []Orientation{3, 6, 7} {
			file := fmt.Sprintf("%s-%d.jpg", name, o)
			if err := os.WriteFile(filepath.Join(dir, file), setOrientationJPEG(t, src, o), 0o644); err != nil {
				t.Fatal(err)
			}
			want[file] = int(o)
		}
	}
	out, err := exec.Command(tool, "-j", "-n", "-q", "-EXIF:Orientation", "-validate", "-warning", "-error", dir).Output()
	if err != nil {
		t.Fatalf("exiftool: %v", err)
	}
	var got []struct {
		SourceFile  string
		Orientation int
		Validate    string
		Warning     string
		Error       string
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("exiftool's JSON: %v\n%s", err, out)
	}
	if len(got) != len(want) {
		t.Fatalf("exiftool read %d files, want %d", len(got), len(want))
	}
	// A write adds no warning its source didn't have. (The synthetic
	// sources lack tags Exif requires, which ExifTool warns about.) A new
	// segment validates clean.
	source := map[string]string{}
	for _, e := range got {
		if name := filepath.Base(e.SourceFile); want[name] == 0 {
			source[strings.TrimSuffix(name, ".jpg")] = e.Warning
		}
	}
	for _, e := range got {
		name := filepath.Base(e.SourceFile)
		if want[name] == 0 {
			continue
		}
		if e.Orientation != want[name] {
			t.Errorf("%s: ExifTool reads orientation %d, want %d", name, e.Orientation, want[name])
		}
		from := name[:strings.LastIndexByte(name, '-')]
		if e.Error != "" || e.Warning != source[from] {
			t.Errorf("%s: ExifTool: warning %q (its source: %q), error %q", name, e.Warning, source[from], e.Error)
		}
		if (from == "inserted" || from == "after-jfif") && e.Validate != "0 0 0" { // errors, warnings, minor warnings (-n)
			t.Errorf("%s: ExifTool's validation: %q, %q", name, e.Validate, e.Warning)
		}
	}
}
