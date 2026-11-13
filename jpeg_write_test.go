package mediameta

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image/jpeg"
	"testing"
)

// orientationJPEG is a JPEG whose Exif IFD0 holds Orientation o in the
// given type, with a date in the Exif IFD after it so that the patch has
// neighbours to leave alone.
func orientationJPEG(t testing.TB, order byteOrder, typ uint16, o uint32) []byte {
	b := &tiffBuilder{order: order}
	var v []byte
	switch typ {
	case typeByte:
		v = []byte{byte(o)}
	case typeShort:
		v = b.short(uint16(o))
	case typeLong:
		v = b.long(o)
	}
	n, date := asciiValue("2025:05:04 13:14:15")
	b.ifd0 = []tiffEntry{{tagOrientation, typ, 1, v}}
	b.exif = []tiffEntry{{tagDateTimeOriginal, typeASCII, n, date}}
	return withSegments(plainJPEG(t), exifSegment(b.build()))
}

// diffRanges are the offsets where two files of one size differ.
func diffOffsets(a, b []byte) []int {
	var out []int
	for i := range a {
		if a[i] != b[i] {
			out = append(out, i)
		}
	}
	return out
}

// checkJPEGEdit checks an edited JPEG against its source: it decodes to
// the same pixels, its scan data (everything from SOS on) is byte for
// byte the source's, and its other metadata reads the same.
func checkJPEGEdit(t *testing.T, src, out []byte) {
	t.Helper()
	sos := func(b []byte) []byte { return b[bytes.Index(b, []byte{0xFF, markerSOS}):] }
	if !bytes.Equal(sos(src), sos(out)) {
		t.Fatal("the image data changed")
	}
	a, err1 := jpeg.Decode(bytes.NewReader(src))
	b, err2 := jpeg.Decode(bytes.NewReader(out))
	if err1 != nil || err2 != nil || a.Bounds() != b.Bounds() {
		t.Fatalf("decode: %v, %v", err1, err2)
	}
	xa, xb := readExif(t, src), readExif(t, out)
	if !xa.DateTimeOriginal.Equal(xb.DateTimeOriginal) || xa.OffsetKnown != xb.OffsetKnown {
		t.Errorf("the date changed: %v, %v", xa.DateTimeOriginal, xb.DateTimeOriginal)
	}
}

func setOrientationJPEG(t *testing.T, file []byte, o Orientation) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := SetOrientation(bytes.NewReader(file), int64(len(file)), &out, o); err != nil {
		t.Fatalf("SetOrientation(%d): %v", o, err)
	}
	return out.Bytes()
}

// Every orientation, from a tag of each type and byte order: only the
// tag's bytes change, the plan's patch is exactly those bytes, applying it
// in place gives the streamed file, and setting it again changes nothing.
func TestSetOrientationJPEG(t *testing.T) {
	for _, order := range []byteOrder{binary.LittleEndian, binary.BigEndian} {
		for _, typ := range []uint16{typeShort, typeLong, typeByte} {
			src := orientationJPEG(t, order, typ, 1)
			width := typeSize(typ)
			for want := Orientation(1); want <= 8; want++ {
				t.Run(fmt.Sprintf("%v/type%d/%d", order, typ, want), func(t *testing.T) {
					out := setOrientationJPEG(t, src, want)
					if got := orientationOfFile(t, out); got != want {
						t.Fatalf("orientation %d, want %d", got, want)
					}
					if len(out) != len(src) {
						t.Fatalf("size %d, want %d", len(out), len(src))
					}
					diff := diffOffsets(src, out)
					if want == 1 && len(diff) != 0 {
						t.Fatalf("setting the orientation it has changed %d bytes", len(diff))
					}
					if len(diff) > width {
						t.Fatalf("%d bytes changed, the tag has %d", len(diff), width)
					}
					checkJPEGEdit(t, src, out)

					plan, err := PlanOrientation(bytes.NewReader(src), int64(len(src)), want)
					if err != nil {
						t.Fatal(err)
					}
					if !plan.InPlace || plan.NewSize != plan.Size {
						t.Fatalf("plan in place %v, %d → %d bytes", plan.InPlace, plan.Size, plan.NewSize)
					}
					clone := &writerAt{b: bytes.Clone(src)}
					if err := plan.Apply(clone); err != nil {
						t.Fatal(err)
					}
					if !bytes.Equal(clone.b, out) {
						t.Fatal("Apply differs from SetOrientation")
					}
					for _, p := range plan.Patches {
						if len(diff) > 0 && (p.Offset > int64(diff[0]) || p.Offset+int64(len(p.Data)) <= int64(diff[len(diff)-1])) {
							t.Errorf("patch at %d+%d doesn't cover the changed bytes %v", p.Offset, len(p.Data), diff)
						}
					}
					var streamed bytes.Buffer
					if _, err := plan.WriteTo(&streamed); err != nil || !bytes.Equal(streamed.Bytes(), out) {
						t.Fatalf("WriteTo differs from SetOrientation (%v)", err)
					}
					if again := setOrientationJPEG(t, out, want); !bytes.Equal(again, out) {
						t.Fatal("setting it again changed the file")
					}
				})
			}
		}
	}
}

