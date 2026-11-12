package mediameta

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// The orientation tests read results back with this package's own
// reader, so a wrong convention (irot's direction, imir's axis, their
// order) would pass both ways. Here libheif decides: an image libheif
// encoded is given each orientation, libheif decodes it with its
// transformations applied, and the pixels must be the stored image turned
// as the Exif table says. Runs where libheif's heif-enc and heif-dec are
// installed; skipped elsewhere.

// exifView is the stored image src as orientation o shows it, straight
// from the Exif table (CIPA DC-008), independent of this package's
// transforms.
func exifView(src image.Image, o Orientation) *image.RGBA {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	at := func(x, y int) color.Color { return src.At(b.Min.X+x, b.Min.Y+y) }
	dw, dh := w, h
	if o >= 5 {
		dw, dh = h, w
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := range dh {
		for x := range dw {
			var c color.Color
			switch o {
			case 1:
				c = at(x, y)
			case 2: // mirrored left-right
				c = at(w-1-x, y)
			case 3: // rotated 180°
				c = at(w-1-x, h-1-y)
			case 4: // mirrored top-bottom
				c = at(x, h-1-y)
			case 5: // transposed
				c = at(y, x)
			case 6: // rotated 90° clockwise
				c = at(y, h-1-x)
			case 7: // transversed
				c = at(w-1-y, h-1-x)
			case 8: // rotated 270° clockwise
				c = at(w-1-y, x)
			}
			dst.Set(x, y, c)
		}
	}
	return dst
}

// testPattern is an image no turn or mirror maps onto itself.
func testPattern() *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, 48, 32))
	for y := range 32 {
		for x := range 48 {
			c := color.RGBA{uint8(x * 5), uint8(y * 7), 40, 255}
			if x < 12 && y < 8 {
				c = color.RGBA{250, 250, 250, 255} // a corner block
			}
			img.Set(x, y, c)
		}
	}
	return img
}

func libheifTools(t *testing.T) (enc, dec string) {
	t.Helper()
	enc, err1 := exec.LookPath("heif-enc")
	dec, err2 := exec.LookPath("heif-dec")
	if err1 != nil || err2 != nil {
		t.Skip("libheif's heif-enc and heif-dec not installed")
	}
	return enc, dec
}

func decodeWithLibheif(t *testing.T, dec, dir string, file []byte, name string) image.Image {
	t.Helper()
	in := filepath.Join(dir, name+".heic")
	out := filepath.Join(dir, name+".png")
	if err := os.WriteFile(in, file, 0o644); err != nil {
		t.Fatal(err)
	}
	if b, err := exec.Command(dec, in, out).CombinedOutput(); err != nil {
		t.Fatalf("heif-dec %s: %v\n%s", name, err, b)
	}
	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	return img
}

// samePixels compares two images exactly: both decode one bitstream, so
// only the transformations differ.
func samePixels(a, b image.Image) bool {
	if a.Bounds().Dx() != b.Bounds().Dx() || a.Bounds().Dy() != b.Bounds().Dy() {
		return false
	}
	for y := range a.Bounds().Dy() {
		for x := range a.Bounds().Dx() {
			r1, g1, b1, a1 := a.At(a.Bounds().Min.X+x, a.Bounds().Min.Y+y).RGBA()
			r2, g2, b2, a2 := b.At(b.Bounds().Min.X+x, b.Bounds().Min.Y+y).RGBA()
			if r1 != r2 || g1 != g2 || b1 != b2 || a1 != a2 {
				return false
			}
		}
	}
	return true
}

func TestOrientationsDecodeAsExifSaysWithLibheif(t *testing.T) {
	enc, dec := libheifTools(t)
	dir := t.TempDir()
	var pngData bytes.Buffer
	if err := png.Encode(&pngData, testPattern()); err != nil {
		t.Fatal(err)
	}
	srcPNG := filepath.Join(dir, "pattern.png")
	if err := os.WriteFile(srcPNG, pngData.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	srcHEIC := filepath.Join(dir, "pattern.heic")
	if b, err := exec.Command(enc, "-L", "-o", srcHEIC, srcPNG).CombinedOutput(); err != nil {
		t.Skipf("heif-enc can't encode here: %v\n%s", err, b)
	}
	orig, err := os.ReadFile(srcHEIC)
	if err != nil {
		t.Fatal(err)
	}
	if o := orientationOfFile(t, orig); o != 1 {
		t.Fatalf("heif-enc wrote orientation %d", o)
	}
	stored := decodeWithLibheif(t, dec, dir, orig, "stored")
	// From an unmirrored, a mirrored and a mirrored-and-turned start, so
	// adding, keeping and dropping an imir are all decoded.
	for _, start := range []Orientation{1, 2, 7} {
		from := orig
		if start != 1 {
			from = setOrientation(t, orig, start)
		}
		for o := Orientation(1); o <= 8; o++ {
			out := setOrientation(t, from, o)
			got := decodeWithLibheif(t, dec, dir, out, "out")
			if !samePixels(got, exifView(stored, o)) {
				for k := Orientation(1); k <= 8; k++ {
					if samePixels(got, exifView(stored, k)) {
						t.Errorf("from %d, orientation %d: libheif shows %d", start, o, k)
					}
				}
				t.Errorf("from %d, orientation %d: libheif shows something else", start, o)
			}
		}
		// SetRotation keeps the mirror first: from 2, 90° is 7.
		if start == 2 {
			got := decodeWithLibheif(t, dec, dir, setRotation(t, from, 90), "rot")
			if !samePixels(got, exifView(stored, 7)) {
				t.Error("2 turned 90° clockwise doesn't decode as 7")
			}
		}
	}
}
