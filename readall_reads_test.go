package mediameta

import (
	"bytes"
	"path/filepath"
	"testing"
)

// rangeLog records the ranges read.
type rangeLog struct {
	r      *bytes.Reader
	ranges [][2]int64
}

func (l *rangeLog) ReadAt(p []byte, off int64) (int, error) {
	l.ranges = append(l.ranges, [2]int64{off, int64(len(p))})
	return l.r.ReadAt(p, off)
}

// ReadAll reads no range of a HEIF file twice: the meta box parseHEIF
// read is the one the Exif item is found in.
func TestReadAllReadsOnce(t *testing.T) {
	paths, _ := filepath.Glob("testdata/iphone/*.heic")
	more, _ := filepath.Glob("testdata/imageio/*.heic")
	for _, p := range append(paths, more...) {
		data := readFile(t, p)
		l := &rangeLog{r: bytes.NewReader(data)}
		if _, err := ReadAll(l, int64(len(data))); err != nil {
			t.Fatal(err)
		}
		seen := map[[2]int64]bool{}
		for _, rg := range l.ranges {
			if seen[rg] {
				t.Errorf("%s: %d+%d read twice", p, rg[0], rg[1])
			}
			seen[rg] = true
		}
	}
}
