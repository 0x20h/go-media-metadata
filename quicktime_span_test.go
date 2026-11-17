package mediameta

import (
	"bytes"
	"testing"
)

// A spanReader reads what the file holds: inside the span from its
// buffer, across its edges and outside it from the file; past
// maxMoovRead there's none, and the file is read as before.
func TestSpanReader(t *testing.T) {
	file := make([]byte, 4096)
	for i := range file {
		file[i] = byte(i * 7)
	}
	src := bytes.NewReader(file)
	s := readSpan(src, 1000, 500)
	if s == nil {
		t.Fatal("no span")
	}
	defer s.release()
	for _, c := range [][2]int64{{1000, 500}, {1000, 1}, {1499, 1}, {1200, 100}, {990, 20}, {1490, 20}, {0, 16}, {3000, 1096}, {1500, 8}} {
		got := make([]byte, c[1])
		n, err := s.ReadAt(got, c[0])
		if err != nil || n != len(got) || !bytes.Equal(got, file[c[0]:c[0]+c[1]]) {
			t.Errorf("%d+%d: %d, %v", c[0], c[1], n, err)
		}
	}
	if readSpan(src, 0, maxMoovRead+1) != nil {
		t.Error("a span past maxMoovRead")
	}
	if readSpan(src, 4000, 200) != nil {
		t.Error("a span past the end of the file")
	}
}
