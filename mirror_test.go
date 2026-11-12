package mediameta

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/0x20h/go-media-metadata/internal/bmff/testbuild"
)

func setOrientation(t *testing.T, file []byte, o Orientation) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := SetOrientation(bytes.NewReader(file), int64(len(file)), &out, o); err != nil {
		t.Fatalf("SetOrientation(%d): %v", o, err)
	}
	return out.Bytes()
}

// SetOrientation on every layout and orientation: the orientation reads
// back, the Exif tag (where there is one) follows, every item's data is
// what it was, the related images stay in step, the plan agrees with the
// streamed write, a second identical write changes nothing, and going
// back shows the original orientation again.
func TestSetOrientation(t *testing.T) {
	for _, l := range layouts {
		o := testbuild.Default()
		l.edit(&o)
		orig, data := testbuild.Build(o)
		start := orientationOfFile(t, orig)
		for want := Orientation(1); want <= 8; want++ {
			t.Run(fmt.Sprintf("%s/%d", l.name, want), func(t *testing.T) {
				out := setOrientation(t, orig, want)
				if got := orientationOfFile(t, out); got != want {
					t.Fatalf("orientation %d, want %d", got, want)
				}
				if tag, has, err := exifOrientation(bytes.NewReader(out), int64(len(out))); err != nil {
					t.Fatal(err)
				} else if has && tag != want {
					t.Errorf("Exif Orientation %d, want %d", tag, want)
				}
				for id, d := range data {
					if id == testbuild.Exif {
						continue
					}
					if g := itemData(t, out)[id]; !bytes.Equal(g, d) {
						t.Errorf("item %d's data changed", id)
					}
				}
				relatedInStep(t, orig, out)
				p, err := PlanOrientation(bytes.NewReader(orig), int64(len(orig)), want)
				if err != nil {
					t.Fatal(err)
				}
				checkPlan(t, orig, p)
				var streamed bytes.Buffer
				if _, err := p.WriteTo(&streamed); err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(streamed.Bytes(), out) {
					t.Error("SetOrientation and the plan's WriteTo differ")
				}
				if again := setOrientation(t, out, want); !bytes.Equal(again, out) {
					t.Error("a second identical SetOrientation changed the file")
				}
				if back := setOrientation(t, out, start); orientationOfFile(t, back) != start {
					t.Errorf("back to %d shows %d", start, orientationOfFile(t, back))
				}
			})
		}
	}
}

// A mirrored image rotated is mirrored first, then turned: the Exif
// numbering's order, so 2 turned right is 7 and Degrees() is the turn.
func TestSetRotationKeepsTheMirrorFirst(t *testing.T) {
	for _, l := range layouts {
		o := testbuild.Default()
		l.edit(&o)
		orig, _ := testbuild.Build(o)
		start := orientationOfFile(t, orig)
		if !start.Mirrored() {
			continue
		}
		for deg, want := range map[int]Orientation{0: 2, 90: 7, 180: 4, 270: 5, -90: 5} {
			if got := orientationOfFile(t, setRotation(t, orig, deg)); got != want {
				t.Errorf("%s: mirrored, rotated %d: %d, want %d", l.name, deg, got, want)
			}
		}
	}
	// From 2, a quarter turn right (Degrees 0 → 90) is 7.
	o := testbuild.Default()
	o.Irot, o.Imir = 0, 1 // Apple's orientation 2
	two, _ := testbuild.Build(o)
	if got := orientationOfFile(t, two); got != 2 {
		t.Fatalf("the fixture shows %d, want 2", got)
	}
	if got := orientationOfFile(t, setRotation(t, two, 90)); got != 7 {
		t.Errorf("2 turned right: %d, want 7", got)
	}
}

