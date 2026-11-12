package mediameta

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/0x20h/go-media-metadata/internal/bmff"
	"github.com/0x20h/go-media-metadata/internal/bmff/testbuild"
)

// itemData reads every item's data out of a HEIF file through its iloc.
func itemData(t *testing.T, file []byte) map[uint32][]byte {
	t.Helper()
	f, err := parseHEIF(bytes.NewReader(file), int64(len(file)))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	raw := file[f.meta.Offset:f.meta.End()]
	idat := idatBody(raw)
	out := map[uint32][]byte{}
	for _, it := range f.m.Loc.Items {
		var base int64
		if it.ConstructionMethod == 1 {
			base = f.meta.Offset + idat
		}
		var d []byte
		for _, e := range it.Extents {
			start := base + int64(it.BaseOffset+e.Offset)
			d = append(d, file[start:start+int64(e.Length)]...)
		}
		out[it.ID] = d
	}
	return out
}

// relatedInStep checks the invariant for the images that belong to the
// primary (thumbnails, auxiliary images such as a gain map): each that
// showed the primary's orientation before shows its new one after.
func relatedInStep(t *testing.T, before, after []byte) {
	t.Helper()
	fb, err := parseHEIF(bytes.NewReader(before), int64(len(before)))
	if err != nil {
		t.Fatal(err)
	}
	fa, err := parseHEIF(bytes.NewReader(after), int64(len(after)))
	if err != nil {
		t.Fatal(err)
	}
	pb, _ := fb.orientation()
	pa, _ := fa.orientation()
	for _, id := range fb.related() {
		ob, err := fb.itemOrientation(id)
		if err != nil || ob != pb {
			continue
		}
		if oa, _ := fa.itemOrientation(id); oa != pa {
			t.Errorf("item %d (thumbnail/auxiliary) shows %d, the primary %d: they were in step", id, oa, pa)
		}
	}
}

func orientationOfFile(t *testing.T, file []byte) Orientation {
	t.Helper()
	o, err := ReadOrientation(bytes.NewReader(file), int64(len(file)))
	if err != nil {
		t.Fatalf("ReadOrientation: %v", err)
	}
	return o
}

func setRotation(t *testing.T, file []byte, degrees int) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := SetRotation(bytes.NewReader(file), int64(len(file)), &out, degrees); err != nil {
		t.Fatalf("SetRotation(%d): %v", degrees, err)
	}
	return out.Bytes()
}

// The properties' orientation, by hand from ISO/IEC 23008-12 (irot turns
// anticlockwise and comes first; imir axis 0 mirrors left-right, 1
// top-bottom) and the Exif numbering.
func TestReadOrientation(t *testing.T) {
	for _, c := range []struct {
		irot, imir int
		want       Orientation
	}{
		{-1, -1, 1}, {0, -1, 1}, {1, -1, 8}, {2, -1, 3}, {3, -1, 6},
		{-1, 0, 2}, {1, 0, 7}, {2, 0, 4}, {3, 0, 5}, {-1, 1, 4}, {1, 1, 5}, {2, 1, 2}, {3, 1, 7},
	} {
		o := testbuild.Default()
		o.Irot, o.Imir = c.irot, c.imir
		file, _ := testbuild.Build(o)
		if got := orientationOfFile(t, file); got != c.want {
			t.Errorf("irot %d imir %d: %v (%d), want %d", c.irot, c.imir, got, got, c.want)
		}
	}
}

func TestOrientationDecomposition(t *testing.T) {
	for o := Orientation(1); o <= 8; o++ {
		if orientationOf(o.asTransform()) != o {
			t.Errorf("%d doesn't round-trip", o)
		}
	}
}

type layout struct {
	name string
	edit func(*testbuild.Options)
}

