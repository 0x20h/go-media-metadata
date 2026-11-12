package mediameta

import (
	"fmt"
	"io"

	"github.com/0x20h/go-media-metadata/internal/bmff"
)

// SetOrientation writes to dst the HEIF file in src (size bytes) with its
// primary image shown at o, any of the eight Exif orientations: its irot
// and imir properties are set to show it. An imir it already has is kept
// for a mirrored o (the rotation is chosen for its axis) and dropped for
// one that isn't. A mirror it needs has the axis Apple's ImageIO writes
// for o (1, left-right, for 2 and 7; 0, top-bottom, for 4 and 5): an imir
// of that axis the file has, else a new one. The properties apply in the
// order ISO/IEC 23008-12 associates them, irot before imir (6.5.10,
// 6.5.12): a new irot goes before the item's imir, a new imir after its
// irot. imir's axis is read as Apple and libheif read it (see imirAxis).
//
// The images that belong to it turn with it, and the Exif Orientation tag
// follows, as for SetRotation. On an error nothing useful has been written
// to dst.
func SetOrientation(src io.ReaderAt, size int64, dst io.Writer, o Orientation) error {
	r, err := planOrientation(src, size, o)
	if err != nil {
		return err
	}
	_, err = r.f.write(src, size, dst, r.meta, r.delta, r.patches)
	return err
}

// orientationFor is the orientation mirrored or not, then rotated degrees
// clockwise (a multiple of 90).
func orientationFor(mirrored bool, degrees int) Orientation {
	d := ((degrees % 360) + 360) % 360
	for o := Orientation(1); o <= 8; o++ {
		if o.Mirrored() == mirrored && o.Degrees() == d {
			return o
		}
	}
	return 1 // unreachable for a multiple of 90
}

// setOrientation shows the primary image at o, and with it each related
// image (thumbnail, gain map) that showed the primary's orientation, so
// they stay in step: first each one's mirror, then its rotation.
func (f *heifFile) setOrientation(o Orientation) error {
	if _, entry := f.m.Prop.Entry(f.m.Primary); entry == nil {
		return fmt.Errorf("%w: the primary item has no properties", ErrUnsupported)
	}
	before, err := f.orientation()
	if err != nil {
		return err
	}
	targets := []uint32{f.m.Primary}
	for _, id := range f.related() {
		if _, entry := f.m.Prop.Entry(id); entry == nil {
			continue
		}
		if ro, err := f.itemOrientation(id); err == nil && ro == before {
			targets = append(targets, id)
		}
	}
	if err := f.setMirror(targets, o); err != nil {
		return err
	}
	// The rotation that, with each one's mirror where it stands, shows o.
	quarters := -1
	same := true
	var buf [8]int
	each := buf[:0]
	for _, id := range targets {
		q, err := f.quartersFor(id, o)
		if err != nil {
			return err
		}
		each = append(each, q)
		if quarters >= 0 && q != quarters {
			same = false
		}
		quarters = q
	}
	if same {
		if err := f.setIrot(targets, quarters); err != nil {
			return err
		}
	} else {
		for i, id := range targets {
			if err := f.setItemIrot(id, each[i]); err != nil {
				return err
			}
		}
	}
	for _, id := range targets {
		got, err := f.itemOrientation(id)
		if err != nil {
			return err
		}
		if got != o {
			return fmt.Errorf("%w: item %d would show %d, not %d", ErrUnsupported, id, got, o)
		}
	}
	return nil
}

// imirRef is where an item's imir is associated: its index in the item's
// associations (-1 without one) and the property's index.
func (f *heifFile) imirRef(id uint32) (at int, idx uint16, err error) {
	p := f.m.Prop
	_, entry := p.Entry(id)
	if entry == nil {
		return -1, 0, nil
	}
	at = -1
	for i, ref := range entry.Props {
		if ref.Index != 0 && p.Props[ref.Index-1].Type == bmff.T("imir") {
			if at >= 0 {
				return -1, 0, fmt.Errorf("%w: two imir properties on item %d", ErrUnsupported, id)
			}
			at, idx = i, ref.Index
		}
	}
	return at, idx, nil
}

