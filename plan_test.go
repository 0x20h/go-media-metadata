package mediameta

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/0x20h/go-media-metadata/internal/bmff/testbuild"
)

// fileAt is an io.WriterAt over a fixed-size file in memory that refuses
// writes outside it.
type fileAt []byte

func (f fileAt) WriteAt(p []byte, off int64) (int, error) {
	if off < 0 || off+int64(len(p)) > int64(len(f)) {
		return 0, fmt.Errorf("write [%d, %d) outside the file of %d bytes", off, off+int64(len(p)), len(f))
	}
	return copy(f[off:], p), nil
}

// checkPlan checks a plan against its streamed result: InPlace exactly when
// the size stays, and then the patches are sorted, disjoint, inside the
// file, cover every changed byte, and applied to a copy give the streamed
// result byte for byte.
func checkPlan(t *testing.T, src []byte, p *Plan) {
	t.Helper()
	var stream bytes.Buffer
	n, err := p.WriteTo(&stream)
	if err != nil {
		t.Fatalf("WriteTo: %v", err)
	}
	if n != int64(stream.Len()) || n != p.NewSize {
		t.Fatalf("WriteTo wrote %d bytes (%d buffered), NewSize %d", n, stream.Len(), p.NewSize)
	}
	if p.InPlace != (p.NewSize == p.Size) {
		t.Fatalf("InPlace %v with size %d → %d", p.InPlace, p.Size, p.NewSize)
	}
	if !p.InPlace {
		if p.Patches != nil {
			t.Fatal("patches on a plan that isn't in place")
		}
		if err := p.Apply(fileAt(bytes.Clone(src))); !errors.Is(err, ErrNotInPlace) {
			t.Fatalf("Apply: %v, want ErrNotInPlace", err)
		}
		return
	}
	covered := make([]bool, len(src))
	end := int64(-1)
	for _, pt := range p.Patches {
		if pt.Offset <= end {
			t.Fatalf("patch at %d overlaps or isn't sorted after one ending at %d", pt.Offset, end)
		}
		if len(pt.Data) == 0 || pt.Offset < 0 || pt.Offset+int64(len(pt.Data)) > p.Size {
			t.Fatalf("patch [%d, +%d) in a file of %d bytes", pt.Offset, len(pt.Data), p.Size)
		}
		for i := range pt.Data {
			covered[pt.Offset+int64(i)] = true
		}
		end = pt.Offset + int64(len(pt.Data)) - 1
	}
	out := stream.Bytes()
	for i := range src {
		if src[i] != out[i] && !covered[i] {
			t.Fatalf("byte %d changed outside the patches", i)
		}
	}
	applied := fileAt(bytes.Clone(src))
	if err := p.Apply(applied); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !bytes.Equal(applied, out) {
		t.Fatalf("Apply and WriteTo differ in %d bytes", differing(applied, out))
	}
}

func TestPlanRotation(t *testing.T) {
	type sample struct {
		name string
		data []byte
	}
	var samples []sample
	for _, l := range layouts {
		o := testbuild.Default()
		l.edit(&o)
		file, _ := testbuild.Build(o)
		samples = append(samples, sample{l.name, file})
	}
	for _, f := range benchFiles(t) {
		samples = append(samples, sample{f.name, f.data})
	}
	inPlace := 0
	for _, s := range samples {
		for _, deg := range []int{0, 90, 180, 270} {
			t.Run(fmt.Sprintf("%s/%d", s.name, deg), func(t *testing.T) {
				p, err := PlanRotation(bytes.NewReader(s.data), int64(len(s.data)), deg)
				if err != nil {
					t.Fatal(err)
				}
				checkPlan(t, s.data, p)
				// SetRotation is the same edit.
				var set bytes.Buffer
				if err := SetRotation(bytes.NewReader(s.data), int64(len(s.data)), &set, deg); err != nil {
					t.Fatal(err)
				}
				var stream bytes.Buffer
				if _, err := p.WriteTo(&stream); err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(set.Bytes(), stream.Bytes()) {
					t.Fatal("SetRotation and the plan's WriteTo differ")
				}
				if p.InPlace {
					inPlace++
				}
			})
		}
	}
	if inPlace == 0 {
		t.Fatal("no plan was in place")
	}
}

// Camera originals and ImageIO files rotate in place: the irot byte and
// the Exif Orientation tag.
func TestPlanRotationInPlaceSamples(t *testing.T) {
	for _, path := range []string{
		"testdata/imageio/b-orientation6.heic",
		"testdata/iphone/grid-gainmap-orientation6.heic",
	} {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		p, err := PlanRotation(bytes.NewReader(src), int64(len(src)), 180)
		if err != nil {
			t.Fatal(err)
		}
		if !p.InPlace {
			t.Fatalf("%s: not in place", path)
		}
		n := 0
		for _, pt := range p.Patches {
			n += len(pt.Data)
		}
		if n > 3 {
			t.Errorf("%s: %d patches of %d bytes in all, want at most 3 bytes", path, len(p.Patches), n)
		}
		checkPlan(t, src, p)
	}
}

func FuzzPlanRotation(f *testing.F) {
	seeds(f)
	f.Fuzz(func(t *testing.T, data []byte) {
		withinDeadline(t, func() {
			for _, deg := range []int{0, 90, 180} {
				p, err := PlanRotation(bytes.NewReader(data), int64(len(data)), deg)
				if err != nil {
					continue
				}
				checkPlan(t, data, p)
			}
		})
	})
}

func BenchmarkPlanRotation(b *testing.B) {
	for _, f := range benchFiles(b) {
		b.Run(f.name, func(b *testing.B) {
			cr := &countingReader{r: bytes.NewReader(f.data)}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if _, err := PlanRotation(cr, int64(len(f.data)), 90); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(cr.bytes)/float64(b.N), "read-B/op")
		})
	}
}

func BenchmarkApply(b *testing.B) {
	for _, f := range benchFiles(b) {
		p, err := PlanRotation(bytes.NewReader(f.data), int64(len(f.data)), 180)
		if err != nil {
			b.Fatal(err)
		}
		if !p.InPlace {
			continue
		}
		b.Run(f.name, func(b *testing.B) {
			dst := fileAt(bytes.Clone(f.data))
			b.ReportAllocs()
			for range b.N {
				if err := p.Apply(dst); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