var layouts = []layout{
	{"iPhone-like", func(*testbuild.Options) {}},
	{"no irot", func(o *testbuild.Options) { o.Irot = -1 }},
	{"irot 0", func(o *testbuild.Options) { o.Irot = 0 }},
	{"shared irot", func(o *testbuild.Options) { o.SharedIrot = true }},
	{"shared irot, no exif", func(o *testbuild.Options) { o.SharedIrot = true; o.ExifOrientation = -1 }},
	{"iPhone: irot shared with thumbnail and gain map", func(o *testbuild.Options) { o.SharedIrot = true; o.GainMap = true }},
	{"gain map with its own rotation", func(o *testbuild.Options) { o.GainMap = true }},
	{"mirrored", func(o *testbuild.Options) { o.Imir = 0 }},
	{"mirrored without irot", func(o *testbuild.Options) { o.Irot = -1; o.Imir = 1 }},
	{"ipma v1 wide", func(o *testbuild.Options) { o.IpmaVersion = 1; o.IpmaWide = true }},
	{"indices past 127", func(o *testbuild.Options) { o.Irot = -1; o.ExtraProps = 125 }},
	{"iloc v0", func(o *testbuild.Options) { o.IlocVersion = 0 }},
	{"iloc v2, 8-byte fields", func(o *testbuild.Options) {
		o.IlocVersion, o.OffsetSize, o.LengthSize = 2, 8, 8
	}},
	{"base offsets", func(o *testbuild.Options) { o.Irot = -1; o.BaseOffsetSize = 4 }},
	{"base offsets, no extent offsets", func(o *testbuild.Options) { o.Irot = -1; o.BaseOffsetSize = 8; o.OffsetSize = 0 }},
	{"exif big-endian", func(o *testbuild.Options) { o.ExifBigEndian = true }},
	{"exif in idat", func(o *testbuild.Options) { o.Irot = -1; o.ExifInIdat = true }},
	{"exif without orientation", func(o *testbuild.Options) { o.ExifOrientation = 0 }},
	{"no exif", func(o *testbuild.Options) { o.ExifOrientation = -1 }},
	{"meta after mdat", func(o *testbuild.Options) { o.Irot = -1; o.MetaAfterMdat = true }},
	{"single image", func(o *testbuild.Options) { o.Grid = false; o.Thumbnail = false; o.Irot = -1 }},
}

