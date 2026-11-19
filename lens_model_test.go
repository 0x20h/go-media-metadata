package mediameta

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/0x20h/go-media-metadata/internal/bmff"
)

// canonExif is TIFF data (little-endian) as a Canon camera writes it,
// without Exif's own LensModel: IFD0's Make and Model, an Exif IFD whose
// MakerNote is Canon's IFD with LensModel (0x0095) and another tag.
func canonExif(lens string) []byte {
	le := binary.LittleEndian
	b := []byte{'I', 'I', 42, 0, 8, 0, 0, 0}
	ent := func(out []byte, tag, typ uint16, count, v uint32) []byte {
		out = le.AppendUint16(out, tag)
		out = le.AppendUint16(out, typ)
		out = le.AppendUint32(out, count)
		return le.AppendUint32(out, v)
	}
	// IFD0 at 8: Make, Model, ExifIFD.
	ifd0 := 8
	strs := ifd0 + 2 + 3*12 + 4
	make_, model := "Canon\x00", "Canon EOS R6\x00"
	exifAt := strs + len(make_) + len(model)
	b = le.AppendUint16(b, 3)
	b = ent(b, tagMake, typeASCII, uint32(len(make_)), uint32(strs))
	b = ent(b, tagModel, typeASCII, uint32(len(model)), uint32(strs+len(make_)))
	b = ent(b, tagExifIFD, typeLong, 1, uint32(exifAt))
	b = le.AppendUint32(b, 0)
	b = append(b, make_...)
	b = append(b, model...)
	// The Exif IFD: MakerNote.
	mnAt := exifAt + 2 + 12 + 4
	mn := le.AppendUint16(nil, 2)
	lensAt := mnAt + 2 + 2*12 + 4
	lensz := lens + "\x00"
	mn = ent(mn, 0x0001, typeShort, 1, 7)
	mn = ent(mn, 0x0095, typeASCII, uint32(len(lensz)), uint32(lensAt))
	mn = le.AppendUint32(mn, 0)
	mn = append(mn, lensz...)
	b = le.AppendUint16(b, 1)
	b = ent(b, tagMakerNote, typeUndef, uint32(len(mn)), uint32(mnAt))
	b = le.AppendUint32(b, 0)
	return append(b, mn...)
}

func TestCanonMakerNoteLensModel(t *testing.T) {
	const lens = "RF24-105mm F4 L IS USM"
	file := withSegments(plainJPEG(t), exifSegment(canonExif(lens)))
	m, err := ReadAll(bytes.NewReader(file), int64(len(file)))
	if err != nil || m.Camera.LensModel != lens || m.Camera.Model != "Canon EOS R6" {
		t.Fatalf("camera %+v, %v", m.Camera, err)
	}
	tool, err := exec.LookPath("exiftool")
	if err != nil {
		t.Skip("exiftool not installed")
	}
	p := filepath.Join(t.TempDir(), "canon.jpg")
	if err := os.WriteFile(p, file, 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(tool, "-j", "-q", "-LensModel", p).Output()
	var got []map[string]any
	if err != nil || json.Unmarshal(out, &got) != nil || len(got) != 1 || got[0]["LensModel"] != lens {
		t.Errorf("ExifTool %s (%v)", out, err)
	}
}

// An iPhone movie's Keys entry com.apple.quicktime.camera.lens_model
// reads as ExifTool's Keys CameraLensModel. (ExifTool doesn't write that key, so it
// goes into the corpus movie through this package's Keys writer.)
func TestKeysLensModel(t *testing.T) {
	const lens = "iPhone 15 Pro back triple camera 6.765mm f/1.78"
	src := readFile(t, "testdata/video/keys.mov")
	top, err := bmff.ReadBoxes(bytes.NewReader(src), 0, int64(len(src)))
	if err != nil {
		t.Fatal(err)
	}
	moovBox, _ := bmff.Find(top, bmff.T("moov"))
	moov, err := parseQTNode(src[moovBox.Offset:moovBox.End()], 0)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := keysMeta(moov, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := keys.set(keyLensModel, lens); err != nil {
		t.Fatal(err)
	}
	// The new moov in the old one's place: the chunk offsets aren't
	// moved, which the metadata read doesn't look at.
	file := append(append(append([]byte(nil), src[:moovBox.Offset]...), moov.encode()...), src[moovBox.End():]...)
	v, err := ReadVideo(bytes.NewReader(file), int64(len(file)))
	if err != nil || v.LensModel != lens || v.Make != "Apple" {
		t.Fatalf("%+v, %v", v, err)
	}
	m, err := ReadAll(bytes.NewReader(file), int64(len(file)))
	if err != nil || m.Camera.LensModel != lens || m.Camera.Make != "Apple" {
		t.Fatalf("ReadAll %+v, %v", m.Camera, err)
	}
	tool, err := exec.LookPath("exiftool")
	if err != nil {
		t.Skip("exiftool not installed")
	}
	p := filepath.Join(t.TempDir(), "lens.mov")
	if err := os.WriteFile(p, file, 0o644); err != nil {
		t.Fatal(err)
	}
	// ExifTool's name for the key: Keys CameraLensModel.
	out, err := exec.Command(tool, "-j", "-q", "-Keys:CameraLensModel", p).Output()
	var got []map[string]any
	if err != nil || json.Unmarshal(out, &got) != nil || len(got) != 1 || got[0]["CameraLensModel"] != lens {
		t.Errorf("ExifTool %s (%v)", out, err)
	}
}
