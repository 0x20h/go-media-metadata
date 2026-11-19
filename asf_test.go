package mediameta

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// The ASF test files read as made: the creation date (or, without one,
// WM/EncodingTime), the duration less the preroll, the video size, the
// title and description.
func TestReadASF(t *testing.T) {
	at := time.Date(2025, 5, 4, 11, 14, 15, 0, time.UTC)
	for _, c := range []struct {
		file          string
		created       time.Time
		width, height int
		title, desc   string
	}{
		{"video.wmv", at, 70, 50, "Harbour at dusk", "Morning, by the water"},
		{"audio.wma", at, 0, 0, "Tone", ""},
		{"encodingtime.wmv", time.Date(2006, 7, 8, 9, 10, 11, 0, time.UTC), 70, 50, "Encoded", ""},
	} {
		b := readFile(t, filepath.Join("testdata", "asf", c.file))
		v, err := ReadVideo(bytes.NewReader(b), int64(len(b)))
		if err != nil {
			t.Fatalf("%s: %v", c.file, err)
		}
		if !v.Created.Equal(c.created) || !v.OffsetKnown || v.Duration != time.Second || v.Width != c.width || v.Height != c.height ||
			v.Title != c.title || v.Description != c.desc {
			t.Errorf("%s: %+v", c.file, v)
		}
		m, err := ReadAll(bytes.NewReader(b), int64(len(b)))
		if err != nil || m.Format != "ASF" || m.Taken.Source != SourceMovie || !m.Taken.Time.Equal(c.created) || m.Title != c.title {
			t.Errorf("%s: ReadAll %s %+v %q, %v", c.file, m.Format, m.Taken, m.Title, err)
		}
		// Read only.
		if _, err := PlanVideoMetadata(bytes.NewReader(b), int64(len(b)), VideoEdit{Title: ptr("x")}); !errors.Is(err, ErrUnsupported) {
			t.Errorf("%s: PlanVideoMetadata %v", c.file, err)
		}
		if _, err := PlanRotation(bytes.NewReader(b), int64(len(b)), 90); !errors.Is(err, ErrUnsupported) {
			t.Errorf("%s: PlanRotation %v", c.file, err)
		}
	}
}

// ExifTool reads the same values, but for the duration: its Duration is
// the play duration with the preroll in it (4.1 s here); ffprobe's, the
// one read here, is without (1 s).
func TestASFAgreesWithExifTool(t *testing.T) {
	tool, err := exec.LookPath("exiftool")
	if err != nil {
		t.Skip("exiftool not installed")
	}
	out, err := exec.Command(tool, "-j", "-n", "-q", "-CreationDate", "-EncodingTime", "-Title", "-Description", "-ImageWidth", "-ImageHeight",
		"-ext", "wmv", "-ext", "wma", filepath.Join("testdata", "asf")).Output()
	if err != nil {
		t.Fatalf("exiftool: %v", err)
	}
	var got []struct {
		SourceFile              string
		CreationDate            string
		EncodingTime            json.Number
		Title, Description      string
		ImageWidth, ImageHeight int
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("exiftool read %d files, want 3", len(got))
	}
	for _, e := range got {
		b, err := os.ReadFile(e.SourceFile)
		if err != nil {
			t.Fatal(err)
		}
		v, err := ReadVideo(bytes.NewReader(b), int64(len(b)))
		if err != nil {
			t.Fatalf("%s: %v", e.SourceFile, err)
		}
		want := e.CreationDate // "2025:05:04 11:14:15Z", or 1601 for none
		if n, err := strconv.ParseUint(string(e.EncodingTime), 10, 64); err == nil && want == "1601:01:01 00:00:00Z" {
			want = fromFILETIME(n).Format("2006:01:02 15:04:05Z") // ExifTool gives the FILETIME as a number
		}
		if got := v.Created.Format("2006:01:02 15:04:05Z"); got != want || v.Title != e.Title || v.Description != e.Description ||
			v.Width != e.ImageWidth || v.Height != e.ImageHeight {
			t.Errorf("%s: %s %q %q %d×%d; ExifTool %s %q %q %d×%d", e.SourceFile, got, v.Title, v.Description, v.Width, v.Height,
				want, e.Title, e.Description, e.ImageWidth, e.ImageHeight)
		}
	}
}

// ffprobe's duration is the one read.
func TestASFDurationAgreesWithFFprobe(t *testing.T) {
	tool, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe not installed")
	}
	paths, _ := filepath.Glob("testdata/asf/*.wm?")
	for _, p := range paths {
		out, err := exec.Command(tool, "-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", p).Output()
		if err != nil {
			t.Fatal(err)
		}
		want, err := time.ParseDuration(string(bytes.TrimSpace(out)) + "s")
		if err != nil {
			t.Fatal(err)
		}
		b := readFile(t, p)
		if v, err := ReadVideo(bytes.NewReader(b), int64(len(b))); err != nil || v.Duration != want {
			t.Errorf("%s: %v, ffprobe %v (%v)", p, v.Duration, want, err)
		}
	}
}