// SetRotation on every layout and angle: the orientation reads back, the
// Exif tag (where there is one) agrees, every item's data is byte for byte
// what it was, the bytes before the meta box are unchanged, a second
// identical write changes nothing, and going back restores the original.
func TestSetRotation(t *testing.T) {
	wantFor := map[int]Orientation{0: 1, 90: 6, 180: 3, 270: 8}
	for _, l := range layouts {
		o := testbuild.Default()
		l.edit(&o)
		orig, data := testbuild.Build(o)
		origOrientation := orientationOfFile(t, orig)
		_, origHasTag, err := exifOrientation(bytes.NewReader(orig), int64(len(orig)))
		if err != nil {
			t.Fatalf("%s: %v", l.name, err)
		}
		for _, deg := range []int{0, 90, 180, 270} {
			t.Run(fmt.Sprintf("%s/%d", l.name, deg), func(t *testing.T) {
				out := setRotation(t, orig, deg)
				got := orientationOfFile(t, out)
				if o.Imir < 0 {
					if got != wantFor[deg] {
						t.Errorf("orientation %d, want %d", got, wantFor[deg])
					}
				} else if got.Mirrored() != true {
					t.Errorf("orientation %d lost its mirroring", got)
				}
				tag, has, err := exifOrientation(bytes.NewReader(out), int64(len(out)))
				if err != nil {
					t.Fatal(err)
				}
				if has != origHasTag {
					t.Errorf("Exif Orientation tag present %v, was %v", has, origHasTag)
				}
				if has && tag != got {
					t.Errorf("Exif Orientation %d, the properties show %d", tag, got)
				}
				for id, want := range data {
					if id == testbuild.Exif {
						continue // its Orientation tag may change
					}
					if g := itemData(t, out)[id]; !bytes.Equal(g, want) {
						t.Errorf("item %d's data changed (%d bytes, was %d)", id, len(g), len(want))
					}
				}
				if ex, ok := data[testbuild.Exif]; ok {
					g := itemData(t, out)[testbuild.Exif]
					if diff := differing(g, ex); diff > 2 {
						t.Errorf("Exif item: %d bytes differ, want at most the 2 of the tag", diff)
					}
				}
				f, _ := parseHEIF(bytes.NewReader(orig), int64(len(orig)))
				// Before the meta box only the Exif tag may change (an Exif
				// item there, when the meta box comes last).
				if d := differing(out[:f.meta.Offset], orig[:f.meta.Offset]); d > 2 {
					t.Errorf("%d bytes before the meta box changed", d)
				}
				relatedInStep(t, orig, out)
				if again := setRotation(t, out, deg); !bytes.Equal(again, out) {
					t.Error("a second identical SetRotation changed the file")
				}
			})
		}
		// Back to where it was: byte for byte the original where the
		// change was in place.
		if origOrientation.Mirrored() || o.SharedIrot {
			continue
		}
		back := setRotation(t, setRotation(t, orig, 90), origOrientation.Degrees())
		if o.Irot >= 0 && o.Irot != 0 && !bytes.Equal(back, orig) {
			t.Errorf("%s: rotating and back isn't the original", l.name)
		}
		if got := orientationOfFile(t, back); got != origOrientation {
			t.Errorf("%s: back to %d, want %d", l.name, got, origOrientation)
		}
	}
}

func differing(a, b []byte) int {
	if len(a) != len(b) {
		return 1 << 30
	}
	n := 0
	for i := range a {
		if a[i] != b[i] {
			n++
		}
	}
	return n
}

// An irot only the primary uses is changed in place: same size, one byte.
func TestSetRotationInPlace(t *testing.T) {
	orig, _ := testbuild.Build(testbuild.Default())
	out := setRotation(t, orig, 180)
	if len(out) != len(orig) {
		t.Fatalf("size %d, was %d", len(out), len(orig))
	}
	// The irot byte and the two Exif Orientation bytes (the value 6 → 3
	// changes one of them).
	if d := differing(out, orig); d < 1 || d > 3 {
		t.Errorf("%d bytes differ", d)
	}
}

func TestSetRotationErrors(t *testing.T) {
	orig, _ := testbuild.Build(testbuild.Default())
	var out bytes.Buffer
	if err := SetRotation(bytes.NewReader(orig), int64(len(orig)), &out, 45); err == nil {
		t.Error("45° accepted")
	}
	jpeg := []byte("\xff\xd8\xff\xe0\x00\x10JFIF\x00\x01\x01\x00\x00\x01\x00\x01\x00\x00\xff\xd9")
	if _, err := ReadOrientation(bytes.NewReader(jpeg), int64(len(jpeg))); !errors.Is(err, ErrNotHEIF) && !errors.Is(err, ErrMalformed) {
		t.Errorf("a JPEG: %v", err)
	}
	mp4 := []byte("\x00\x00\x00\x14ftypisom\x00\x00\x02\x00isom\x00\x00\x00\x08free")
	if _, err := ReadOrientation(bytes.NewReader(mp4), int64(len(mp4))); !errors.Is(err, ErrNotHEIF) {
		t.Errorf("an MP4: %v", err)
	}
	// Truncated anywhere: an error, never a panic.
	for n := range len(orig) {
		_, _ = ReadOrientation(bytes.NewReader(orig[:n]), int64(n))
	}
}