// writerAt is an in-memory io.WriterAt over a copy of the source.
type writerAt struct{ b []byte }

func (w *writerAt) WriteAt(p []byte, off int64) (int, error) {
	if off < 0 || off+int64(len(p)) > int64(len(w.b)) {
		return 0, errors.New("out of range")
	}
	return copy(w.b[off:], p), nil
}

// SetRotation replaces the rotation and keeps a mirroring, as for HEIF.
func TestSetRotationJPEG(t *testing.T) {
	for _, c := range []struct {
		from    uint32
		degrees int
		want    Orientation
	}{
		{1, 90, 6}, {6, 90, 6}, {6, 0, 1}, {6, 180, 3}, {3, -90, 8}, {8, 450, 6},
		{2, 90, 7}, {7, 0, 2}, {5, 180, 4}, {9, 90, 6}, {0, 270, 8},
	} {
		src := orientationJPEG(t, binary.BigEndian, typeShort, c.from)
		var out bytes.Buffer
		if err := SetRotation(bytes.NewReader(src), int64(len(src)), &out, c.degrees); err != nil {
			t.Fatalf("from %d by %d: %v", c.from, c.degrees, err)
		}
		if got := orientationOfFile(t, out.Bytes()); got != c.want {
			t.Errorf("from %d, rotation %d: orientation %d, want %d", c.from, c.degrees, got, c.want)
		}
		if got := Orientation(c.want).Degrees(); got != ((c.degrees%360)+360)%360 {
			t.Errorf("from %d, rotation %d: shows %d°", c.from, c.degrees, got)
		}
		checkJPEGEdit(t, src, out.Bytes())
	}
	src := orientationJPEG(t, binary.BigEndian, typeShort, 1)
	if err := SetRotation(bytes.NewReader(src), int64(len(src)), &bytes.Buffer{}, 45); err == nil {
		t.Error("a rotation of 45° was accepted")
	}
}

// A JPEG without Exif gets a segment holding only the tag, after the JFIF
// segment; one that needs no tag is left as it is.
func TestSetOrientationJPEGWithoutExif(t *testing.T) {
	jfif := segment(markerAPP0, []byte("JFIF\x00\x01\x02\x00\x00\x01\x00\x01\x00\x00"))
	for name, src := range map[string][]byte{
		"plain":     plainJPEG(t),
		"with JFIF": withSegments(plainJPEG(t), jfif),
	} {
		out := setOrientationJPEG(t, src, 6)
		if got := orientationOfFile(t, out); got != 6 {
			t.Fatalf("%s: orientation %d, want 6", name, got)
		}
		checkJPEGEdit(t, src, out)
		ins := len(out) - len(src)
		at := 2
		if bytes.HasPrefix(src[2:], jfif) {
			at += len(jfif)
		}
		if !bytes.Equal(out[:at], src[:at]) || !bytes.Equal(out[at+ins:], src[at:]) {
			t.Errorf("%s: the segment isn't inserted at %d", name, at)
		}
		plan, err := PlanRotation(bytes.NewReader(src), int64(len(src)), 90)
		if err != nil {
			t.Fatal(err)
		}
		if plan.InPlace || plan.NewSize != int64(len(out)) || plan.Apply(&writerAt{b: bytes.Clone(src)}) != ErrNotInPlace {
			t.Errorf("%s: plan in place %v, new size %d", name, plan.InPlace, plan.NewSize)
		}
		var streamed bytes.Buffer
		if _, err := plan.WriteTo(&streamed); err != nil || !bytes.Equal(streamed.Bytes(), out) {
			t.Errorf("%s: WriteTo differs from SetOrientation (%v)", name, err)
		}
		if same := setOrientationJPEG(t, src, 1); !bytes.Equal(same, src) {
			t.Errorf("%s: orientation 1 changed a file without Exif", name)
		}
	}
}

