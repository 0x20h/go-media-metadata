package mediameta

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/0x20h/go-media-metadata/internal/bmff"
	"github.com/0x20h/go-media-metadata/internal/bmff/qtbuild"
)

// brokenMovies are a fast-start movie in the layouts ExifTool reads but a
// strict box walk refuses.
func brokenMovies(t testing.TB) (whole []byte, broken map[string][]byte) {
	o := qtbuild.Default()
	o.MoovFirst = true
	whole, _, _ = qtbuild.Build(o)
	cat := func(b ...[]byte) []byte { return bytes.Join(b, nil) }
	return whole, map[string][]byte{
		"mdat cut short":          whole[:len(whole)-1000],
		"mdat cut in its header":  whole[:len(whole)-o.MediaSize-4],
		"partial box header":      cat(whole, []byte{0, 0, 0, 0x40, 'm', 'o', 'o'}),
		"junk after the last box": cat(whole, []byte("\xffjunk!")),
		"junk box header":         cat(whole, []byte("\x00\x00\x00\x03\xff\xfe\xfd\xfcmore junk")),
		"box past the end":        cat(whole, []byte("\x00\x01\x00\x00free"), make([]byte, 100)),
	}
}

func TestReadVideoTolerant(t *testing.T) {
	whole, broken := brokenMovies(t)
	want, err := ReadVideo(bytes.NewReader(whole), int64(len(whole)))
	if err != nil {
		t.Fatal(err)
	}
	wantAll, err := ReadAll(bytes.NewReader(whole), int64(len(whole)))
	if err != nil {
		t.Fatal(err)
	}
	for name, file := range broken {
		r, n := bytes.NewReader(file), int64(len(file))
		if _, err := bmff.ReadBoxes(r, 0, n); err == nil {
			t.Errorf("%s: reads strictly; not a test of leniency", name)
		}
		got, err := ReadVideo(r, n)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("%s: %+v, %v; want %+v", name, got, err, want)
		}
		all, err := ReadAll(r, n)
		if err != nil || all.Video == nil || !reflect.DeepEqual(*all.Video, want) || all.Format != wantAll.Format ||
			all.Size != wantAll.Size || all.Taken != wantAll.Taken {
			t.Errorf("%s: ReadAll %+v, %v", name, all, err)
		}
		if _, err := PlanVideoMetadata(r, n, VideoEdit{Title: ptr("x")}); !errors.Is(err, ErrMalformed) {
			t.Errorf("%s: PlanVideoMetadata %v, want ErrMalformed", name, err)
		}
		if _, err := PlanRotation(r, n, 90); err == nil {
			t.Errorf("%s: PlanRotation succeeded", name)
		}
	}
	// A movie whose moov is cut isn't read.
	o := qtbuild.Default()
	file, _, _ := qtbuild.Build(o) // moov last
	if _, err := ReadVideo(bytes.NewReader(file[:len(file)-50]), int64(len(file)-50)); !errors.Is(err, ErrMalformed) {
		t.Errorf("moov cut short: %v, want ErrMalformed", err)
	}

	tool, err := exec.LookPath("exiftool")
	if err != nil {
		t.Skip("exiftool not installed")
	}
	dir := t.TempDir()
	var paths []string
	for name, file := range broken {
		p := filepath.Join(dir, filepath.Base(name)+".mov")
		if err := os.WriteFile(p, file, 0o644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	out, err := exec.Command(tool, append([]string{"-j", "-n", "-q", "-QuickTime:Duration", "-Keys:Make", "-Keys:Model"}, paths...)...).Output()
	if err != nil {
		t.Fatal(err)
	}
	var tags []struct {
		SourceFile, Make, Model string
		Duration                float64
	}
	if err := json.Unmarshal(out, &tags); err != nil {
		t.Fatal(err)
	}
	for _, e := range tags {
		if e.Make != want.Make || e.Model != want.Model || e.Duration != want.Duration.Seconds() {
			t.Errorf("%s: ExifTool %+v, ReadVideo %+v", e.SourceFile, e, want)
		}
	}
}

func TestReadBoxesLenient(t *testing.T) {
	whole, broken := brokenMovies(t)
	strict, err := bmff.ReadBoxes(bytes.NewReader(whole), 0, int64(len(whole)))
	if err != nil {
		t.Fatal(err)
	}
	if got, cut := bmff.ReadBoxesLenient(bytes.NewReader(whole), 0, int64(len(whole))); cut || !reflect.DeepEqual(got, strict) {
		t.Errorf("whole: %v, %v; want %v", got, cut, strict)
	}
	for name, file := range broken {
		got, cut := bmff.ReadBoxesLenient(bytes.NewReader(file), 0, int64(len(file)))
		if !cut || len(got) < 2 || got[len(got)-1].End() > int64(len(file)) {
			t.Errorf("%s: %v, %v", name, got, cut)
		}
	}
}