// A file with a movie box can't have its offsets moved safely.
func TestSetRotationRefusesMovieBox(t *testing.T) {
	o := testbuild.Default()
	o.Irot = -1
	orig, _ := testbuild.Build(o)
	withMoov := append(append([]byte{}, orig...), 0, 0, 0, 8, 'm', 'o', 'o', 'v')
	var out bytes.Buffer
	err := SetRotation(bytes.NewReader(withMoov), int64(len(withMoov)), &out, 90)
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("with a moov box: %v, want ErrUnsupported", err)
	}
}

func seeds(f *testing.F) {
	for _, l := range layouts {
		o := testbuild.Default()
		l.edit(&o)
		file, _ := testbuild.Build(o)
		f.Add(file)
	}
	f.Add(zeroWidthExtentsFile())
}

// zeroWidthExtentsFile is a HEIF file whose iloc has every field size 0,
// so extents take no bytes, and whose items each declare 65535 of them.
func zeroWidthExtentsFile() []byte {
	box := func(typ string, body []byte) []byte {
		b := binary.BigEndian.AppendUint32(nil, uint32(8+len(body)))
		return append(append(b, typ...), body...)
	}
	iloc := []byte{0, 0, 0, 0, 0, 0}
	iloc = binary.BigEndian.AppendUint16(iloc, 100)
	for i := range 100 {
		iloc = binary.BigEndian.AppendUint16(iloc, uint16(i+1))
		iloc = append(iloc, 0, 0, 0xFF, 0xFF)
	}
	ftyp := box("ftyp", []byte("heic\x00\x00\x00\x00mif1heic"))
	hdlr := box("hdlr", append(append(make([]byte, 8), "pict"...), make([]byte, 13)...))
	meta := append(append([]byte{0, 0, 0, 0}, hdlr...), box("iloc", iloc)...)
	return append(ftyp, box("meta", meta)...)
}

// fuzzDeadline is how long one fuzz input may take: a few milliseconds is
// normal, so this only catches an input that never finishes.
const fuzzDeadline = 5 * time.Second

// withinDeadline runs fn and fails the input if it takes longer than
// fuzzDeadline, so a hang is reported as that input instead of stalling
// the fuzzer.
func withinDeadline(t *testing.T, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(fuzzDeadline):
		t.Fatalf("an input took longer than %v", fuzzDeadline)
	}
}

func FuzzReadOrientation(f *testing.F) {
	seeds(f)
	f.Fuzz(func(t *testing.T, data []byte) {
		withinDeadline(t, func() {
			o, err := ReadOrientation(bytes.NewReader(data), int64(len(data)))
			if err == nil && !o.Valid() {
				t.Errorf("orientation %d without an error", o)
			}
		})
	})
}

func FuzzSetRotation(f *testing.F) {
	seeds(f)
	f.Fuzz(func(t *testing.T, data []byte) {
		withinDeadline(t, func() {
			for _, deg := range []int{0, 90} {
				var out bytes.Buffer
				if err := SetRotation(bytes.NewReader(data), int64(len(data)), &out, deg); err != nil {
					continue
				}
				o, err := ReadOrientation(bytes.NewReader(out.Bytes()), int64(out.Len()))
				if err != nil {
					t.Errorf("SetRotation(%d) wrote a file that doesn't read back: %v", deg, err)
					return
				}
				if !o.Valid() {
					t.Errorf("orientation %d", o)
					return
				}
			}
		})
	})
}

var _ = bmff.T

// Past 127 properties the ipma switches to 15-bit indices.
func TestSetRotationWidensIpma(t *testing.T) {
	o := testbuild.Default()
	o.Irot, o.ExtraProps = -1, 125
	orig, _ := testbuild.Build(o)
	out := setRotation(t, orig, 90)
	f, err := parseHEIF(bytes.NewReader(out), int64(len(out)))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.m.Prop.Props) <= 127 || f.m.Prop.Assoc[0].Flags&1 == 0 {
		t.Fatalf("%d properties, ipma flags %d: want more than 127 and 15-bit indices", len(f.m.Prop.Props), f.m.Prop.Assoc[0].Flags)
	}
	if got := orientationOfFile(t, out); got != 6 {
		t.Fatalf("orientation %d, want 6", got)
	}
}

