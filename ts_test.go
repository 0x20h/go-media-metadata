package mediameta

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The M2TS corpus (testdata/m2ts, see its make.sh): ExifTool's values for
// the MDPM and the duration, ffprobe's for the picture size (ExifTool
// gives none for these files).
func TestReadTSVideo(t *testing.T) {
	for _, c := range []struct {
		file    string
		created string
		gps     bool
		w, h    int
	}{
		{"mdpm.m2ts", "2025-05-04T13:14:15+02:00", true, 70, 50},
		{"mdpm-west.m2ts", "2019-12-31T23:59:59-03:30", true, 70, 50},
		{"plain.ts", "", false, 70, 50},
	} {
		file := readFile(t, filepath.Join("testdata/m2ts", c.file))
		v, err := ReadVideo(bytes.NewReader(file), int64(len(file)))
		if err != nil {
			t.Fatalf("%s: %v", c.file, err)
		}
		if c.created != "" {
			want, _ := time.Parse(time.RFC3339, c.created)
			if !v.Created.Equal(want) || !v.OffsetKnown || v.Created.Format("-07:00") != want.Format("-07:00") {
				t.Errorf("%s: created %v (offset %v), want %v", c.file, v.Created, v.OffsetKnown, want)
			}
		} else if !v.Created.IsZero() {
			t.Errorf("%s: created %v", c.file, v.Created)
		}
		if (v.GPS != nil) != c.gps || c.gps && (v.GPS.Latitude != 51.5 || v.GPS.Longitude != -0.125 || v.GPS.Altitude != 35.5) {
			t.Errorf("%s: GPS %+v", c.file, v.GPS)
		}
		if c.gps && (v.Make != "Sony" || v.Model != "HDR-CX405") {
			t.Errorf("%s: make %q, model %q", c.file, v.Make, v.Model)
		}
		if v.Width != c.w || v.Height != c.h || v.Orientation != 1 || v.Duration != 1920*time.Millisecond {
			t.Errorf("%s: %d×%d, orientation %d, duration %v", c.file, v.Width, v.Height, v.Orientation, v.Duration)
		}
		if s, err := ReadImageSize(bytes.NewReader(file), int64(len(file))); err != nil || s.Width != c.w || s.Height != c.h {
			t.Errorf("%s: ReadImageSize %+v, %v", c.file, s, err)
		}
		if o, err := ReadOrientation(bytes.NewReader(file), int64(len(file))); err != nil || o != 1 {
			t.Errorf("%s: ReadOrientation %v, %v", c.file, o, err)
		}
		for name, err := range map[string]error{
			"PlanOrientation":   second(PlanOrientation(bytes.NewReader(file), int64(len(file)), 6)),
			"PlanRotation":      second(PlanRotation(bytes.NewReader(file), int64(len(file)), 90)),
			"PlanVideoMetadata": second(PlanVideoMetadata(bytes.NewReader(file), int64(len(file)), VideoEdit{RemoveGPS: true})),
			"HashImageData":     second(HashImageData(bytes.NewReader(file), int64(len(file)), io.Discard)),
		} {
			if !errors.Is(err, ErrUnsupported) {
				t.Errorf("%s: %s: %v, want ErrUnsupported", c.file, name, err)
			}
		}
	}
}

func second[T any](_ T, err error) error { return err }

// ReadVideo reads a transport stream's MDPM and duration as ExifTool does,
// and its size as ffprobe does, where they are installed.
func TestTSAgreesWithExifTool(t *testing.T) {
	tool, err := exec.LookPath("exiftool")
	if err != nil {
		t.Skip("exiftool not installed")
	}
	paths, _ := filepath.Glob("testdata/m2ts/*.*ts")
	out, err := exec.Command(tool, append([]string{"-j", "-n", "-q", "-DateTimeOriginal", "-GPSLatitude", "-GPSLongitude",
		"-GPSAltitude", "-GPSAltitudeRef", "-Model", "-Duration"}, paths...)...).Output()
	if err != nil {
		t.Fatal(err)
	}
	var got []struct {
		SourceFile, DateTimeOriginal, Model    string
		GPSLatitude, GPSLongitude, GPSAltitude *float64
		GPSAltitudeRef                         int
		Duration                               float64
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(paths) || len(got) < 3 {
		t.Fatalf("ExifTool read %d of %d files", len(got), len(paths))
	}
	for _, e := range got {
		file := readFile(t, e.SourceFile)
		v, err := ReadVideo(bytes.NewReader(file), int64(len(file)))
		if err != nil {
			t.Fatalf("%s: %v", e.SourceFile, err)
		}
		// ExifTool: "2019:12:31 23:59:59-03:30 DST"; the flag is beside
		// the offset.
		date := strings.TrimSuffix(e.DateTimeOriginal, " DST")
		if mine := v.Created.Format("2006:01:02 15:04:05-07:00"); date != "" && mine != date || date == "" && !v.Created.IsZero() {
			t.Errorf("%s: created %s, ExifTool %q", e.SourceFile, mine, e.DateTimeOriginal)
		}
		if (e.GPSLatitude != nil) != (v.GPS != nil) {
			t.Errorf("%s: GPS %+v, ExifTool %v", e.SourceFile, v.GPS, e.GPSLatitude)
		} else if g := v.GPS; g != nil {
			alt := *e.GPSAltitude
			if e.GPSAltitudeRef == 1 {
				alt = -alt
			}
			if math.Abs(g.Latitude-*e.GPSLatitude) > 1e-6 || math.Abs(g.Longitude-*e.GPSLongitude) > 1e-6 || math.Abs(g.Altitude-alt) > 1e-6 {
				t.Errorf("%s: GPS %+v, ExifTool %v %v %v", e.SourceFile, *g, *e.GPSLatitude, *e.GPSLongitude, alt)
			}
		}
		if v.Model != e.Model || math.Abs(v.Duration.Seconds()-e.Duration) > 1e-6 {
			t.Errorf("%s: model %q, duration %v; ExifTool %q, %v", e.SourceFile, v.Model, v.Duration, e.Model, e.Duration)
		}
		if probe, err := exec.LookPath("ffprobe"); err == nil {
			out, err := exec.Command(probe, "-v", "error", "-select_streams", "v:0", "-show_entries", "stream=width,height", "-of", "csv=p=0", e.SourceFile).Output()
			if first, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n"); err == nil && strings.TrimSpace(first) != itoa(v.Width)+","+itoa(v.Height) {
				t.Errorf("%s: %d×%d, ffprobe %s", e.SourceFile, v.Width, v.Height, out)
			}
		}
	}
}

// A stream's duration past 341 s, where ticks × time.Second would overflow:
// the regression input FuzzReadVideo found, its last PCR 400 s after its
// first.
func TestTSLongDuration(t *testing.T) {
	file := readFile(t, "testdata/fuzz/FuzzReadVideo/ts-pcr-span-400s")
	// The corpus file's bytes: after the "go test fuzz v1" line, a []byte
	// literal.
	lines := strings.SplitN(string(file), "\n", 2)
	lit := strings.TrimSuffix(strings.TrimSpace(lines[1]), ")")
	data, err := strconv.Unquote(strings.TrimPrefix(lit, "[]byte("))
	if err != nil {
		t.Fatal(err)
	}
	v, err := ReadVideo(strings.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if v.Duration < 399*time.Second || v.Duration > 401*time.Second {
		t.Errorf("duration %v, want about 400 s", v.Duration)
	}
}
