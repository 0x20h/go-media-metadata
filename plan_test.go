package mediameta

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/0x20h/go-media-metadata/internal/bmff/testbuild"
)

// bufferAt is an io.WriterAt over a byte slice of fixed size.
type bufferAt struct {
	b      []byte
	writes int
}

func (w *bufferAt) WriteAt(p []byte, off int64) (int, error) {
	if off < 0 || off+int64(len(p)) > int64(len(w.b)) {
		return 0, fmt.Errorf("write [%d, %d) outside [0, %d)", off, off+int64(len(p)), len(w.b))
	}
	w.writes++
	return copy(w.b[off:], p), nil
}

// planFiles are the test layouts and the sample files.
func planFiles(t testing.TB) []benchFile {
	var out []benchFile
	for _, l := range layouts {
		o := testbuild.Default()
		l.edit(&o)
		file, _ := testbuild.Build(o)
		out = append(out, benchFile{l.name, file})
	}
	return append(out, benchFiles(t)...)
}

// checkPlan checks a plan of file against what WriteTo makes: the patches
// are sorted, don't overlap and lie in the file; InPlace exactly when the
// size stays; and when InPlace, Apply on a copy gives byte for byte what
// WriteTo gives, every changed byte inside a patch. Otherwise Apply writes
// nothing and says ErrNotInPlace.
func checkPlan(t *testing.T, file []byte, p *Plan) {
	t.Helper()
	var out bytes.Buffer
	n, err := p.WriteTo(&out)
	if err != nil {
		t.Fatalf("WriteTo: %v", err)
	}
	if n != int64(out.Len()) || n != p.NewSize {
		t.Fatalf("WriteTo wrote %d bytes (said %d), NewSize %d", out.Len(), n, p.NewSize)
	}
	if p.Size != int64(len(file)) {
		t.Fatalf("Size %d, the file has %d", p.Size, len(file))
	}
	if p.InPlace != (p.NewSize == p.Size) {
		t.Fatalf("InPlace %v with Size %d, NewSize %d", p.InPlace, p.Size, p.NewSize)
	}
	end := int64(-1)
	covered := make([]bool, len(file))
	for _, pt := range p.Patches {
		if len(pt.Data) == 0 {
			t.Errorf("an empty patch at %d", pt.Offset)
		}
		if pt.Offset <= end {
			t.Errorf("patch at %d: not after the previous one, which ends at %d", pt.Offset, end)
		}
		end = pt.Offset + int64(len(pt.Data)) - 1
		if pt.Offset < 0 || end >= p.Size {
			t.Fatalf("patch [%d, %d] outside [0, %d)", pt.Offset, end, p.Size)
		}
		for i := pt.Offset; i <= end; i++ {
			covered[i] = true
		}
	}
	dst := &bufferAt{b: bytes.Clone(file)}
	err = p.Apply(dst)
	if !p.InPlace {
		if !errors.Is(err, ErrNotInPlace) || dst.writes != 0 {
			t.Fatalf("Apply of a plan not in place: %v, %d writes", err, dst.writes)
		}
		if len(p.Patches) != 0 {
			t.Errorf("%d patches on a plan not in place", len(p.Patches))
		}
		return
	}
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !bytes.Equal(dst.b, out.Bytes()) {
		t.Fatalf("Apply and WriteTo differ in %d bytes", differing(dst.b, out.Bytes()))
	}
	for i := range file {
		if file[i] != out.Bytes()[i] && !covered[i] {
			t.Fatalf("byte %d changed outside the patches", i)
		}
	}
}

// For every layout, sample file and angle, the plan and the streaming
// write agree.
func TestPlanRotation(t *testing.T) {
	for _, f := range planFiles(t) {
		for _, deg := range []int{0, 90, 180, 270, -90} {
			t.Run(fmt.Sprintf("%s/%d", f.name, deg), func(t *testing.T) {
				p, err := PlanRotation(bytes.NewReader(f.data), int64(len(f.data)), deg)
				if err != nil {
					t.Fatal(err)
				}
				checkPlan(t, f.data, p)
				// SetRotation is the plan, streamed.
				var a, b bytes.Buffer
				if _, err := p.WriteTo(&a); err != nil {
					t.Fatal(err)
				}
				if err := SetRotation(bytes.NewReader(f.data), int64(len(f.data)), &b, deg); err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(a.Bytes(), b.Bytes()) {
					t.Error("SetRotation and the plan's WriteTo differ")
				}
			})
		}
	}
}

