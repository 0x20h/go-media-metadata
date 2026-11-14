package mediameta

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/0x20h/go-media-metadata/internal/bmff/qtbuild"
	"github.com/0x20h/go-media-metadata/internal/bmff/testbuild"
)

// One file of each format the orientation functions take, and what they
// say for the ones they don't.
func dispatchFiles(t testing.TB) map[string][]byte {
	heif, _ := testbuild.Build(testbuild.Default())
	movie, _, _ := qtbuild.Build(qtbuild.Default())
	files := map[string][]byte{
		"HEIF":  heif,
		"JPEG":  plainJPEG(t),
		"RAW":   rawTIFFFile(binary.LittleEndian, typeShort),
		"movie": movie,
	}
	for name, f := range chunkedFiles(t) {
		files[name] = f
	}
	return files
}

// Read, Set and Plan Orientation and Rotation work on every format, with
// the same results.
func TestOrientationEveryFormat(t *testing.T) {
	for name, file := range dispatchFiles(t) {
		for o := Orientation(1); o <= 8; o++ {
			p, err := PlanOrientation(bytes.NewReader(file), int64(len(file)), o)
			if err != nil {
				t.Fatalf("%s: PlanOrientation(%d): %v", name, o, err)
			}
			checkPlan(t, file, p)
			var planned, set bytes.Buffer
			if _, err := p.WriteTo(&planned); err != nil {
				t.Fatal(err)
			}
			if err := SetOrientation(bytes.NewReader(file), int64(len(file)), &set, o); err != nil {
				t.Fatalf("%s: SetOrientation(%d): %v", name, o, err)
			}
			if !bytes.Equal(planned.Bytes(), set.Bytes()) {
				t.Errorf("%s: SetOrientation(%d) and PlanOrientation differ", name, o)
			}
			got, err := ReadOrientation(bytes.NewReader(set.Bytes()), int64(set.Len()))
			if err != nil || got != o {
				t.Errorf("%s: set %d, reads %d (%v)", name, o, got, err)
			}
		}
		// A rotation keeps a mirroring: 2 turned 90° is 7.
		var mirrored bytes.Buffer
		if err := SetOrientation(bytes.NewReader(file), int64(len(file)), &mirrored, 2); err != nil {
			t.Fatal(err)
		}
		m := mirrored.Bytes()
		var turned bytes.Buffer
		if err := SetRotation(bytes.NewReader(m), int64(len(m)), &turned, 90); err != nil {
			t.Fatalf("%s: SetRotation: %v", name, err)
		}
		if got, err := ReadOrientation(bytes.NewReader(turned.Bytes()), int64(turned.Len())); err != nil || got != 7 {
			t.Errorf("%s: mirrored, turned 90°: %d (%v), want 7", name, got, err)
		}
		if p, err := PlanRotation(bytes.NewReader(m), int64(len(m)), 90); err != nil || !bytes.Equal(written(t, p), turned.Bytes()) {
			t.Errorf("%s: PlanRotation and SetRotation differ (%v)", name, err)
		}
	}
}

