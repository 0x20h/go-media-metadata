package mediameta

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// canonSettingsExif is canonExif with the maker note's CameraSettings
// (tag 0x0001): 30 16-bit values, LensType at 22, the maximum and minimum
// focal length at 23 and 24, FocalUnits at 25, MaxAperture at 26 (Canon
// EV). The other values are made up.
func canonSettingsExif(lensType, maxFocal, minFocal, units uint16, maxAperture int16) []byte {
	b := canonExif("")
	le := binary.LittleEndian
	// canonExif's maker note: 2 entries, 0x0001 first, a SHORT of 1.
	mnAt := bytes.Index(b, []byte{2, 0, 1, 0, 3, 0, 1, 0, 0, 0})
	if mnAt < 0 {
		panic("no maker note")
	}
	settings := make([]byte, 0, 60)
	vals := make([]uint16, 30)
	vals[0] = 60
	vals[22], vals[23], vals[24], vals[25], vals[26] = lensType, maxFocal, minFocal, units, uint16(maxAperture)
	for _, v := range vals {
		settings = le.AppendUint16(settings, v)
	}
	// The settings appended to the data; the entry points to them.
	at := len(b)
	e := b[mnAt+2:]
	le.PutUint32(e[4:], 30)
	le.PutUint32(e[8:], uint32(at))
	// The maker note's size in its Exif entry grows to cover them (as
	// readers take a maker note to the end of its entry's data).
	mn := bytes.Index(b, []byte{0x7c, 0x92, 7, 0})
	le.PutUint32(b[mn+4:], le.Uint32(b[mn+4:])+uint32(len(settings)))
	return append(b, settings...)
}

// The CameraSettings lens fields read as ExifTool reads them (-n): the
// LensType id as a number, the focal range in millimetres, the widest
// aperture as an f-number. The id is only a number: no name is looked up
// for it.
func TestCanonLensID(t *testing.T) {
	for _, c := range []struct {
		name                        string
		id, max, min, units         uint16
		ev                          int16
		wantID                      int
		wantMin, wantMax, wantApert float64
	}{
		{"zoom", 61182, 200, 70, 1, 0x60, 61182, 70, 200, 2.82842712474619},
		{"prime, third stops", 137, 50, 50, 1, 0x2c, 137, 50, 50, math.Exp((32 + 32.0/3) / 32 * math.Ln2 / 2)},
		{"focal units", 4, 220, 60, 4, 0x80, 4, 15, 55, 4},
		{"no lens id", 65535, 20, 10, 1, 0x80, 0, 10, 20, 4},
		{"nothing", 0, 0, 0, 0, 0, 0, 0, 0, 0},
	} {
		file := withSegments(plainJPEG(t), exifSegment(canonSettingsExif(c.id, c.max, c.min, c.units, c.ev)))
		m, err := ReadAll(bytes.NewReader(file), int64(len(file)))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		g := m.Camera
		if g.LensID != c.wantID || math.Abs(g.LensFocalMin-c.wantMin) > 1e-9 || math.Abs(g.LensFocalMax-c.wantMax) > 1e-9 ||
			math.Abs(g.LensMaxAperture-c.wantApert) > 1e-9 {
			t.Errorf("%s: %+v", c.name, g)
		}
		tool, err := exec.LookPath("exiftool")
		if err != nil || c.id == 0 {
			continue
		}
		p := filepath.Join(t.TempDir(), "canon.jpg")
		if err := os.WriteFile(p, file, 0o644); err != nil {
			t.Fatal(err)
		}
		out, err := exec.Command(tool, "-j", "-n", "-LensType", "-MinFocalLength", "-MaxFocalLength", "-MaxAperture", p).Output()
		var got []struct{ LensType, MinFocalLength, MaxFocalLength, MaxAperture float64 }
		if err != nil || json.Unmarshal(out, &got) != nil || len(got) != 1 {
			t.Fatalf("%s: ExifTool %s (%v)", c.name, out, err)
		}
		e := got[0]
		wantID := e.LensType
		if wantID == 65535 {
			wantID = 0 // ExifTool's "n/a": no id
		}
		if float64(g.LensID) != wantID || math.Abs(g.LensFocalMin-e.MinFocalLength) > 1e-6 ||
			math.Abs(g.LensFocalMax-e.MaxFocalLength) > 1e-6 || math.Abs(g.LensMaxAperture-e.MaxAperture) > 1e-6 {
			t.Errorf("%s: %+v, ExifTool %+v", c.name, g, e)
		}
	}
}

// Canon RAW files (MEDIAMETA_RAW_DIR): the lens fields as ExifTool
// reads them, in CR2 (maker note values in the file) and CR3 (CMT3).
func TestCanonLensIDRaw(t *testing.T) {
	dir := os.Getenv("MEDIAMETA_RAW_DIR")
	tool, err := exec.LookPath("exiftool")
	if dir == "" || err != nil {
		t.Skip("MEDIAMETA_RAW_DIR or exiftool not set")
	}
	var paths []string
	for _, ext := range []string{"cr2", "cr3"} {
		p, _ := filepath.Glob(filepath.Join(dir, "*."+ext))
		paths = append(paths, p...)
	}
	for _, p := range paths {
		file := readFile(t, p)
		m, err := ReadAll(bytes.NewReader(file), int64(len(file)))
		if err != nil || !strings.HasPrefix(m.Camera.Make, "Canon") {
			continue
		}
		out, err := exec.Command(tool, "-j", "-n", "-LensType", "-MinFocalLength", "-MaxFocalLength", "-MaxAperture", p).Output()
		var got []struct{ LensType, MinFocalLength, MaxFocalLength, MaxAperture float64 }
		if err != nil || json.Unmarshal(out, &got) != nil || len(got) != 1 {
			t.Fatalf("%s: ExifTool %s (%v)", p, out, err)
		}
		e, g := got[0], m.Camera
		wantID := e.LensType
		if wantID == 65535 {
			wantID = 0
		}
		if float64(g.LensID) != wantID || math.Abs(g.LensFocalMin-e.MinFocalLength) > 1e-6 ||
			math.Abs(g.LensFocalMax-e.MaxFocalLength) > 1e-6 || math.Abs(g.LensMaxAperture-e.MaxAperture) > 1e-6 {
			t.Errorf("%s: %+v, ExifTool %+v", p, g, e)
		}
	}
}