// An Exif item pointing outside the media data (here at the ftyp) has its
// "tag" left alone: a patch never writes over structure.
func TestExifPatchStaysInMediaData(t *testing.T) {
	o := testbuild.Default()
	orig, _ := testbuild.Build(o)
	f, err := parseHEIF(bytes.NewReader(orig), int64(len(orig)))
	if err != nil {
		t.Fatal(err)
	}
	ftyp := bytes.Clone(orig[:f.boxes[0].End()])
	// Point the Exif item's extent at offset 0 (iloc v1, 4-byte fields).
	it, _ := f.m.Loc.Item(testbuild.Exif)
	it.Extents[0].Offset = 0
	meta, err := f.m.Encode()
	if err != nil {
		t.Fatal(err)
	}
	bad := append(append(bytes.Clone(orig[:f.meta.Offset]), meta...), orig[f.meta.End():]...)
	out := setRotation(t, bad, 180)
	if !bytes.Equal(out[:len(ftyp)], ftyp) {
		t.Fatal("the ftyp changed")
	}
	if got := orientationOfFile(t, out); got != 3 {
		t.Fatalf("orientation %d, want 3", got)
	}
}

// An ipco without properties is written back as an ipco.
func TestEmptyIpcoRoundTrip(t *testing.T) {
	m, err := bmff.ParseMeta(buildEmptyIpcoMeta())
	if err != nil {
		t.Fatal(err)
	}
	enc, err := m.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bmff.ParseMeta(enc); err != nil {
		t.Fatalf("re-encoded meta doesn't parse: %v", err)
	}
}

func buildEmptyIpcoMeta() []byte {
	box := func(t string, body []byte) []byte {
		b := []byte{0, 0, 0, 0}
		n := len(body) + 8
		b[0], b[1], b[2], b[3] = byte(n>>24), byte(n>>16), byte(n>>8), byte(n)
		return append(append(b, t...), body...)
	}
	ipma := box("ipma", []byte{0, 0, 0, 0, 0, 0, 0, 0})
	iprp := box("iprp", append(box("ipco", nil), ipma...))
	return box("meta", append([]byte{0, 0, 0, 0}, iprp...))
}

// iPhones share one irot between the image, its thumbnail and its HDR gain
// map: a rotation changes that irot in place, so all three turn together
// and nothing else moves; a gain map with a rotation of its own is left as
// it is.
func TestRotateRelatedImages(t *testing.T) {
	o := testbuild.Default()
	o.SharedIrot, o.GainMap = true, true
	orig, _ := testbuild.Build(o)
	for _, deg := range []int{0, 90, 180, 270} {
		out := setRotation(t, orig, deg)
		if len(out) != len(orig) {
			t.Errorf("%d°: %d bytes, was %d: the shared irot should change in place", deg, len(out), len(orig))
		}
		f, err := parseHEIF(bytes.NewReader(out), int64(len(out)))
		if err != nil {
			t.Fatal(err)
		}
		p, _ := f.orientation()
		for _, id := range []uint32{testbuild.Thumbnail, testbuild.GainMap} {
			if got, _ := f.itemOrientation(id); got != p {
				t.Errorf("%d°: item %d shows %d, the primary %d", deg, id, got, p)
			}
		}
	}

	o = testbuild.Default()
	o.GainMap = true // the gain map has no irot: it doesn't show the primary's rotation
	orig, _ = testbuild.Build(o)
	out := setRotation(t, orig, 180)
	f, _ := parseHEIF(bytes.NewReader(out), int64(len(out)))
	if got, _ := f.itemOrientation(testbuild.GainMap); got != 1 {
		t.Errorf("a gain map with its own rotation turned to %d", got)
	}
}
