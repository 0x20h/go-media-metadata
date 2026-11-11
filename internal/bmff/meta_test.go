package bmff_test

import (
	"bytes"
	"testing"

	"github.com/0x20h/go-media-metadata/internal/bmff"
	"github.com/0x20h/go-media-metadata/internal/bmff/testbuild"
)

// A meta box parsed and encoded again, unchanged, is byte for byte what it
// was, in every layout the builder makes.
func TestMetaRoundTrip(t *testing.T) {
	variants := []func(*testbuild.Options){
		func(*testbuild.Options) {},
		func(o *testbuild.Options) { o.IlocVersion = 0 },
		func(o *testbuild.Options) { o.IlocVersion, o.OffsetSize, o.LengthSize, o.BaseOffsetSize = 2, 8, 8, 8 },
		func(o *testbuild.Options) { o.IpmaVersion, o.IpmaWide = 1, true },
		func(o *testbuild.Options) { o.ExtraProps = 200; o.IpmaWide = true },
		func(o *testbuild.Options) { o.ExifInIdat = true; o.Imir = 1 },
		func(o *testbuild.Options) { o.MetaAfterMdat = true; o.Grid = false },
	}
	for i, v := range variants {
		o := testbuild.Default()
		v(&o)
		file, _ := testbuild.Build(o)
		boxes, err := bmff.ReadBoxes(bytes.NewReader(file), 0, int64(len(file)))
		if err != nil {
			t.Fatal(err)
		}
		mb, ok := bmff.Find(boxes, bmff.T("meta"))
		if !ok {
			t.Fatal("no meta")
		}
		raw := file[mb.Offset:mb.End()]
		m, err := bmff.ParseMeta(raw)
		if err != nil {
			t.Fatalf("variant %d: %v", i, err)
		}
		enc, err := m.Encode()
		if err != nil {
			t.Fatalf("variant %d: %v", i, err)
		}
		if !bytes.Equal(enc, raw) {
			t.Errorf("variant %d: re-encoded meta differs (%d vs %d bytes)", i, len(enc), len(raw))
		}
	}
}

// Every prefix of a valid meta box, and every single-byte corruption of
// its header area, is an error or a parse: never a panic.
func TestParseMetaTruncated(t *testing.T) {
	file, _ := testbuild.Build(testbuild.Default())
	boxes, _ := bmff.ReadBoxes(bytes.NewReader(file), 0, int64(len(file)))
	mb, _ := bmff.Find(boxes, bmff.T("meta"))
	raw := file[mb.Offset:mb.End()]
	for n := range len(raw) {
		_, _ = bmff.ParseMeta(raw[:n])
	}
	for i := range min(len(raw), 512) {
		c := bytes.Clone(raw)
		c[i] ^= 0xFF
		_, _ = bmff.ParseMeta(c)
	}
}

// A property box of size 0 ("to the end of ipco") gets its real size when
// it's written back, so a property appended after it stays its own box.
func TestSizeZeroPropertyKeepsItsSize(t *testing.T) {
	o := testbuild.Default()
	o.Irot = -1
	file, _ := testbuild.Build(o)
	boxes, _ := bmff.ReadBoxes(bytes.NewReader(file), 0, int64(len(file)))
	mb, _ := bmff.Find(boxes, bmff.T("meta"))
	raw := bytes.Clone(file[mb.Offset:mb.End()])
	m, err := bmff.ParseMeta(raw)
	if err != nil {
		t.Fatal(err)
	}
	last := m.Prop.Props[len(m.Prop.Props)-1].Raw
	// Find the last property in the raw meta and zero its size.
	at := bytes.LastIndex(raw, last)
	copy(raw[at:at+4], []byte{0, 0, 0, 0})
	m, err = bmff.ParseMeta(raw)
	if err != nil {
		t.Fatal(err)
	}
	n := len(m.Prop.Props)
	m.Prop.Props = append(m.Prop.Props, bmff.Prop{Type: bmff.T("irot"), Raw: []byte{0, 0, 0, 9, 'i', 'r', 'o', 't', 1}})
	enc, err := m.Encode()
	if err != nil {
		t.Fatal(err)
	}
	again, err := bmff.ParseMeta(enc)
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Prop.Props) != n+1 || again.Prop.Props[n].Type != bmff.T("irot") {
		t.Fatalf("%d properties after appending to %d, last %v", len(again.Prop.Props), n, again.Prop.Props[len(again.Prop.Props)-1].Type)
	}
}
