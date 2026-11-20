package mediameta

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// The FLV test files read as made: the duration, size and title of
// onMetaData, and the date from creationdate (no offset), else ffmpeg's
// creation_time.
func TestReadFLV(t *testing.T) {
	for _, c := range []struct {
		file    string
		created time.Time
		offset  bool
		title   string
	}{
		{"ffmpeg.flv", time.Date(2025, 5, 4, 11, 14, 15, 0, time.UTC), true, "Harbour at dusk"},
		{"dated.flv", time.Date(2005, 6, 7, 8, 9, 10, 0, time.UTC), false, ""},
	} {
		b := readFile(t, filepath.Join("testdata", "flv", c.file))
		v, err := ReadVideo(bytes.NewReader(b), int64(len(b)))
		if err != nil {
			t.Fatalf("%s: %v", c.file, err)
		}
		if !v.Created.Equal(c.created) || v.OffsetKnown != c.offset || v.Duration != time.Second || v.Width != 70 || v.Height != 50 || v.Title != c.title {
			t.Errorf("%s: %+v", c.file, v)
		}
		m, err := ReadAll(bytes.NewReader(b), int64(len(b)))
		if err != nil || m.Format != "FLV" || !m.Taken.Time.Equal(c.created) || m.Size.Width != 70 {
			t.Errorf("%s: ReadAll %s %+v %+v, %v", c.file, m.Format, m.Taken, m.Size, err)
		}
		if _, err := PlanVideoMetadata(bytes.NewReader(b), int64(len(b)), VideoEdit{Title: ptr("x")}); !errors.Is(err, ErrUnsupported) {
			t.Errorf("%s: PlanVideoMetadata %v", c.file, err)
		}
		if _, err := PlanRotation(bytes.NewReader(b), int64(len(b)), 90); !errors.Is(err, ErrUnsupported) {
			t.Errorf("%s: PlanRotation %v", c.file, err)
		}
	}
	// Only metadatadate: the date it gives, in UTC.
	b := readFile(t, "testdata/flv/dated.flv")
	b = bytes.Replace(b, []byte("creationdate"), []byte("creationdatx"), 1)
	if v, err := ReadVideo(bytes.NewReader(b), int64(len(b))); err != nil || !v.Created.Equal(time.Date(2005, 6, 8, 1, 2, 3, 0, time.UTC)) || !v.OffsetKnown {
		t.Errorf("metadatadate: %+v, %v", v, err)
	}
}

// ExifTool reads the same duration, size, title and dates.
func TestFLVAgreesWithExifTool(t *testing.T) {
	tool, err := exec.LookPath("exiftool")
	if err != nil {
		t.Skip("exiftool not installed")
	}
	out, err := exec.Command(tool, "-j", "-n", "-q", "-Duration", "-ImageWidth", "-ImageHeight", "-Title", "-CreateDate", "-Creation_time",
		"-ext", "flv", filepath.Join("testdata", "flv")).Output()
	if err != nil {
		t.Fatalf("exiftool: %v", err)
	}
	var got []struct {
		SourceFile              string
		Duration                float64
		ImageWidth, ImageHeight int
		Title, CreateDate       string
		CreationTime            string `json:"Creation_time"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("exiftool read %d files, want 2", len(got))
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
		date := e.CreateDate
		if date == "" {
			date = e.CreationTime
		}
		want, _ := parseFLVDate(date)
		if v.Duration.Seconds() != e.Duration || v.Width != e.ImageWidth || v.Height != e.ImageHeight || v.Title != e.Title || !v.Created.Equal(want) {
			t.Errorf("%s: %v %d×%d %q %v; ExifTool %v %d×%d %q %q", e.SourceFile, v.Duration, v.Width, v.Height, v.Title, v.Created,
				e.Duration, e.ImageWidth, e.ImageHeight, e.Title, date)
		}
	}
}
