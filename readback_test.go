package mediameta

import (
	"bytes"
	"encoding/binary"
	"io"
	"path/filepath"
	"testing"
)

// heifPlanReader serves what WriteTo writes, at any offset and length.
func TestHEIFPlanReader(t *testing.T) {
	paths, _ := filepath.Glob("testdata/iphone/*.heic")
	if len(paths) == 0 {
		t.Fatal("no HEIC test files")
	}
	exif, short, long := photoTIFF(binary.LittleEndian), xmpPacket("x", 0), xmpPacket("y", 3000)
	for _, path := range paths {
		file := readFile(t, path)
		for _, plan := range []func() (*Plan, error){
			func() (*Plan, error) { return PlanExif(bytes.NewReader(file), int64(len(file)), exif) },
			func() (*Plan, error) { return PlanXMP(bytes.NewReader(file), int64(len(file)), short) },
			func() (*Plan, error) { return PlanXMP(bytes.NewReader(file), int64(len(file)), long) },
		} {
			p, err := plan()
			if err != nil {
				t.Fatalf("%s: %v", path, err)
			}
			want := written(t, p)
			r, n := heifPlanReader(p)
			if n != int64(len(want)) {
				t.Fatalf("%s: %d bytes, want %d", path, n, len(want))
			}
			got := make([]byte, n)
			if _, err := r.ReadAt(got, 0); err != nil && err != io.EOF {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("%s: the reader differs from WriteTo", path)
			}
			// Pieces across the parts' edges, and past the end.
			for _, at := range []int64{0, p.f.meta.Offset - 3, p.f.meta.Offset + 5, int64(len(p.meta)) + p.f.meta.Offset - 2, n - 7} {
				if at < 0 {
					continue
				}
				b := make([]byte, 16)
				k, _ := r.ReadAt(b, at)
				if !bytes.Equal(b[:k], want[at:min(at+16, n)]) {
					t.Fatalf("%s: at %d", path, at)
				}
			}
		}
	}
}
