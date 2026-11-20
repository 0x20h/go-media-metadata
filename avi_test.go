package mediameta

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// The AVI corpus (testdata/avi, see its make.sh): ReadVideo reads what
// ExifTool reads, and writes are refused.
func TestAVIAgreesWithExifTool(t *testing.T) {
	paths, _ := filepath.Glob("testdata/avi/*.avi")
	if len(paths) < 3 {
		t.Fatalf("%d AVI files", len(paths))
	}
	for _, p := range paths {
		file := readFile(t, p)
		r, n := bytes.NewReader(file), int64(len(file))
		if _, err := PlanOrientation(r, n, 6); !errors.Is(err, ErrUnsupported) {
			t.Errorf("%s: PlanOrientation %v, want ErrUnsupported", p, err)
		}
		if o, err := ReadOrientation(r, n); err != nil || o != 1 {
			t.Errorf("%s: orientation %v, %v", p, o, err)
		}
	}
	tool, err := exec.LookPath("exiftool")
	if err != nil {
		t.Skip("exiftool not installed")
	}
	out, err := exec.Command(tool, append([]string{"-j", "-n", "-q", "-RIFF:DateTimeOriginal", "-RIFF:DateCreated",
		"-Composite:Duration", "-RIFF:ImageWidth", "-RIFF:ImageHeight", "-RIFF:Title", "-RIFF:Comment",
		"-Nikon:Make", "-Nikon:Model", "-Nikon:DateTimeOriginal",
		// A camera's Exif block (camera_video_exif.go).
		"-IFD0:Make", "-IFD0:Model", "-ExifIFD:DateTimeOriginal", "-ExifIFD:LensModel",
		"-Composite:GPSLatitude", "-Composite:GPSLongitude"}, paths...)...).Output()
	if err != nil {
		t.Fatal(err)
	}
	var got []struct {
		SourceFile, DateTimeOriginal, DateCreated, Title, Comment string
		Make, Model, LensModel                                    string
		Duration                                                  float64
		GPSLatitude, GPSLongitude                                 *float64
		ImageWidth, ImageHeight                                   int
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	dated := 0
	for _, e := range got {
		file := readFile(t, e.SourceFile)
		v, err := ReadVideo(bytes.NewReader(file), int64(len(file)))
		if err != nil {
			t.Fatalf("%s: %v", e.SourceFile, err)
		}
		date := e.DateTimeOriginal
		if date == "" {
			date = e.DateCreated
		}
		if date == "" {
			if !v.Created.IsZero() {
				t.Errorf("%s: created %v, ExifTool none", e.SourceFile, v.Created)
			}
		} else {
			layout := "2006:01:02 15:04:05"
			if len(date) == len("2006:01:02") {
				layout = "2006:01:02"
			}
			want, err := time.Parse(layout, date)
			if err != nil || !v.Created.Equal(want) || v.OffsetKnown {
				t.Errorf("%s: created %v, ExifTool %q", e.SourceFile, v.Created, date)
			}
			dated++
		}
		if math.Abs(v.Duration.Seconds()-e.Duration) > 1e-3 || v.Width != e.ImageWidth || v.Height != e.ImageHeight ||
			v.Title != e.Title || v.Description != e.Comment || v.Make != e.Make || v.Model != e.Model || v.LensModel != e.LensModel {
			t.Errorf("%s: %+v, ExifTool %+v", e.SourceFile, v, e)
		}
		if (v.GPS != nil) != (e.GPSLatitude != nil) {
			t.Errorf("%s: GPS %+v, ExifTool %v", e.SourceFile, v.GPS, e.GPSLatitude)
		} else if g := v.GPS; g != nil && (math.Abs(g.Latitude-*e.GPSLatitude) > 1e-4 || math.Abs(g.Longitude-*e.GPSLongitude) > 1e-4) {
			t.Errorf("%s: GPS %+v, ExifTool %v %v", e.SourceFile, *g, *e.GPSLatitude, *e.GPSLongitude)
		}
	}
	if dated < 3 {
		t.Errorf("%d dated AVI files", dated)
	}
}
