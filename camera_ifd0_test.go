package mediameta

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Exposure tags in IFD0 (as some Samsung cameras write them), without an
// Exif IFD, read as ExifTool reads them; the Exif IFD's win where both are.
func TestCameraFromIFD0(t *testing.T) {
	b := &tiffBuilder{order: binary.LittleEndian}
	b.ifd0 = []tiffEntry{
		{tagMake, typeASCII, 8, []byte("SAMSUNG\x00")},
		{tagExposureTime, typeRational, 1, b.rationals([2]uint32{1, 120})},
		{tagFNumber, typeRational, 1, b.rationals([2]uint32{22, 10})},
		{tagPhotographicSensitivity, typeShort, 1, b.short(200)},
		{tagFocalLength, typeRational, 1, b.rationals([2]uint32{43, 10})},
	}
	file := withSegments(plainJPEG(t), exifSegment(b.build()))
	m, err := ReadAll(bytes.NewReader(file), int64(len(file)))
	c := m.Camera
	if err != nil || c.ISO != 200 || math.Abs(c.ExposureTime-1.0/120) > 1e-9 || c.FNumber != 2.2 || c.FocalLength != 4.3 {
		t.Fatalf("%+v, %v", c, err)
	}
	// The Exif IFD's value comes first.
	b.exif = []tiffEntry{{tagPhotographicSensitivity, typeShort, 1, b.short(800)}}
	both := withSegments(plainJPEG(t), exifSegment(b.build()))
	if m, _ := ReadAll(bytes.NewReader(both), int64(len(both))); m.Camera.ISO != 800 || m.Camera.FNumber != 2.2 {
		t.Errorf("with an Exif IFD: %+v", m.Camera)
	}
	tool, err := exec.LookPath("exiftool")
	if err != nil {
		t.Skip("exiftool not installed")
	}
	p := filepath.Join(t.TempDir(), "ifd0.jpg")
	if err := os.WriteFile(p, file, 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(tool, "-j", "-n", "-q", "-ExposureTime", "-FNumber", "-ISO", "-FocalLength", p).Output()
	var got []map[string]any
	if err != nil || json.Unmarshal(out, &got) != nil || len(got) != 1 {
		t.Fatalf("%v %s", err, out)
	}
	num := func(k string) float64 { f, _ := got[0][k].(float64); return f }
	e := got[0]
	if math.Abs(num("ExposureTime")-c.ExposureTime) > 1e-9 || num("FNumber") != c.FNumber || int(num("ISO")) != c.ISO || num("FocalLength") != c.FocalLength {
		t.Errorf("ExifTool %v, Camera %+v", e, c)
	}
}