// The usual case is in place and small: the irot byte and the two Exif
// Orientation bytes, whatever the file's size.
func TestPlanRotationInPlace(t *testing.T) {
	for _, path := range []string{
		"testdata/imageio/b-orientation6.heic",
		"testdata/iphone/grid-gainmap-orientation6.heic",
	} {
		file, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		p, err := PlanRotation(bytes.NewReader(file), int64(len(file)), 180)
		if err != nil {
			t.Fatal(err)
		}
		if !p.InPlace {
			t.Fatalf("%s: not in place (%d → %d bytes)", path, p.Size, p.NewSize)
		}
		n := 0
		for _, pt := range p.Patches {
			n += len(pt.Data)
		}
		if n == 0 || n > 3 {
			t.Errorf("%s: %d patched bytes in %d patches, want 1 to 3", path, n, len(p.Patches))
		}
		t.Logf("%s: %d bytes in %d patches", path, n, len(p.Patches))
	}
	// No change: in place, nothing to write.
	file, _ := testbuild.Build(testbuild.Default())
	o := orientationOfFile(t, file)
	p, err := PlanRotation(bytes.NewReader(file), int64(len(file)), o.Degrees())
	if err != nil {
		t.Fatal(err)
	}
	if !p.InPlace || len(p.Patches) != 0 {
		t.Errorf("rotating to the current angle: InPlace %v, %d patches", p.InPlace, len(p.Patches))
	}
}

// An edit that grows the meta box isn't in place: Apply refuses it and
// writes nothing.
func TestPlanNotInPlace(t *testing.T) {
	o := testbuild.Default()
	o.Irot = -1
	file, _ := testbuild.Build(o)
	p, err := PlanRotation(bytes.NewReader(file), int64(len(file)), 90)
	if err != nil {
		t.Fatal(err)
	}
	if p.InPlace || p.NewSize == p.Size {
		t.Fatalf("appending an irot: InPlace %v, %d → %d bytes", p.InPlace, p.Size, p.NewSize)
	}
	checkPlan(t, file, p)
	if _, err := PlanRotation(bytes.NewReader(file), int64(len(file)), 45); err == nil {
		t.Error("45° accepted")
	}
}

// A plan reads the boxes before the image data and the Exif item's head,
// never the whole file.
func TestPlanRotationReadsLittle(t *testing.T) {
	for _, f := range benchFiles(t) {
		cr := &countingReader{r: bytes.NewReader(f.data)}
		if _, err := PlanRotation(cr, int64(len(f.data)), 90); err != nil {
			t.Fatalf("%s: %v", f.name, err)
		}
		if cr.bytes > maxOrientationRead+maxExifHead {
			t.Errorf("%s: read %d bytes, want at most %d", f.name, cr.bytes, maxOrientationRead+maxExifHead)
		}
		t.Logf("%s (%d bytes): read %d bytes in %d calls", f.name, len(f.data), cr.bytes, cr.calls)
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
			// Metadata items: replaced or added, fitting or not.
			for _, e := range [][2][]byte{{photoTIFF(binary.LittleEndian), nil}, {nil, xmpPacket("x", 0)}, {nil, xmpPacket("y", 3000)}} {
				var p *Plan
				var err error
				if e[0] != nil {
					p, err = PlanExif(bytes.NewReader(data), int64(len(data)), e[0])
				} else {
					p, err = PlanXMP(bytes.NewReader(data), int64(len(data)), e[1])
				}
				if err != nil {
					continue
				}
				checkPlan(t, data, p)
			}
			// Exif edits through EditExif.
			for _, e := range []ExifEdit{{RemoveGPS: true}, {GPS: &GPS{Latitude: 1, Longitude: 2}}} {
				if p, err := PlanExifEdit(bytes.NewReader(data), int64(len(data)), e); err == nil {
					checkPlan(t, data, p)
				}
			}
			// Orientation edits: adding, keeping and taking away a mirror.
			for _, o := range []Orientation{1, 2, 7} {
				p, err := PlanOrientation(bytes.NewReader(data), int64(len(data)), o)
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
		// 180: a change for every sample, which shows 1 or 6.
		p, err := PlanRotation(bytes.NewReader(f.data), int64(len(f.data)), 180)
		if err != nil {
			b.Fatal(err)
		}
		if !p.InPlace {
			continue
		}
		b.Run(f.name, func(b *testing.B) {
			dst := &bufferAt{b: bytes.Clone(f.data)}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if err := p.Apply(dst); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
