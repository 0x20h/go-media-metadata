package mediameta

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// ReadExif's Camera matches ExifTool's EXIF values on every corpus file
// and, with MEDIAMETA_RAW_DIR set, on real RAW files: the text exactly,
// the numbers within 1e-9 relative.
func TestCameraAgreesWithExifTool(t *testing.T) {
	tool, err := exec.LookPath("exiftool")
	if err != nil {
		t.Skip("exiftool not installed")
	}
	pats := []string{"testdata/exiftool/*.jpg", imageIOCorpus + "/*.jpg", imageIOCorpus + "/*.heic", "testdata/iphone/*.heic", "testdata/webp/*.webp"}
	if dir := os.Getenv("MEDIAMETA_RAW_DIR"); dir != "" {
		for _, ext := range []string{"dng", "cr2", "cr3", "nef", "arw", "orf", "rw2", "raf"} {
			pats = append(pats, filepath.Join(dir, "*."+ext))
		}
	}
	var paths []string
	for _, pat := range pats {
		p, _ := filepath.Glob(pat)
		paths = append(paths, p...)
	}
	args := append([]string{"-j", "-n", "-q", "-G0", "-EXIF:Make", "-EXIF:Model", "-EXIF:LensMake", "-EXIF:LensModel", "-EXIF:ISO",
		"-EXIF:SensitivityType", "-EXIF:RecommendedExposureIndex", "-EXIF:ISOSpeed", "-EXIF:FNumber", "-EXIF:ExposureTime",
		"-EXIF:FocalLength", "-EXIF:FocalLengthIn35mmFormat"}, paths...)
	out, err := exec.Command(tool, args...).Output()
	if err != nil {
		t.Fatalf("exiftool: %v", err)
	}
	var got []map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	withCamera := 0
	for _, e := range got {
		path := e["SourceFile"].(string)
		file := readFile(t, path)
		x, err := ReadExif(bytes.NewReader(file), int64(len(file)))
		if err != nil {
			t.Errorf("%s: %v", path, err)
			continue
		}
		c := x.Camera
		if c.Model != "" {
			withCamera++
		}
		text := func(k string) string { s, _ := e["EXIF:"+k].(string); return s }
		num := func(k string) float64 {
			v, _ := e["EXIF:"+k].(float64)
			if v < 0 || math.IsInf(v, 0) {
				return 0
			}
			return v
		}
		iso := num("ISO")
		if iso == 65535 {
			switch num("SensitivityType") {
			case 2, 4, 5, 6:
				if v := num("RecommendedExposureIndex"); v > 0 {
					iso = v
				}
			case 3, 7:
				if v := num("ISOSpeed"); v > 0 {
					iso = v
				}
			}
		}
		near := func(a, b float64) bool { return math.Abs(a-b) <= 1e-9*math.Max(1, math.Abs(b)) }
		if c.Make != text("Make") || c.Model != text("Model") || c.LensMake != text("LensMake") || c.LensModel != text("LensModel") ||
			float64(c.ISO) != iso || !near(c.FNumber, num("FNumber")) || !near(c.ExposureTime, num("ExposureTime")) ||
			!near(c.FocalLength, num("FocalLength")) || float64(c.FocalLengthIn35mm) != num("FocalLengthIn35mmFormat") {
			t.Errorf("%s: %+v, ExifTool %v", path, c, e)
		}
	}
	if withCamera < 3 {
		t.Errorf("only %d corpus files with a camera", withCamera)
	}
}

// Camera's values from both byte orders, and ISO past 65535.
func TestCameraCorpus(t *testing.T) {
	for name, want := range map[string]Camera{
		"camera-ii.jpg": {Make: "Canon", Model: "Canon EOS R6", LensMake: "Canon", LensModel: "RF70-200mm F2.8 L IS USM",
			ISO: 400, FNumber: 2.8, ExposureTime: 0.004, FocalLength: 135, FocalLengthIn35mm: 135},
		"camera-mm.jpg": {Make: "Apple", Model: "iPhone 12 Pro", LensModel: "iPhone 12 Pro back triple camera 4.2mm f/1.6",
			ISO: 102400, FNumber: 1.6, ExposureTime: 0.0588235294, FocalLength: 4.2, FocalLengthIn35mm: 26},
	} {
		file := readFile(t, filepath.Join("testdata/exiftool", name))
		x, err := ReadExif(bytes.NewReader(file), int64(len(file)))
		if err != nil {
			t.Fatal(err)
		}
		c := x.Camera
		if c.Make != want.Make || c.Model != want.Model || c.LensMake != want.LensMake || c.LensModel != want.LensModel ||
			c.ISO != want.ISO || math.Abs(c.FNumber-want.FNumber) > 1e-9 || math.Abs(c.ExposureTime-want.ExposureTime) > 1e-9 ||
			math.Abs(c.FocalLength-want.FocalLength) > 1e-9 || c.FocalLengthIn35mm != want.FocalLengthIn35mm {
			t.Errorf("%s: %+v, want %+v", name, c, want)
		}
	}
}
