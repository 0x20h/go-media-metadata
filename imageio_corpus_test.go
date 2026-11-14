package mediameta

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The ImageIO corpus (testdata/imageio-corpus): JPEG and HEIC files whose
// metadata Apple's ImageIO wrote, and the values it wrote
// (expected.json). See its README for how ImageIO lays them out.

const imageIOCorpus = "testdata/imageio-corpus"

func imageIOFiles(t testing.TB) map[string][]byte {
	t.Helper()
	files := map[string][]byte{}
	for _, pat := range []string{"*.jpg", "*.heic"} {
		paths, _ := filepath.Glob(filepath.Join(imageIOCorpus, pat))
		for _, p := range paths {
			b, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			files[filepath.Base(p)] = b
		}
	}
	if len(files) == 0 {
		t.Fatal("no ImageIO corpus files")
	}
	return files
}

func imageIOExpected(t *testing.T) map[string]corpusWant {
	t.Helper()
	var want map[string]corpusWant
	if err := json.Unmarshal(readFile(t, filepath.Join(imageIOCorpus, "expected.json")), &want); err != nil {
		t.Fatal(err)
	}
	return want
}

func TestImageIOCorpusReads(t *testing.T) {
	files := imageIOFiles(t)
	want := imageIOExpected(t)
	if len(files) != len(want) {
		t.Errorf("%d files, %d expected", len(files), len(want))
	}
	for name, w := range want {
		file, ok := files[name]
		if !ok {
			t.Errorf("%s: missing", name)
			continue
		}
		checkCorpusFile(t, name, file, w)
	}
}

// imageData is what a write must leave as it is: a JPEG's scan data, a
// HEIF file's image items.
func imageDataOf(t *testing.T, file []byte) [][]byte {
	t.Helper()
	if isJPEG(bytes.NewReader(file), int64(len(file))) {
		return [][]byte{file[bytes.Index(file, []byte{0xFF, markerSOS}):]}
	}
	f, err := parseHEIF(bytes.NewReader(file), int64(len(file)))
	if err != nil {
		t.Fatal(err)
	}
	var out [][]byte
	for id, d := range itemData(t, file) {
		typ := f.m.ItemTypes[id]
		switch string(typ[:]) {
		case "Exif", "mime":
		default:
			out = append(out, d)
		}
	}
	return out
}

func sameImageData(a, b [][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]int{}
	for _, d := range a {
		seen[string(d)]++
	}
	for _, d := range b {
		seen[string(d)]--
	}
	for _, n := range seen {
		if n != 0 {
			return false
		}
	}
	return true
}

// Each writer, on each ImageIO file: the change reads back, everything else
// reads as ImageIO wrote it, and the image data is untouched. Where
// exiftool is installed, it validates every result with no warning its
// source didn't have.
func TestImageIOCorpusWriteBack(t *testing.T) {
	files := imageIOFiles(t)
	dir := t.TempDir()
	written := map[string]string{}
	for name, file := range files {
		for edit, apply := range corpusEdits {
			if edit == "iptc keywords" && !strings.HasSuffix(name, ".jpg") {
				continue // IPTC-IIM is JPEG only
			}
			w := imageIOExpected(t)[name]
			out := apply(t, file, &w)
			if !sameImageData(imageDataOf(t, file), imageDataOf(t, out)) {
				t.Errorf("%s after %s: the image data changed", name, edit)
			}
			checkCorpusFile(t, name+" after "+edit, out, w)
			ext := filepath.Ext(name)
			fn := strings.TrimSuffix(name, ext) + "--" + strings.ReplaceAll(edit, " ", "-") + ext
			if err := os.WriteFile(filepath.Join(dir, fn), out, 0o644); err != nil {
				t.Fatal(err)
			}
			written[fn] = name
		}
		if err := os.WriteFile(filepath.Join(dir, name), file, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	tool, err := exec.LookPath("exiftool")
	if err != nil {
		return
	}
	// Every warning ExifTool's validation gives a written file it gave
	// its source too: a write may take a warning away (an IPTC block
	// ImageIO put where it doesn't belong), never add one.
	warnings := func(fn string) map[string]bool {
		out, err := exec.Command(tool, "-validate", "-warning", "-a", "-s3", filepath.Join(dir, fn)).Output()
		if err != nil {
			t.Fatal(err)
		}
		set := map[string]bool{}
		for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if l != "" && !strings.Contains(l, "Warnings") && !strings.HasPrefix(l, "OK") {
				set[l] = true
			}
		}
		return set
	}
	for fn, src := range written {
		before := warnings(src)
		for w := range warnings(fn) {
			if !before[w] {
				t.Errorf("%s: ExifTool warns %q, which its source didn't", fn, w)
			}
		}
	}
}

// The corpus files are seeds for the fuzz targets that read and write
// JPEG and HEIF.
func imageIOSeeds(f *testing.F) {
	for _, b := range imageIOFiles(f) {
		f.Add(b)
	}
}
