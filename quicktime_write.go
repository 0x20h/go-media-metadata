package mediameta

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"

	"github.com/0x20h/go-media-metadata/internal/bmff"
)

// planVideoOrientation is setVideoOrientation's edit, computed but not
// written. It is always in place: each video track's matrix is 36 bytes
// at a fixed place in its track header, so the plan patches those and
// nothing else. A track already shown as o is left as it is, so a movie
// already shown as o gives a plan without patches.
func planVideoOrientation(src io.ReaderAt, size int64, o Orientation) (*Plan, error) {
	if !o.Valid() {
		return nil, fmt.Errorf("mediameta: orientation %d: not 1 to 8", uint8(o))
	}
	tracks, err := videoTracks(src, size)
	if err != nil {
		return nil, err
	}
	var patches []Patch
	for _, t := range tracks {
		var cur [9]int32
		for i := range cur {
			cur[i] = int32(binary.BigEndian.Uint32(t.matrix[4*i:]))
		}
		if matrixOrientation(cur) == o {
			continue // shown as o already: its translation stays as written
		}
		m, ok := orientationMatrix(o, t.width, t.height)
		if !ok {
			return nil, fmt.Errorf("%w: a video track of %d×%d pixels is too large for its matrix", ErrUnsupported, t.width, t.height)
		}
		if !bytes.Equal(m, t.matrix) {
			patches = append(patches, Patch{Offset: t.matrixAt, Data: m})
		}
	}
	internal := make([]patch, len(patches))
	for i, p := range patches {
		internal[i] = patch{at: p.Offset, data: p.Data}
	}
	return &Plan{InPlace: true, Patches: patches, Size: size, NewSize: size, src: src, patches: internal}, nil
}

// planVideoRotation is setVideoRotation's edit, computed but not written.
// Like planVideoOrientation it is always in place.
func planVideoRotation(src io.ReaderAt, size int64, degrees int) (*Plan, error) {
	if degrees%90 != 0 {
		return nil, fmt.Errorf("mediameta: rotation of %d°: not a multiple of 90", degrees)
	}
	v, err := ReadVideo(src, size)
	if err != nil {
		return nil, err
	}
	mirrored := v.Orientation.Valid() && v.Orientation.Mirrored()
	return planVideoOrientation(src, size, orientationFor(mirrored, degrees))
}

// videoTrack is where a video track's matrix is, as read.
type videoTrack struct {
	matrixAt      int64
	matrix        []byte
	width, height int
}

// videoTracks finds the movie's video tracks. A movie header with a
// matrix other than the identity is refused: it turns every track, and
// the orientation is then theirs and its together.
func videoTracks(src io.ReaderAt, size int64) ([]videoTrack, error) {
	if size < 8 {
		return nil, errNotQuickTime
	}
	top, err := bmff.ReadBoxes(src, 0, size)
	if err != nil {
		if !looksLikeMovie(src, size) {
			return nil, errNotQuickTime
		}
		return nil, err
	}
	if err := checkMovie(src, top); err != nil {
		return nil, err
	}
	moov, ok := bmff.Find(top, bmff.T("moov"))
	if !ok {
		return nil, fmt.Errorf("%w: no movie box", ErrMalformed)
	}
	kids, err := bmff.ReadBoxes(src, moov.Body(), moov.End())
	if err != nil {
		return nil, err
	}
	var out []videoTrack
	for _, k := range kids {
		switch k.Type {
		case bmff.T("mvhd"):
			b, err := readBox(src, k, maxLeafBox)
			if err != nil {
				return nil, err
			}
			if !mvhdIdentity(b) {
				return nil, fmt.Errorf("%w: the movie header has a matrix of its own", ErrUnsupported)
			}
		case bmff.T("trak"):
			tkhd, ok, err := videoTkhd(src, k)
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
			b, err := readBox(src, tkhd, maxLeafBox)
			if err != nil {
				return nil, err
			}
			_, w, h, ok := tkhdMatrix(b)
			if !ok {
				return nil, fmt.Errorf("%w: a track header this package can't read", ErrUnsupported)
			}
			at, _ := tkhdMatrixAt(b[0]) // tkhdMatrix checked the version and length
			out = append(out, videoTrack{
				matrixAt: tkhd.Body() + int64(at),
				matrix:   b[at : at+36],
				width:    w, height: h,
			})
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: no video track", ErrUnsupported)
	}
	return out, nil
}

// mvhdMatrixAt is where a movie header's matrix starts in its payload:
// after the times, timescale, duration, rate, volume and 10 reserved
// bytes.
func mvhdMatrixAt(version byte) (int, bool) {
	switch version {
	case 0:
		return 36, true
	case 1:
		return 48, true
	}
	return 0, false
}

// identityMatrix is the matrix that leaves a picture as it is.
var identityMatrix = matrixBytes([9]int32{fix16One, 0, 0, 0, fix16One, 0, 0, 0, fix30One})

// mvhdIdentity reports whether a movie header's matrix is the identity
// (or the header too short to have one, as a broken writer's may be).
func mvhdIdentity(b []byte) bool {
	if len(b) < 1 {
		return true
	}
	at, ok := mvhdMatrixAt(b[0])
	if !ok || len(b) < at+36 {
		return true
	}
	return bytes.Equal(b[at:at+36], identityMatrix)
}

func matrixBytes(m [9]int32) []byte {
	out := make([]byte, 0, 36)
	for _, v := range m {
		out = binary.BigEndian.AppendUint32(out, uint32(v))
	}
	return out
}

// orientationMatrix is the track matrix that shows a w×h picture as o:
// the rotation or mirroring, and the translation that moves the turned
// picture back to the origin, as camera recordings have it. False when
// the translation doesn't fit 16.16 fixed point.
func orientationMatrix(o Orientation, w, h int) ([]byte, bool) {
	var abcd [4]int32
	for k, v := range matrices {
		if v == o {
			abcd = k
		}
	}
	a, b, c, d := abcd[0]/fix16One, abcd[1]/fix16One, abcd[2]/fix16One, abcd[3]/fix16One
	// The stored corners (0,0), (w,0), (0,h), (w,h) go to
	// (a·x + c·y, b·x + d·y); the translation is minus the smallest.
	minX, minY := 0, 0
	for _, p := range [][2]int{{w, 0}, {0, h}, {w, h}} {
		minX = min(minX, int(a)*p[0]+int(c)*p[1])
		minY = min(minY, int(b)*p[0]+int(d)*p[1])
	}
	tx, ty := -minX, -minY
	if tx > 0x7FFF || ty > 0x7FFF {
		return nil, false
	}
	return matrixBytes([9]int32{abcd[0], abcd[1], 0, abcd[2], abcd[3], 0, int32(tx) * fix16One, int32(ty) * fix16One, fix30One}), true
}
