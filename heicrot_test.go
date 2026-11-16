package mediameta

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// A HEIC whose Exif Orientation (1) disagrees with its irot (270°
// anticlockwise, orientation 6): readers show it by irot, as HEIF has them
// (ISO/IEC 23008-12 6.5.10), and so does ReadOrientation. The Exif tag is
// read as written. On macOS, Apple's ImageIO is asked too: it reports
// orientation 6 and gives the image turned (480×640).
func TestHEICIrotOverExif(t *testing.T) {
	const path = "testdata/imageio/c-exif1-irot270.heic"
	file := readFile(t, path)
	r, n := bytes.NewReader(file), int64(len(file))
	o, err := ReadOrientation(r, n)
	if err != nil || o != 6 {
		t.Errorf("ReadOrientation %d, %v; want 6, from irot", o, err)
	}
	x, err := ReadExif(r, n)
	if err != nil || x.Orientation != 1 {
		t.Errorf("Exif Orientation %d, %v; want 1, as written", x.Orientation, err)
	}
	s, err := ReadImageSize(r, n)
	if w, h := s.Shown(); err != nil || w != 480 || h != 640 {
		t.Errorf("shown %d×%d (%+v, %v), want 480×640", w, h, s, err)
	}

	if runtime.GOOS != "darwin" {
		return
	}
	swift, err := exec.LookPath("swift")
	if err != nil {
		t.Log("swift not installed: ImageIO not asked")
		return
	}
	probe := filepath.Join(t.TempDir(), "probe.swift")
	src, err := os.ReadFile("testdata/imageio/probe.swift.txt")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(probe, src, 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(swift, probe, path).CombinedOutput()
	if err != nil {
		t.Skipf("swift probe: %v\n%s", err, out)
	}
	t.Logf("ImageIO: %s", strings.TrimSpace(string(out)))
	if got := string(out); !strings.Contains(got, "orientation property: 6") || !strings.Contains(got, "shown (transformed): 480x640") {
		t.Errorf("ImageIO: %s; want orientation 6, shown 480x640", got)
	}
}