// noTagJPEG is a JPEG whose Exif has a date and a GPS position but no
// Orientation tag, as some editors write it.
func noTagJPEG(t testing.TB, order byteOrder) []byte {
	b := &tiffBuilder{order: order, ifd0: []tiffEntry{}}
	n, date := asciiValue("2025:05:04 13:14:15")
	b.exif = []tiffEntry{{tagDateTimeOriginal, typeASCII, n, date}}
	b.gp = []tiffEntry{
		{tagGPSLatitudeRef, typeASCII, 2, []byte("N\x00")},
		{tagGPSLatitude, typeRational, 3, b.rationals([2]uint32{51, 1}, [2]uint32{30, 1}, [2]uint32{0, 1})},
		{tagGPSLongitudeRef, typeASCII, 2, []byte("E\x00")},
		{tagGPSLongitude, typeRational, 3, b.rationals([2]uint32{7, 1}, [2]uint32{0, 1}, [2]uint32{0, 1})},
	}
	return withSegments(plainJPEG(t), exifSegment(b.build()))
}

// An Exif segment without the tag gets it through EditExif: every
// orientation reads back, the date and position stay, the image data is
// untouched, and from then on a change is a patch of the tag in place.
func TestSetOrientationJPEGWithoutTag(t *testing.T) {
	for _, order := range []byteOrder{binary.LittleEndian, binary.BigEndian} {
		src := noTagJPEG(t, order)
		before := readExif(t, src)
		for want := Orientation(2); want <= 8; want++ {
			out := setOrientationJPEG(t, src, want)
			if got := orientationOfFile(t, out); got != want {
				t.Fatalf("%v: orientation %d, want %d", order, got, want)
			}
			checkJPEGEdit(t, src, out)
			after := readExif(t, out)
			if after.GPS == nil || after.GPS.Latitude != before.GPS.Latitude || after.GPS.Longitude != before.GPS.Longitude {
				t.Errorf("%v: the position changed: %+v", order, after.GPS)
			}
			plan, err := PlanOrientation(bytes.NewReader(src), int64(len(src)), want)
			if err != nil {
				t.Fatal(err)
			}
			checkPlan(t, src, plan)
			var streamed bytes.Buffer
			if _, err := plan.WriteTo(&streamed); err != nil || !bytes.Equal(streamed.Bytes(), out) {
				t.Fatalf("%v: WriteTo differs from SetOrientation (%v)", order, err)
			}
			// The tag is there now: the next change is in place.
			next := Orientation(1 + want%8)
			p2, err := PlanOrientation(bytes.NewReader(out), int64(len(out)), next)
			if err != nil || !p2.InPlace || len(p2.Patches) != 1 {
				t.Fatalf("%v: second edit: in place %v, %d patches, %v", order, p2 != nil && p2.InPlace, len(p2.Patches), err)
			}
		}
		// Orientation 1, which such a file shows already, changes nothing.
		if same := setOrientationJPEG(t, src, 1); !bytes.Equal(same, src) {
			t.Errorf("%v: orientation 1 changed the file", order)
		}
		// SetRotation from none: rotated, not mirrored.
		var out bytes.Buffer
		if err := SetRotation(bytes.NewReader(src), int64(len(src)), &out, 90); err != nil {
			t.Fatal(err)
		}
		if got := orientationOfFile(t, out.Bytes()); got != 6 {
			t.Errorf("%v: rotated 90: orientation %d, want 6", order, got)
		}
	}
}

// Only the first Exif segment, the one read, is changed.
func TestSetOrientationJPEGTwoExifSegments(t *testing.T) {
	tiff := func(o uint16) []byte {
		b := &tiffBuilder{order: binary.BigEndian}
		b.ifd0 = []tiffEntry{{tagOrientation, typeShort, 1, b.short(o)}}
		return b.build()
	}
	src := withSegments(plainJPEG(t), exifSegment(tiff(1)), exifSegment(tiff(3)))
	out := setOrientationJPEG(t, src, 6)
	if got := orientationOfFile(t, out); got != 6 {
		t.Fatalf("orientation %d, want 6", got)
	}
	if d := diffOffsets(src, out); len(d) != 1 || d[0] > bytes.Index(src[4:], []byte{0xFF, markerAPP1})+4+len(exifSegment(tiff(1))) {
		t.Errorf("changed bytes %v, want one in the first segment", d)
	}
}

// HEIF files take the HEIF path whatever their Exif says, so the two are
// never applied together: the irot/imir decide, and the tag follows.
func TestSetOrientationLeavesHEIFToHEIF(t *testing.T) {
	file := readFile(t, "testdata/imageio/b-orientation6.heic")
	out := setOrientationJPEG(t, file, 3)
	if got := orientationOfFile(t, out); got != 3 {
		t.Errorf("orientation %d, want 3", got)
	}
	if x := readExif(t, out); x.Orientation != 3 {
		t.Errorf("Exif Orientation %d, want 3 (in step)", x.Orientation)
	}
}
