package mediameta

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/0x20h/go-media-metadata/internal/bmff"
	"github.com/0x20h/go-media-metadata/internal/bmff/testbuild"
)

// countingReader is an io.ReaderAt that records the ranges read from it.
type countingReader struct {
	r      io.ReaderAt
	mu     sync.Mutex
	calls  int
	bytes  int64
	ranges [][2]int64
}

func (c *countingReader) ReadAt(p []byte, off int64) (int, error) {
	n, err := c.r.ReadAt(p, off)
	c.mu.Lock()
	c.calls++
	c.bytes += int64(n)
	c.ranges = append(c.ranges, [2]int64{off, off + int64(n)})
	c.mu.Unlock()
	return n, err
}

type benchFile struct {
	name string
	data []byte
}

// benchFiles are the sample files plus synthetic layouts that take the
// slower write paths: an irot appended, and a wide ipma.
func benchFiles(tb testing.TB) []benchFile {
	var out []benchFile
	for _, p := range []string{
		"testdata/imageio/a-orientation1.heic",
		"testdata/imageio/b-orientation6.heic",
		"testdata/iphone/grid-gainmap-orientation6.heic",
	} {
		b, err := os.ReadFile(p)
		if err != nil {
			tb.Fatal(err)
		}
		out = append(out, benchFile{filepath.Base(p), b})
	}
	o := testbuild.Default()
	file, _ := testbuild.Build(o)
	out = append(out, benchFile{"synthetic", file})
	o.ExtraProps, o.IpmaWide = 200, true
	file, _ = testbuild.Build(o)
	out = append(out, benchFile{"synthetic-200props", file})
	return out
}

func BenchmarkReadOrientation(b *testing.B) {
	for _, f := range benchFiles(b) {
		b.Run(f.name, func(b *testing.B) {
			cr := &countingReader{r: bytes.NewReader(f.data)}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if _, err := ReadOrientation(cr, int64(len(f.data))); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(cr.bytes)/float64(b.N), "read-B/op")
			b.ReportMetric(float64(cr.calls)/float64(b.N), "readcalls/op")
		})
	}
}

func BenchmarkSetRotation(b *testing.B) {
	for _, f := range benchFiles(b) {
		b.Run(f.name, func(b *testing.B) {
			r := bytes.NewReader(f.data)
			b.ReportAllocs()
			b.SetBytes(int64(len(f.data)))
			b.ResetTimer()
			for range b.N {
				if err := SetRotation(r, int64(len(f.data)), io.Discard, 90); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// maxOrientationRead is what a ReadOrientation may read at most: the
// boxes before the image data, never the image data itself.
const maxOrientationRead = 64 << 10

// ReadOrientation reads only the metadata: a few KB, and nothing inside
// an mdat box.
func TestReadOrientationReadsLittle(t *testing.T) {
	for _, f := range benchFiles(t) {
		cr := &countingReader{r: bytes.NewReader(f.data)}
		if _, err := ReadOrientation(cr, int64(len(f.data))); err != nil {
			t.Fatalf("%s: %v", f.name, err)
		}
		if cr.bytes > maxOrientationRead {
			t.Errorf("%s: read %d bytes, want at most %d", f.name, cr.bytes, maxOrientationRead)
		}
		boxes, err := bmff.ReadBoxes(bytes.NewReader(f.data), 0, int64(len(f.data)))
		if err != nil {
			t.Fatal(err)
		}
		for _, b := range boxes {
			if b.Type != bmff.T("mdat") {
				continue
			}
			for _, rg := range cr.ranges {
				// Reading an mdat's header is fine; its body isn't.
				if rg[0] < b.End() && rg[1] > b.Body() {
					t.Errorf("%s: read [%d, %d) inside mdat [%d, %d)", f.name, rg[0], rg[1], b.Body(), b.End())
				}
			}
		}
		t.Logf("%s (%d bytes): read %d bytes in %d calls", f.name, len(f.data), cr.bytes, cr.calls)
	}
}