// Keeping the mirroring (or its absence) is in place where the image has
// an irot of its own; adding or taking away a mirror changes the
// associations, and the size.
func TestPlanOrientationInPlace(t *testing.T) {
	o := testbuild.Default() // an irot only the primary uses
	o.Imir = 0
	mirrored, _ := testbuild.Build(o)
	plain, _ := testbuild.Build(testbuild.Default())
	cases := []struct {
		name    string
		file    []byte
		to      Orientation
		inPlace bool
	}{
		{"mirrored to mirrored", mirrored, 5, true},
		{"mirrored to mirrored, no turn", mirrored, 2, true},
		{"plain to plain", plain, 8, true},
		{"plain to mirrored", plain, 7, false},
		{"mirrored to plain", mirrored, 6, false},
	}
	for _, c := range cases {
		p, err := PlanOrientation(bytes.NewReader(c.file), int64(len(c.file)), c.to)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if p.InPlace != c.inPlace {
			t.Errorf("%s: InPlace %v, want %v", c.name, p.InPlace, c.inPlace)
		}
		checkPlan(t, c.file, p)
	}
}

func TestSetOrientationErrors(t *testing.T) {
	file, _ := testbuild.Build(testbuild.Default())
	for _, o := range []Orientation{0, 9} {
		if _, err := PlanOrientation(bytes.NewReader(file), int64(len(file)), o); err == nil {
			t.Errorf("orientation %d accepted", o)
		}
	}
}

// appleLayouts are the irot (anticlockwise quarter turns) and imir axis
// Apple's ImageIO (macOS 26.6) writes for each orientation: always an
// irot, before the imir in ipma, both essential, with an Exif Orientation
// tag that matches.
var appleLayouts = map[Orientation][2]int{
	1: {0, -1}, 2: {0, 1}, 3: {2, -1}, 4: {0, 0},
	5: {1, 0}, 6: {3, -1}, 7: {1, 1}, 8: {1, -1},
}

// Apple's files read as Apple shows them, from the properties alone: the
// matching Exif tag isn't applied on top of them.
func TestAppleLayoutsRead(t *testing.T) {
	for want, l := range appleLayouts {
		o := testbuild.Default()
		o.Irot, o.Imir, o.ExifOrientation = l[0], l[1], int(want)
		file, _ := testbuild.Build(o)
		if got := orientationOfFile(t, file); got != want {
			t.Errorf("irot %d imir %d (Exif %d): %d, want %d", l[0], l[1], want, got, want)
		}
	}
}

// From an Apple file showing 1 (irot 0), SetOrientation writes what Apple
// writes for each orientation: the same irot angle and imir axis, irot
// first.
func TestSetOrientationWritesApplesLayout(t *testing.T) {
	o := testbuild.Default()
	o.Irot, o.Imir, o.ExifOrientation = 0, -1, 1
	one, _ := testbuild.Build(o)
	for want, l := range appleLayouts {
		out := setOrientation(t, one, want)
		f, err := parseHEIF(bytes.NewReader(out), int64(len(out)))
		if err != nil {
			t.Fatal(err)
		}
		_, entry := f.m.Prop.Entry(f.m.Primary)
		irot, imir, irotAt, imirAt := -1, -1, -1, -1
		for i, ref := range entry.Props {
			if ref.Index == 0 {
				continue
			}
			p := f.m.Prop.Props[ref.Index-1]
			switch p.Type {
			case [4]byte{'i', 'r', 'o', 't'}:
				irot, _ = irotAngle(p)
				irotAt = i
			case [4]byte{'i', 'm', 'i', 'r'}:
				imir, _ = imirAxis(p)
				imirAt = i
			}
		}
		if irot != l[0] || imir != l[1] {
			t.Errorf("orientation %d: irot %d imir %d, Apple writes irot %d imir %d", want, irot, imir, l[0], l[1])
		}
		if imirAt >= 0 && irotAt > imirAt {
			t.Errorf("orientation %d: imir before irot", want)
		}
		if tag, has, _ := exifOrientation(bytes.NewReader(out), int64(len(out))); has && tag != want {
			t.Errorf("orientation %d: Exif %d", want, tag)
		}
	}
}