// Formats whose orientation isn't written, and files of no format.
func TestOrientationRefusals(t *testing.T) {
	jxl, _ := buildJXL(t, jxlLayouts[0], 6)
	if _, err := PlanOrientation(bytes.NewReader(jxl), int64(len(jxl)), 3); !errors.Is(err, ErrUnsupported) {
		t.Errorf("JPEG XL: %v, want ErrUnsupported", err)
	}
	if o, err := ReadOrientation(bytes.NewReader(jxl), int64(len(jxl))); err != nil || o != 6 {
		t.Errorf("JPEG XL read: %v, %v", o, err)
	}
	raf := rawSamples(t)[4].file // RAF
	if _, err := PlanRotation(bytes.NewReader(raf), int64(len(raf)), 90); !errors.Is(err, ErrUnsupported) {
		t.Errorf("RAF: %v, want ErrUnsupported", err)
	}
	gif := []byte("GIF89a\x01\x00\x01\x00\x00\x00\x00")
	for name, err := range map[string]error{
		"ReadOrientation": func() error { _, err := ReadOrientation(bytes.NewReader(gif), int64(len(gif))); return err }(),
		"PlanOrientation": func() error { _, err := PlanOrientation(bytes.NewReader(gif), int64(len(gif)), 1); return err }(),
		"PlanRotation":    func() error { _, err := PlanRotation(bytes.NewReader(gif), int64(len(gif)), 90); return err }(),
		"ReadExif":        func() error { _, err := ReadExif(bytes.NewReader(gif), int64(len(gif))); return err }(),
		"ReadVideo":       func() error { _, err := ReadVideo(bytes.NewReader(gif), int64(len(gif))); return err }(),
		"ReadPreviews":    func() error { _, err := ReadPreviews(bytes.NewReader(gif), int64(len(gif))); return err }(),
		"ReadEmbedded":    func() error { _, err := ReadEmbedded(bytes.NewReader(gif), int64(len(gif))); return err }(),
	} {
		if !errors.Is(err, ErrUnknownFormat) {
			t.Errorf("%s(GIF): %v, want ErrUnknownFormat", name, err)
		}
	}
}

// Every orientation entry point, on files of every format and their
// mutations: the format is told apart (ftyp brands, TIFF and RAF magic,
// JPEG XL), and whatever it routes to keeps the plan's guarantees.
func FuzzOrientationAnyFormat(f *testing.F) {
	for _, file := range dispatchFiles(f) {
		f.Add(file)
	}
	for _, s := range rawSamples(f) { // CR2, NEF, ARW, RAF, RW2, CR3, maker notes
		f.Add(s.file)
	}
	jxl, _ := buildJXL(f, jxlLayouts[0], 6)
	f.Add(jxl)
	f.Add(buildMotionPhoto(f, motionOpts{}).file)
	sentinel := func(err error) bool {
		return errors.Is(err, ErrUnknownFormat) || errors.Is(err, ErrMalformed) || errors.Is(err, ErrUnsupported) ||
			errors.Is(err, ErrOrientationInExif)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		withinDeadline(t, func() {
			r := func() *bytes.Reader { return bytes.NewReader(data) }
			n := int64(len(data))
			_, readErr := ReadOrientation(r(), n)
			if readErr != nil && !sentinel(readErr) {
				t.Fatalf("ReadOrientation: an error that is none of the sentinels: %v", readErr)
			}
			for _, o := range []Orientation{1, 6, 7} {
				p, err := PlanOrientation(r(), n, o)
				if err != nil {
					if !sentinel(err) {
						t.Fatalf("PlanOrientation(%d): an error that is none of the sentinels: %v", o, err)
					}
					continue
				}
				checkPlan(t, data, p)
				out := written(t, p)
				var set bytes.Buffer
				if err := SetOrientation(r(), n, &set, o); err != nil || !bytes.Equal(set.Bytes(), out) {
					t.Fatalf("SetOrientation(%d) differs from the plan (%v)", o, err)
				}
				if readErr == nil {
					if got, err := ReadOrientation(bytes.NewReader(out), int64(len(out))); err != nil || got != o {
						t.Fatalf("set %d, reads %d (%v)", o, got, err)
					}
				}
			}
			p, err := PlanRotation(r(), n, 90)
			if err != nil {
				if !sentinel(err) {
					t.Fatalf("PlanRotation: an error that is none of the sentinels: %v", err)
				}
				return
			}
			checkPlan(t, data, p)
			out := written(t, p)
			var set bytes.Buffer
			if err := SetRotation(r(), n, &set, 90); err != nil || !bytes.Equal(set.Bytes(), out) {
				t.Fatalf("SetRotation differs from the plan (%v)", err)
			}
			if readErr == nil {
				if got, err := ReadOrientation(bytes.NewReader(out), int64(len(out))); err != nil || got.Degrees() != 90 {
					t.Fatalf("rotated 90°, reads %v (%v)", got, err)
				}
			}
		})
	})
}
