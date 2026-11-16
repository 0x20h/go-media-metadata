package mediameta

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// tinyTIFF builds an uncompressed 4×3 RGB TIFF without an Orientation
// tag: IFD0 with the image's tags, a strip, a description (a value past 4
// bytes), Software (a tag after Orientation's, 0x131), an ExifIFD with a
// DateTimeOriginal, and IFD1, a 1×1 thumbnail.
func tinyTIFF(order binary.ByteOrder, odd bool) []byte {
	ord := order.(binary.AppendByteOrder)
	var b []byte
	if order == binary.LittleEndian {
		b = append(b, 'I', 'I', '*', 0)
	} else {
		b = append(b, 'M', 'M', 0, '*')
	}
	b = ord.AppendUint32(b, 0) // IFD0's offset, set below
	pixels := bytes.Repeat([]byte{200, 100, 50}, 12)
	stripAt := len(b)
	b = append(b, pixels...)
	descAt := len(b)
	b = append(b, "A test pattern\x00"...)
	softAt := len(b)
	b = append(b, "tiny\x00\x00"...)
	dtoAt := len(b)
	b = append(b, "2021:07:08 09:10:11\x00"...)
	thumbAt := len(b)
	b = append(b, 1, 2, 3)
	if odd {
		b = append(b, 0) // so the file ends at an odd offset
	}
	type ent struct {
		tag, typ uint16
		count    uint32
		v        uint32
	}
	ifd := func(es []ent, next uint32) []byte {
		out := ord.AppendUint16(nil, uint16(len(es)))
		for _, e := range es {
			out = ord.AppendUint16(out, e.tag)
			out = ord.AppendUint16(out, e.typ)
			out = ord.AppendUint32(out, e.count)
			if e.typ == typeShort && e.count == 1 {
				out = ord.AppendUint16(out, uint16(e.v))
				out = append(out, 0, 0)
			} else {
				out = ord.AppendUint32(out, e.v)
			}
		}
		return ord.AppendUint32(out, next)
	}
	if len(b)%2 == 1 {
		b = append(b, 0)
	}
	exifAt := len(b)
	b = append(b, ifd([]ent{{0x9003, typeASCII, 20, uint32(dtoAt)}}, 0)...)
	ifd1At := len(b)
	b = append(b, ifd([]ent{{256, typeShort, 1, 1}, {257, typeShort, 1, 1}, {259, typeShort, 1, 1}, {262, typeShort, 1, 2},
		{273, typeLong, 1, uint32(thumbAt)}, {277, typeShort, 1, 3}, {279, typeLong, 1, 3}}, 0)...)
	ifd0At := len(b)
	b = append(b, ifd([]ent{{256, typeShort, 1, 4}, {257, typeShort, 1, 3}, {259, typeShort, 1, 1}, {262, typeShort, 1, 2},
		{270, typeASCII, 15, uint32(descAt)}, {273, typeLong, 1, uint32(stripAt)}, {277, typeShort, 1, 3},
		{278, typeShort, 1, 3}, {279, typeLong, 1, uint32(len(pixels))}, {305, typeASCII, 5, uint32(softAt)},
		{tagExifIFD, typeLong, 1, uint32(exifAt)}}, uint32(ifd1At))...)
	if odd {
		b = append(b, 0)
	}
	order.PutUint32(b[4:], uint32(ifd0At))
	return b
}

func imageHash(t *testing.T, file []byte) [32]byte {
	t.Helper()
	h := sha256.New()
	if _, err := HashImageData(bytes.NewReader(file), int64(len(file)), h); err != nil {
		t.Fatalf("HashImageData: %v", err)
	}
	return [32]byte(h.Sum(nil))
}

func apply(t *testing.T, p *Plan) []byte {
	t.Helper()
	var buf bytes.Buffer
	if _, err := p.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// A TIFF without an Orientation tag gets one: every orientation, either
// byte order, an odd-sized file; the image data and every other value
// stay, and a second turn is in place.
func TestOrientationAddedToTIFF(t *testing.T) {
	for _, order := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		for _, odd := range []bool{false, true} {
			src := tinyTIFF(order, odd)
			before, err := ReadExif(bytes.NewReader(src), int64(len(src)))
			if err != nil {
				t.Fatal(err)
			}
			hash := imageHash(t, src)
			for o := Orientation(1); o <= 8; o++ {
				p, err := PlanOrientation(bytes.NewReader(src), int64(len(src)), o)
				if err != nil {
					t.Fatalf("%v odd=%v %d: %v", order, odd, o, err)
				}
				if p.InPlace || p.NewSize <= p.Size {
					t.Errorf("%d: a plan in place (%v), %d → %d bytes", o, p.InPlace, p.Size, p.NewSize)
				}
				out := apply(t, p)
				if !bytes.Equal(out[:4], src[:4]) || !bytes.Equal(out[8:len(src)], src[8:]) {
					t.Errorf("%d: bytes other than the header's IFD offset changed", o)
				}
				if got, err := ReadOrientation(bytes.NewReader(out), int64(len(out))); err != nil || got != o {
					t.Errorf("%v %d: read %v, %v", order, o, got, err)
				}
				after, err := ReadExif(bytes.NewReader(out), int64(len(out)))
				if err != nil || after.Orientation != o || after.DateTimeOriginal != before.DateTimeOriginal ||
					after.ImageDescription != before.ImageDescription {
					t.Errorf("%d: Exif %+v (%v), before %+v", o, after, err, before)
				}
				if imageHash(t, out) != hash {
					t.Errorf("%d: the image data hash changed", o)
				}
				// Now that the tag is there, the next turn is in place.
				p2, err := PlanOrientation(bytes.NewReader(out), int64(len(out)), o%8+1)
				if err != nil || !p2.InPlace {
					t.Errorf("%d: the second turn: %+v, %v", o, p2, err)
				}
			}
		}
	}
}

