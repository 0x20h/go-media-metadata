package mediameta

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image/jpeg"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// jpegFiles are JPEGs to edit: without Exif, with a JFIF header, and with
// Exif in either byte order.
func jpegFiles(t testing.TB) map[string][]byte {
	plain := plainJPEG(t)
	jfif := segment(markerAPP0, []byte("JFIF\x00\x01\x02\x00\x00\x01\x00\x01\x00\x00"))
	return map[string][]byte{
		"plain":        plain,
		"jfif":         withSegments(plain, jfif),
		"jfif+exif":    withSegments(plain, jfif, exifSegment(photoTIFF(binary.BigEndian))),
		"photo little": withSegments(plain, exifSegment(photoTIFF(binary.LittleEndian))),
		"rich big":     withSegments(plain, exifSegment(richTIFF(binary.BigEndian))),
	}
}

// jpegScan is a JPEG's bytes from its SOS segment on: the image data.
func jpegScan(t *testing.T, file []byte) []byte {
	t.Helper()
	f, err := parseJPEG(bytes.NewReader(file), int64(len(file)))
	if err != nil {
		t.Fatal(err)
	}
	return file[f.segs[len(f.segs)-1].offset:]
}

// jpegExifTIFF is the TIFF data of a JPEG's first Exif segment.
func jpegExifTIFF(t *testing.T, file []byte) []byte {
	t.Helper()
	f, err := parseJPEG(bytes.NewReader(file), int64(len(file)))
	if err != nil {
		t.Fatal(err)
	}
	_, payload, ok, err := f.exifSegment(bytes.NewReader(file))
	if err != nil || !ok {
		t.Fatalf("no Exif segment: %v", err)
	}
	return payload[len(exifHeader):]
}

// countExif is how many Exif APP1 segments a JPEG has.
func countExif(t *testing.T, file []byte) int {
	t.Helper()
	f, err := parseJPEG(bytes.NewReader(file), int64(len(file)))
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, s := range f.segs {
		if s.marker == markerAPP1 && bytes.HasPrefix(file[s.body:], exifHeader[:5]) {
			n++
		}
	}
	return n
}

// Every edit of every JPEG reads back, keeps the image data and the
// other segments, leaves one Exif segment, and keeps the plan's
// invariants.
func TestPlanExifEditJPEG(t *testing.T) {
	d := time.Date(2031, 7, 8, 9, 10, 11, 120_000_000, time.FixedZone("", -7*3600))
	for name, file := range jpegFiles(t) {
		for _, c := range []struct {
			name string
			edit ExifEdit
		}{
			{"all", editAll},
			{"dates", ExifEdit{DateTimeOriginal: &d, DateTimeDigitized: &d, OffsetKnown: true}},
			{"gps", ExifEdit{GPS: &GPS{Latitude: 48.8584, Longitude: 2.2945}}},
			{"remove gps", ExifEdit{RemoveGPS: true}},
			{"orientation", ExifEdit{Orientation: 3}},
		} {
			t.Run(name+"/"+c.name, func(t *testing.T) {
				p, err := PlanExifEdit(bytes.NewReader(file), int64(len(file)), c.edit)
				if err != nil {
					t.Fatal(err)
				}
				checkPlan(t, file, p)
				out := written(t, p)
				checkEdited(t, jpegExifTIFF(t, out), c.edit)
				if !bytes.Equal(jpegScan(t, out), jpegScan(t, file)) {
					t.Error("the image data changed")
				}
				if n := countExif(t, out); n != 1 {
					t.Errorf("%d Exif segments", n)
				}
				if _, err := jpeg.Decode(bytes.NewReader(out)); err != nil {
					t.Errorf("decode: %v", err)
				}
				if strings.HasPrefix(name, "jfif") && !bytes.Equal(out[2:4], []byte{0xFF, markerAPP0}) {
					t.Error("the JFIF segment isn't first")
				}
			})
		}
	}
}

// An edit that fits is in place and leaves nothing of the old values; one
// that doesn't replaces the segment, and the old one isn't copied.
func TestPlanExifEditJPEGOldValuesGone(t *testing.T) {
	file := withSegments(plainJPEG(t), exifSegment(richTIFF(binary.BigEndian)))
	lat := binary.BigEndian.AppendUint32(binary.BigEndian.AppendUint32(nil, 2659), 100)
	if !bytes.Contains(file, lat) {
		t.Fatal("the fixture's latitude isn't there")
	}
	for _, c := range []struct {
		name    string
		edit    ExifEdit
		inPlace bool
	}{
		{"remove gps", ExifEdit{RemoveGPS: true}, true},
		{"move", editAll, false},
	} {
		p, err := PlanExifEdit(bytes.NewReader(file), int64(len(file)), c.edit)
		if err != nil {
			t.Fatal(err)
		}
		if p.InPlace != c.inPlace {
			t.Errorf("%s: in place %v, want %v", c.name, p.InPlace, c.inPlace)
		}
		checkPlan(t, file, p)
		if out := written(t, p); bytes.Contains(out, lat) {
			t.Errorf("%s: the old latitude is still in the file", c.name)
		}
	}
}

