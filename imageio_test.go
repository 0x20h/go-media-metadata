package mediameta

import (
	"bytes"
	"os"
	"testing"
)

// Files Apple's encoders wrote (testdata/imageio, testdata/iphone; see
// NOTICE): they read as encoded, and every rotation changes the irot angle
// in place, plus the Exif Orientation tag where there is one, and nothing
// else. In the iPhone file the thumbnail and the gain map turn with the
// image.
func TestImageIOFiles(t *testing.T) {
	for _, c := range []struct {
		file   string
		want   Orientation
		hasTag bool
	}{
		{"testdata/imageio/a-orientation1.heic", 1, false},
		{"testdata/imageio/b-orientation6.heic", 6, true},
		// A camera original: grid, thumbnail and gain map share the irot.
		{"testdata/iphone/grid-gainmap-orientation6.heic", 6, true},
	} {
		orig, err := os.ReadFile(c.file)
		if err != nil {
			t.Fatal(err)
		}
		if got := orientationOfFile(t, orig); got != c.want {
			t.Fatalf("%s: orientation %d, want %d", c.file, got, c.want)
		}
		tag, has, err := exifOrientation(bytes.NewReader(orig), int64(len(orig)))
		if err != nil || has != c.hasTag || (has && tag != c.want) {
			t.Fatalf("%s: Exif tag %d (present %v, %v)", c.file, tag, has, err)
		}
		data := itemData(t, orig)
		for deg, want := range map[int]Orientation{0: 1, 90: 6, 180: 3, 270: 8} {
			out := setRotation(t, orig, deg)
			if len(out) != len(orig) {
				t.Errorf("%s %d°: %d bytes, was %d: the irot should change in place", c.file, deg, len(out), len(orig))
			}
			if got := orientationOfFile(t, out); got != want {
				t.Errorf("%s %d°: orientation %d, want %d", c.file, deg, got, want)
			}
			maxDiff := 1
			if c.hasTag {
				maxDiff = 3
			}
			if d := differing(out, orig); d > maxDiff {
				t.Errorf("%s %d°: %d bytes differ, want at most %d", c.file, deg, d, maxDiff)
			}
			tag, has, err := exifOrientation(bytes.NewReader(out), int64(len(out)))
			if err != nil || has != c.hasTag || (has && tag != want) {
				t.Errorf("%s %d°: Exif tag %d (present %v, %v), want %d", c.file, deg, tag, has, err, want)
			}
			relatedInStep(t, orig, out)
			after := itemData(t, out)
			for id, d := range data {
				if n := differing(after[id], d); n != 0 && (n > 2 || !isExif(t, orig, id)) {
					t.Errorf("%s %d°: item %d changed", c.file, deg, id)
				}
			}
		}
	}
}
