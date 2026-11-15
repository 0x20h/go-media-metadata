package mediameta

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// imageDataMD5 is the MD5 of HashImageData's output, "" without image
// data or on an error.
func imageDataMD5(t testing.TB, file []byte) (string, error) {
	t.Helper()
	h := md5.New()
	n, err := HashImageData(bytes.NewReader(file), int64(len(file)), h)
	if err != nil || n == 0 {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// hashTestFiles are files of every layout HashImageData reads: the
// corpora, the RAW samples with MEDIAMETA_RAW_DIR, JPEG XL layouts, a
// Motion Photo, JPEGs cut short or with fill bytes, and, where jpegtran,
// cwebp and ffmpeg are installed, progressive and restart-marker JPEGs,
// WebP files and movies made from a test pattern.
func hashTestFiles(t *testing.T) []string {
	t.Helper()
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
	dir := t.TempDir()
	put := func(name string, b []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
		return p
	}
	for i, l := range jxlLayouts {
		file, _ := buildJXL(t, l, 6)
		put("layout"+string(rune('a'+i))+".jxl", file)
	}
	put("motion.jpg", buildMotionPhoto(t, motionOpts{}).file)
	base := readFile(t, "testdata/exiftool/all.jpg")
	eoi := bytes.LastIndex(base, []byte{0xFF, markerEOI})
	put("no-eoi.jpg", base[:eoi])
	put("cut.jpg", base[:len(base)-100])
	put("fill.jpg", append(append(append([]byte(nil), base[:eoi]...), 0xFF, 0xFF), base[eoi:]...))
	run := func(name string, args ...string) bool {
		if _, err := exec.LookPath(name); err != nil {
			return false
		}
		cmd := exec.Command(name, args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Logf("%s: %v\n%s", name, err, out)
			return false
		}
		return true
	}
	jpeg := put("base.jpg", base)
	for name, args := range map[string][]string{
		"progressive.jpg":     {"-progressive"},
		"restart.jpg":         {"-restart", "1"},
		"progressive-rst.jpg": {"-progressive", "-restart", "1"},
	} {
		if run("jpegtran", append(append(args, "-outfile", name), jpeg)...) {
			paths = append(paths, filepath.Join(dir, name))
		}
	}
	if run("ffmpeg", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc=size=64x48:rate=5", "-frames:v", "1", "-pix_fmt", "rgb24", "pattern.png") {
		paths = append(paths, filepath.Join(dir, "pattern.png"))
		if run("cwebp", "-quiet", "-q", "80", "pattern.png", "-o", "lossy.webp") && run("cwebp", "-quiet", "-lossless", "pattern.png", "-o", "lossless.webp") {
			paths = append(paths, filepath.Join(dir, "lossy.webp"), filepath.Join(dir, "lossless.webp"))
		}
	}
	for name, args := range map[string][]string{
		"av.mp4": {"-c:v", "libx264", "-c:a", "aac"},
		"av.mov": {"-c:v", "mpeg4", "-c:a", "pcm_s16le"},
	} {
		in := []string{"-loglevel", "error", "-f", "lavfi", "-i", "testsrc=size=64x48:rate=10", "-f", "lavfi", "-i", "sine=frequency=440", "-t", "1", "-shortest"}
		if run("ffmpeg", append(append(in, args...), name)...) {
			paths = append(paths, filepath.Join(dir, name))
		}
	}
	return paths
}

// HashImageData's MD5 is ExifTool's ImageDataHash, where ExifTool gives
// one. ExifTool gives none for an ORF (its header isn't TIFF's); this
// package hashes the ORF's strips.
func TestHashImageDataAgreesWithExifTool(t *testing.T) {
	tool, err := exec.LookPath("exiftool")
	if err != nil {
		t.Skip("exiftool not installed")
	}
	paths := hashTestFiles(t)
	list := filepath.Join(t.TempDir(), "list")
	if err := os.WriteFile(list, []byte(strings.Join(paths, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(tool, "-j", "-q", "-q", "-ImageDataHash", "-api", "ImageHashType=MD5", "-@", list).Output()
	if err != nil && len(out) == 0 {
		t.Fatalf("exiftool: %v", err)
	}
	var got []struct{ SourceFile, ImageDataHash string }
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(paths) {
		t.Fatalf("exiftool read %d files of %d", len(got), len(paths))
	}
	compared := 0
	for _, e := range got {
		sum, err := imageDataMD5(t, readFile(t, e.SourceFile))
		switch {
		case strings.EqualFold(filepath.Ext(e.SourceFile), ".orf"):
			if e.ImageDataHash != "" || sum == "" {
				t.Errorf("%s: %q (%v), ExifTool %q", e.SourceFile, sum, err, e.ImageDataHash)
			}
		case sum != e.ImageDataHash:
			t.Errorf("%s: %q (%v), ExifTool %q", e.SourceFile, sum, err, e.ImageDataHash)
		default:
			compared++
		}
	}
	t.Logf("%d files agree with ExifTool", compared)
}

// Metadata writes leave the hash as it was; a changed pixel doesn't.
func TestHashImageDataAcrossWrites(t *testing.T) {
	files := imageIOFiles(t)
	for name, file := range corpus(t) {
		files[name] = file
	}
	for name, file := range files {
		before, err := imageDataMD5(t, file)
		if err != nil || before == "" {
			t.Fatalf("%s: %q, %v", name, before, err)
		}
		for edit, apply := range corpusEdits {
			if edit == "iptc keywords" && !strings.HasSuffix(name, ".jpg") {
				continue
			}
			w := corpusWant{}
			out := apply(t, file, &w)
			if after, err := imageDataMD5(t, out); after != before {
				t.Errorf("%s after %s: %q (%v), was %q", name, edit, after, err, before)
			}
		}
		rotated := planOut(t, file, func() (*Plan, error) { return PlanRotation(bytes.NewReader(file), int64(len(file)), 90) })
		if after, _ := imageDataMD5(t, rotated); after != before {
			t.Errorf("%s rotated: %q, was %q", name, after, before)
		}
	}
	// A byte of the scan changed changes the hash.
	jpeg := readFile(t, "testdata/exiftool/all.jpg")
	changed := append([]byte(nil), jpeg...)
	changed[len(changed)-10] ^= 0x01
	a, _ := imageDataMD5(t, jpeg)
	b, _ := imageDataMD5(t, changed)
	if a == b {
		t.Error("a changed scan byte left the hash")
	}
	if _, err := HashImageData(bytes.NewReader([]byte("plain text, no image")), 20, md5.New()); !errors.Is(err, ErrUnknownFormat) {
		t.Errorf("text: %v, want ErrUnknownFormat", err)
	}
}