// setMirror gives each target a mirror or takes it away. An imir a target
// has stays when it's to be mirrored; one it needs is the primary's, else
// one the file has, else a new one. A removed association leaves the
// property in ipco, where another item may use it.
func (f *heifFile) setMirror(targets []uint32, o Orientation) error {
	mirrored := o.Mirrored()
	p := f.m.Prop
	var use uint16
	for _, id := range targets {
		ipma, entry := p.Entry(id)
		at, idx, err := f.imirRef(id)
		if err != nil {
			return err
		}
		if _, err := f.imirAxisOf(idx); err != nil {
			return err
		}
		switch {
		case mirrored && at >= 0:
			if use == 0 {
				use = idx
			}
		case mirrored:
			if use == 0 {
				if use, err = f.imirIndex(appleAxis(o)); err != nil {
					return err
				}
			}
			if len(entry.Props) >= 255 {
				return fmt.Errorf("%w: item %d has 255 properties", ErrUnsupported, id)
			}
			// imir is the last of the transformative properties.
			entry.Props = append(entry.Props, bmff.PropRef{Essential: true, Index: use})
			if err := f.widen(ipma, use); err != nil {
				return err
			}
		case at >= 0:
			entry.Props = append(entry.Props[:at], entry.Props[at+1:]...)
		}
	}
	return nil
}

// imirAxisOf checks the imir at property index idx (none for 0).
func (f *heifFile) imirAxisOf(idx uint16) (int, error) {
	if idx == 0 {
		return 0, nil
	}
	return imirAxis(f.m.Prop.Props[idx-1])
}

// appleAxis is the imir axis Apple's ImageIO writes for a mirrored
// orientation: 1 (left-right) for 2 and 7, 0 (top-bottom) for 4 and 5,
// each with an irot before it (0 or 90° anticlockwise).
func appleAxis(o Orientation) int {
	if o == 2 || o == 7 {
		return 1
	}
	return 0
}

// imirIndex is the 1-based index of an imir of axis to associate: one the
// file has, else a new one appended to ipco.
func (f *heifFile) imirIndex(axis int) (uint16, error) {
	p := f.m.Prop
	for i, pr := range p.Props {
		if pr.Type == bmff.T("imir") {
			if a, err := imirAxis(pr); err == nil && a == axis && pr.Raw[8]&^1 == 0 {
				return uint16(i + 1), nil
			}
		}
	}
	if len(p.Props) >= 0x7FFF {
		return 0, fmt.Errorf("%w: too many properties", ErrUnsupported)
	}
	p.Props = append(p.Props, bmff.Prop{Type: bmff.T("imir"), Raw: []byte{0, 0, 0, 9, 'i', 'm', 'i', 'r', byte(axis)}})
	return uint16(len(p.Props)), nil
}

// quartersFor is the irot angle (anticlockwise quarter turns) that makes
// item id show o with its other properties as they stand: its irot where
// it is, or a new one where setItemIrot puts it (before the first imir).
func (f *heifFile) quartersFor(id uint32, o Orientation) (int, error) {
	p := f.m.Prop
	_, entry := p.Entry(id)
	if entry == nil {
		return 0, fmt.Errorf("%w: item %d has no properties", ErrUnsupported, id)
	}
	want := o.asTransform()
	for q := range 4 {
		t := identity
		placed := false
		for _, ref := range entry.Props {
			if ref.Index == 0 {
				continue
			}
			pr := p.Props[ref.Index-1]
			switch pr.Type {
			case bmff.T("irot"):
				t = t.then(rotationCW(-q))
				placed = true
			case bmff.T("imir"):
				if !placed {
					t = t.then(rotationCW(-q))
					placed = true
				}
				axis, err := imirAxis(pr)
				if err != nil {
					return 0, err
				}
				t = t.then(imirTransform(axis))
			}
		}
		if !placed {
			t = t.then(rotationCW(-q))
		}
		if t == want {
			return q, nil
		}
	}
	return 0, fmt.Errorf("%w: item %d can't show %d", ErrUnsupported, id, o)
}