// SetOrientation and PlanRotation take the same way.
func TestOrientationAddedThroughSetAndRotate(t *testing.T) {
	src := tinyTIFF(binary.BigEndian, true)
	var buf bytes.Buffer
	if err := SetOrientation(bytes.NewReader(src), int64(len(src)), &buf, 5); err != nil {
		t.Fatal(err)
	}
	if o, err := ReadOrientation(bytes.NewReader(buf.Bytes()), int64(buf.Len())); err != nil || o != 5 {
		t.Errorf("SetOrientation: %v, %v", o, err)
	}
	p, err := PlanRotation(bytes.NewReader(src), int64(len(src)), 90)
	if err != nil {
		t.Fatal(err)
	}
	out := apply(t, p)
	if o, err := ReadOrientation(bytes.NewReader(out), int64(len(out))); err != nil || o != 6 {
		t.Errorf("PlanRotation 90: %v, %v", o, err)
	}
}

// ExifTool reads the orientation written into a TIFF without one, and
// validates the file as it did before.
func TestOrientationAddedAgreesWithExifTool(t *testing.T) {
	tool, err := exec.LookPath("exiftool")
	if err != nil {
		t.Skip("exiftool not installed")
	}
	dir := t.TempDir()
	write := func(name string, b []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	inputs := map[string][]byte{"le.tif": tinyTIFF(binary.LittleEndian, true), "be.tif": tinyTIFF(binary.BigEndian, false)}
	// ffmpeg's TIFFs have no Orientation tag either.
	if ff, err := exec.LookPath("ffmpeg"); err == nil {
		p := filepath.Join(dir, "ffmpeg-src.tif")
		if exec.Command(ff, "-v", "error", "-y", "-f", "lavfi", "-i", "testsrc=size=64x48", "-frames:v", "1", "-c:v", "tiff", p).Run() == nil {
			inputs["ffmpeg.tif"] = readFile(t, p)
		}
	}
	// A DNG with its Orientation tag removed, when the RAW samples are here.
	if raw := os.Getenv("MEDIAMETA_RAW_DIR"); raw != "" {
		if b, err := os.ReadFile(filepath.Join(raw, "dng-camera.dng")); err == nil {
			p := write("dng-src.dng", b)
			if exec.Command(tool, "-q", "-overwrite_original", "-IFD0:Orientation=", p).Run() == nil {
				inputs["camera.dng"] = readFile(t, p)
			}
		}
	}
	validate := func(p string) (map[string]any, error) {
		out, err := exec.Command(tool, "-j", "-n", "-q", "-validate", "-IFD0:Orientation", p).Output()
		if err != nil {
			return nil, err
		}
		var got []map[string]any
		if err := json.Unmarshal(out, &got); err != nil || len(got) != 1 {
			return nil, fmt.Errorf("%v: %s", err, out)
		}
		return got[0], nil
	}
	// ffmpeg's TIFF and the DNGs are added where they're at hand (not on
	// CI); the synthetic TIFFs always are.
	if len(inputs) < 2 {
		t.Errorf("%d inputs", len(inputs))
	}
	for name, src := range inputs {
		t.Logf("%s: %d bytes", name, len(src))
		before, err := validate(write("src-"+name, src))
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := before["Orientation"]; ok {
			t.Fatalf("%s: has an Orientation already", name)
		}
		hash := imageHash(t, src)
		for _, o := range []Orientation{6, 3, 8, 2} {
			p, err := PlanOrientation(bytes.NewReader(src), int64(len(src)), o)
			if err != nil {
				t.Fatalf("%s %d: %v", name, o, err)
			}
			out := apply(t, p)
			after, err := validate(write(fmt.Sprintf("%d-%s", o, name), out))
			if err != nil {
				t.Fatal(err)
			}
			if got, _ := after["Orientation"].(float64); got != float64(o) {
				t.Errorf("%s: ExifTool reads %v, wrote %d", name, after["Orientation"], o)
			}
			if after["Validate"] != before["Validate"] {
				t.Errorf("%s %d: ExifTool's validation %v, before %v", name, o, after["Validate"], before["Validate"])
			}
			if imageHash(t, out) != hash {
				t.Errorf("%s %d: the image data hash changed", name, o)
			}
		}
	}
}
