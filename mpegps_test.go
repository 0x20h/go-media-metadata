package mediameta

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The MPEG program stream corpus (testdata/mpegps, see its make.sh): the
// size is ExifTool's, the duration ffprobe's (ExifTool gives none), and
// writes are refused.
func TestMPEGPSAgreesWithTools(t *testing.T) {
	paths, _ := filepath.Glob("testdata/mpegps/*.mpg")
	if len(paths) < 2 {
		t.Fatalf("%d files", len(paths))
	}
	for _, p := range paths {
		file := readFile(t, p)
		r, n := bytes.NewReader(file), int64(len(file))
		v, err := ReadVideo(r, n)
		if err != nil || v.Width <= 0 || v.Duration <= 0 || v.Orientation != 1 {
			t.Fatalf("%s: %+v, %v", p, v, err)
		}
		if _, err := PlanOrientation(r, n, 6); !errors.Is(err, ErrUnsupported) {
			t.Errorf("%s: PlanOrientation %v, want ErrUnsupported", p, err)
		}
		if probe, err := exec.LookPath("ffprobe"); err == nil {
			out, err := exec.Command(probe, "-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", p).Output()
			d, perr := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
			if err != nil || perr != nil || math.Abs(v.Duration.Seconds()-d) > 0.05 {
				t.Errorf("%s: duration %v, ffprobe %s", p, v.Duration, out)
			}
		}
	}
	tool, err := exec.LookPath("exiftool")
	if err != nil {
		t.Skip("exiftool not installed")
	}
	out, err := exec.Command(tool, append([]string{"-j", "-n", "-q", "-ImageWidth", "-ImageHeight"}, paths...)...).Output()
	if err != nil {
		t.Fatal(err)
	}
	var got []struct {
		SourceFile              string
		ImageWidth, ImageHeight int
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	for _, e := range got {
		file := readFile(t, e.SourceFile)
		v, _ := ReadVideo(bytes.NewReader(file), int64(len(file)))
		if v.Width != e.ImageWidth || v.Height != e.ImageHeight {
			t.Errorf("%s: %d×%d, ExifTool %d×%d", e.SourceFile, v.Width, v.Height, e.ImageWidth, e.ImageHeight)
		}
	}
}
