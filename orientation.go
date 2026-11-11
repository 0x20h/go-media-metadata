package mediameta

import "fmt"

// Orientation is how an image is shown relative to how it's stored, in
// the numbering of the Exif Orientation tag (CIPA DC-008):
//
//	1 as stored                5 mirrored, then rotated 270° clockwise
//	2 mirrored left-right      6 rotated 90° clockwise
//	3 rotated 180°             7 mirrored, then rotated 90° clockwise
//	4 mirrored top-bottom      8 rotated 270° clockwise
type Orientation uint8

// Valid reports whether o is one of 1 to 8.
func (o Orientation) Valid() bool { return o >= 1 && o <= 8 }

// Mirrored reports whether the image is mirrored left-right before it's
// rotated by Degrees.
func (o Orientation) Mirrored() bool {
	switch o {
	case 2, 4, 5, 7:
		return true
	}
	return false
}

// Degrees is the clockwise rotation, 0, 90, 180 or 270, applied after the
// mirroring (if any).
func (o Orientation) Degrees() int {
	switch o {
	case 3, 4:
		return 180
	case 6, 7:
		return 90
	case 5, 8:
		return 270
	}
	return 0
}

func (o Orientation) String() string {
	if !o.Valid() {
		return fmt.Sprintf("Orientation(%d)", uint8(o))
	}
	if o.Mirrored() {
		return fmt.Sprintf("mirrored, rotated %d° clockwise", o.Degrees())
	}
	return fmt.Sprintf("rotated %d° clockwise", o.Degrees())
}

// transform is a 2×2 integer matrix acting on (x, y) with y pointing up:
// the eight orientations are its rotations and reflections.
type transform [4]int

var identity = transform{1, 0, 0, 1}

func (a transform) then(b transform) transform {
	// b · a: a applied first.
	return transform{
		b[0]*a[0] + b[1]*a[2], b[0]*a[1] + b[1]*a[3],
		b[2]*a[0] + b[3]*a[2], b[2]*a[1] + b[3]*a[3],
	}
}

// rotationCW is a clockwise rotation by quarter turns.
func rotationCW(quarters int) transform {
	t := identity
	for range ((quarters % 4) + 4) % 4 {
		t = t.then(transform{0, 1, -1, 0})
	}
	return t
}

var (
	mirrorLeftRight = transform{-1, 0, 0, 1}
	mirrorTopBottom = transform{1, 0, 0, -1}
)

// asTransform is o as a matrix: mirror first, then rotate.
func (o Orientation) asTransform() transform {
	t := identity
	if o.Mirrored() {
		t = mirrorLeftRight
	}
	return t.then(rotationCW(o.Degrees() / 90))
}

// orientationOf is the orientation a transform shows.
func orientationOf(t transform) Orientation {
	for o := Orientation(1); o <= 8; o++ {
		if o.asTransform() == t {
			return o
		}
	}
	return 1 // unreachable: the eight cover every product of the generators
}
