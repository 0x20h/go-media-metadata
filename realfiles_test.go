package mediameta

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0x20h/go-media-metadata/internal/bmff"
)

// TestRealFiles runs SetRotation on HEIF files outside the repository:
// set MEDIAMETA_HEIF_DIR to a folder of .heic/.heif/.avif files. Each is
// rotated to every angle; the orientation must read back, the Exif tag
// (where there is one) agree, and every item's data other than the Exif
// item's tag stay byte for byte as it was. No file is written.
func TestRealFiles(t *testing.T) {
	dir := os.Getenv("MEDIAMETA_HEIF_DIR")
	if dir == "" {
		t.Skip("set MEDIAMETA_HEIF_DIR to test real files")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	wantFor := map[int]Orientation{0: 1, 90: 6, 180: 3, 270: 8}
	n := 0
	for _, e := range entries {
		switch strings.ToLower(filepath.Ext(e.Name())) {
		case ".heic", ".heif", ".avif", ".hif":
		default:
			continue
		}
		n++
		t.Run(e.Name(), func(t *testing.T) {
			orig, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			before := itemData(t, orig)
			start := orientationOfFile(t, orig)
			_, hadTag, err := exifOrientation(bytes.NewReader(orig), int64(len(orig)))
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("%d bytes, %d items, orientation %d, Exif tag %v", len(orig), len(before), start, hadTag)
			for _, deg := range []int{0, 90, 180, 270} {
				out := setRotation(t, orig, deg)
				got := orientationOfFile(t, out)
				if !start.Mirrored() && got != wantFor[deg] {
					t.Errorf("%d°: orientation %d, want %d", deg, got, wantFor[deg])
				}
				tag, has, err := exifOrientation(bytes.NewReader(out), int64(len(out)))
				if err != nil {
					t.Fatal(err)
				}
				if has != hadTag || (has && tag != got) {
					t.Errorf("%d°: Exif tag %d (present %v), properties %d", deg, tag, has, got)
				}
				after := itemData(t, out)
				for id, want := range before {
					if d := differing(after[id], want); d != 0 && (d > 2 || !isExif(t, orig, id)) {
						t.Errorf("%d°: item %d changed (%d bytes)", deg, id, d)
					}
				}
				t.Logf("%d°: %d bytes (%+d)", deg, len(out), len(out)-len(orig))
			}
		})
	}
	if n == 0 {
		t.Fatalf("no HEIF files in %s", dir)
	}
}

func isExif(t *testing.T, file []byte, id uint32) bool {
	t.Helper()
	f, err := parseHEIF(bytes.NewReader(file), int64(len(file)))
	if err != nil {
		t.Fatal(err)
	}
	return f.m.ItemTypes[id] == bmff.T("Exif")
}
