package mediameta

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/0x20h/go-media-metadata/internal/bmff/qtbuild"
)

// ReadImageSize gives ExifTool's composite ImageSize on every corpus file
// and, with MEDIAMETA_RAW_DIR set, on real RAW files; Shown swaps it for
// an orientation that turns the image a quarter.
func TestImageSizeAgreesWithExifTool(t *testing.T) {
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
	out, err := exec.Command(tool, append([]string{"-j", "-n", "-q", "-Composite:ImageSize"}, paths...)...).Output()
	if err != nil {
		t.Fatalf("exiftool: %v", err)
	}
	var got []struct {
		SourceFile, ImageSize string
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(paths) {
		t.Fatalf("exiftool read %d files of %d", len(got), len(paths))
	}
	turned := 0
	for _, e := range got {
		file := readFile(t, e.SourceFile)
		s, err := ReadImageSize(bytes.NewReader(file), int64(len(file)))
		if err != nil {
			t.Errorf("%s: %v", e.SourceFile, err)
			continue
		}
		if want := strings.Fields(e.ImageSize); len(want) != 2 || want[0] != itoa(s.Width) || want[1] != itoa(s.Height) {
			t.Errorf("%s: %d×%d, ExifTool %q", e.SourceFile, s.Width, s.Height, e.ImageSize)
		}
		o, _ := ReadOrientation(bytes.NewReader(file), int64(len(file)))
		w, h := s.Shown()
		if s.Orientation != o || (o >= 5) != (w == s.Height && h == s.Width && w != h) {
			t.Errorf("%s: shown %d×%d at %d (ReadOrientation %d)", e.SourceFile, w, h, s.Orientation, o)
		}
		if o >= 5 {
			turned++
		}
	}
	if turned == 0 {
		t.Error("no corpus file turned a quarter")
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

// Each format's size field, in files made here.
func TestImageSizeFormats(t *testing.T) {
	var pngFile bytes.Buffer
	if err := png.Encode(&pngFile, image.NewGray(image.Rect(0, 0, 7, 5))); err != nil {
		t.Fatal(err)
	}
	// A lossless WebP: 0x2F, then 14 bits each of width-1 and height-1.
	vp8l := func(w, h int) []byte {
		bits := uint32(w-1) | uint32(h-1)<<14
		payload := append([]byte{0x2F}, binary.LittleEndian.AppendUint32(nil, bits)...)
		chunk := append(append([]byte("VP8L"), binary.LittleEndian.AppendUint32(nil, uint32(len(payload)))...), payload...)
		chunk = append(chunk, 0) // padding to even
		riff := append([]byte("WEBP"), chunk...)
		return append(append([]byte("RIFF"), binary.LittleEndian.AppendUint32(nil, uint32(len(riff)))...), riff...)
	}
	// A JPEG XL codestream of 1500×1000: a large SizeHeader, ysize 1000
	// in the 13-bit form, ratio 3:2.
	jxlLarge := func() []byte {
		w := &jxlBits{}
		w.put(0, 1)    // small
		w.put(1, 2)    // u32 selector: 13 bits
		w.put(999, 13) // ysize - 1
		w.put(4, 3)    // ratio 3:2
		w.put(1, 1)    // all_default
		return append(append(append([]byte(nil), jxlCodestream...), w.b...), bytes.Repeat([]byte{0xA5}, 64)...)
	}
	movie, _, _ := qtbuild.Build(qtbuild.Default()) // 1920×1080 turned 90°
	for _, c := range []struct {
		name   string
		file   []byte
		w, h   int
		shownW int
	}{
		{"PNG", pngFile.Bytes(), 7, 5, 7},
		{"VP8L", vp8l(300, 200), 300, 200, 300},
		{"JPEG XL small", jxlCodestreamOf(6), 8, 8, 8},
		{"JPEG XL large", jxlLarge(), 1500, 1000, 1500},
		{"movie", movie, 1920, 1080, 1080},
	} {
		s, err := ReadImageSize(bytes.NewReader(c.file), int64(len(c.file)))
		if w, _ := s.Shown(); err != nil || s.Width != c.w || s.Height != c.h || w != c.shownW {
			t.Errorf("%s: %+v (shown width %d), %v; want %d×%d, shown width %d", c.name, s, w, err, c.w, c.h, c.shownW)
		}
	}
	if _, err := ReadImageSize(bytes.NewReader([]byte("not an image at all")), 19); !errors.Is(err, ErrUnknownFormat) {
		t.Errorf("text: %v, want ErrUnknownFormat", err)
	}
}

// Writes leave the stored size as it is.
func TestImageSizeAfterWrites(t *testing.T) {
	for name, file := range imageIOFiles(t) {
		before, err := ReadImageSize(bytes.NewReader(file), int64(len(file)))
		if err != nil {
			t.Fatal(err)
		}
		out := planOut(t, file, func() (*Plan, error) { return PlanOrientation(bytes.NewReader(file), int64(len(file)), 8) })
		after, err := ReadImageSize(bytes.NewReader(out), int64(len(out)))
		if err != nil || after.Width != before.Width || after.Height != before.Height || after.Orientation != 8 {
			t.Errorf("%s: %+v (%v) after orientation 8, was %+v", name, after, err, before)
		}
	}
}