// A new date of the same shape as the old is in place, and writing the
// same edit again changes nothing.
func TestPlanExifEditJPEGDateInPlace(t *testing.T) {
	d := time.Date(2031, 7, 8, 9, 10, 11, 120_000_000, time.FixedZone("", -7*3600))
	edit := ExifEdit{DateTimeOriginal: &d, OffsetKnown: true}
	file := withSegments(plainJPEG(t), exifSegment(photoTIFF(binary.LittleEndian)))
	p, err := PlanExifEdit(bytes.NewReader(file), int64(len(file)), edit)
	if err != nil {
		t.Fatal(err)
	}
	if !p.InPlace {
		t.Fatalf("not in place (%d → %d bytes)", p.Size, p.NewSize)
	}
	out := written(t, p)
	again, err := PlanExifEdit(bytes.NewReader(out), int64(len(out)), edit)
	if err != nil {
		t.Fatal(err)
	}
	if !again.InPlace || !bytes.Equal(written(t, again), out) {
		t.Error("the same edit again changes the file")
	}
}

func TestPlanExifJPEGErrors(t *testing.T) {
	file := plainJPEG(t)
	big := &tiffBuilder{order: binary.BigEndian}
	big.ifd0 = []tiffEntry{{0xC000, typeUndef, 70000, make([]byte, 70000)}}
	if _, err := PlanExif(bytes.NewReader(file), int64(len(file)), big.build()); !errors.Is(err, ErrUnsupported) {
		t.Errorf("Exif past 64 KB: %v", err)
	}
	broken := file[:bytes.Index(file, []byte{0xFF, markerSOS})]
	if _, err := PlanExifEdit(bytes.NewReader(broken), int64(len(broken)), editAll); !errors.Is(err, ErrMalformed) {
		t.Errorf("a JPEG cut before its image data: %v", err)
	}
}

// ExifTool reads the edit back from the JPEG, with no warnings.
func TestPlanExifEditJPEGWithExifTool(t *testing.T) {
	tool, err := exec.LookPath("exiftool")
	if err != nil {
		t.Skip("exiftool not installed")
	}
	dir := t.TempDir()
	for name, file := range jpegFiles(t) {
		p, err := PlanExifEdit(bytes.NewReader(file), int64(len(file)), editAll)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, strings.ReplaceAll(name, " ", "-")+".jpg")
		if err := os.WriteFile(path, written(t, p), 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := exec.Command(tool, "-validate", "-warning", "-error", "-a", "-s", "-n",
			"-EXIF:Orientation", "-EXIF:DateTimeOriginal", "-EXIF:CreateDate", "-EXIF:OffsetTimeDigitized",
			"-Composite:GPSLatitude", "-Composite:GPSLongitude", path).CombinedOutput()
		if err != nil {
			t.Fatalf("exiftool: %v\n%s", err, got)
		}
		s := string(got)
		for _, want := range [][2]string{
			{"Orientation", "8"}, {"DateTimeOriginal", "2024:02:29 08:07:06"}, {"CreateDate", "2024:02:29 08:07:06"},
			{"OffsetTimeDigitized", "+05:30"}, {"GPSLatitude", "-33.8568"}, {"GPSLongitude", "151.2153"},
		} {
			if line := fmt.Sprintf("%-32s: %s", want[0], want[1]); !strings.Contains(s, line) {
				t.Errorf("%s: ExifTool lacks %q:\n%s", name, line, s)
			}
		}
		for _, line := range strings.Split(s, "\n") {
			if strings.Contains(line, "[minor]") || strings.Contains(line, "Missing required JPEG") {
				continue
			}
			if strings.HasPrefix(line, "Warning") || strings.HasPrefix(line, "Error") {
				t.Errorf("%s: ExifTool: %s", name, line)
			}
		}
	}
}

func FuzzJPEGExif(f *testing.F) {
	for _, file := range jpegFiles(f) {
		f.Add(file)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		withinDeadline(t, func() {
			r := bytes.NewReader(data)
			if p, err := PlanExifEdit(r, int64(len(data)), editAll); err == nil {
				checkPlan(t, data, p)
			}
			if p, err := PlanExif(r, int64(len(data)), photoTIFF(binary.LittleEndian)); err == nil {
				checkPlan(t, data, p)
			}
		})
	})
}
