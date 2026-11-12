package mediameta

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/0x20h/go-media-metadata/internal/bmff/testbuild"
)

// xmpPacket is an XMP packet with a title, of at least n bytes (padded
// with whitespace, as XMP packets are).
func xmpPacket(title string, n int) []byte {
	p := []byte(`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?><x:xmpmeta xmlns:x="adobe:ns:meta/">` +
		`<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#"><rdf:Description rdf:about="" ` +
		`xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title><rdf:Alt><rdf:li xml:lang="x-default">` + title +
		`</rdf:li></rdf:Alt></dc:title></rdf:Description></rdf:RDF></x:xmpmeta>`)
	for len(p) < n-len(`<?xpacket end="w"?>`) {
		p = append(p, ' ')
	}
	return append(p, `<?xpacket end="w"?>`...)
}

// paddedTIFF is photoTIFF followed by n bytes nothing points to: a longer
// value for the same fields.
func paddedTIFF(n int) []byte {
	return append(photoTIFF(binary.LittleEndian), make([]byte, n)...)
}

// metadataEdits are the edits run on every layout: Exif and XMP, short
// enough to fit an existing item and too long to.
var metadataEdits = []struct {
	name string
	exif []byte
	xmp  []byte
}{
	{name: "exif", exif: photoTIFF(binary.LittleEndian)},
	{name: "exif big-endian", exif: photoTIFF(binary.BigEndian)},
	{name: "exif long", exif: paddedTIFF(4000)},
	{name: "xmp", xmp: xmpPacket("Harbour at dusk", 0)},
	{name: "xmp long", xmp: xmpPacket("Harbour at dawn", 6000)},
}

func planMetadata(t *testing.T, file []byte, exif, xmp []byte) *Plan {
	t.Helper()
	var p *Plan
	var err error
	if exif != nil {
		p, err = PlanExif(bytes.NewReader(file), int64(len(file)), exif)
	} else {
		p, err = PlanXMP(bytes.NewReader(file), int64(len(file)), xmp)
	}
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func written(t *testing.T, p *Plan) []byte {
	t.Helper()
	var out bytes.Buffer
	if _, err := p.WriteTo(&out); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// On every layout and sample file: the item reads back as written, the
// image items' data and the orientation are what they were, the plan's
// invariants hold, and writing the same value again is in place.
func TestSetMetadataItems(t *testing.T) {
	for _, f := range planFiles(t) {
		for _, e := range metadataEdits {
			t.Run(f.name+"/"+e.name, func(t *testing.T) {
				p := planMetadata(t, f.data, e.exif, e.xmp)
				checkPlan(t, f.data, p)
				out := written(t, p)
				if e.exif != nil {
					x, err := ReadExif(bytes.NewReader(out), int64(len(out)))
					if err != nil {
						t.Fatal(err)
					}
					if x.DateTimeOriginal.IsZero() || x.GPS == nil {
						t.Errorf("the Exif written doesn't read back: %+v", x)
					}
				} else {
					got, err := ReadXMP(bytes.NewReader(out), int64(len(out)))
					if err != nil {
						t.Fatal(err)
					}
					if !bytes.Equal(got, e.xmp) {
						t.Errorf("XMP reads back as %d bytes, wrote %d", len(got), len(e.xmp))
					}
				}
				if got, want := orientationOfFile(t, out), orientationOfFile(t, f.data); got != want {
					t.Errorf("orientation %d, was %d", got, want)
				}
				before := itemData(t, f.data)
				after := itemData(t, out)
				fb, _ := parseHEIF(bytes.NewReader(f.data), int64(len(f.data)))
				for id, d := range before {
					if typ := fb.m.ItemTypes[id]; typ == [4]byte{'E', 'x', 'i', 'f'} || typ == [4]byte{'m', 'i', 'm', 'e'} {
						continue
					}
					if !bytes.Equal(after[id], d) {
						t.Errorf("item %d's data changed", id)
					}
				}
				again := planMetadata(t, out, e.exif, e.xmp)
				if !again.InPlace {
					t.Errorf("the same value again isn't in place (%d → %d bytes)", again.Size, again.NewSize)
				}
				checkPlan(t, out, again)
			})
		}
	}
}

// A value that fits the item is a patch: the data and the item's length.
// One that doesn't goes in a media data box at the end.
func TestSetMetadataInPlace(t *testing.T) {
	file, _ := testbuild.Build(testbuild.Default())
	long := planMetadata(t, file, paddedTIFF(4000), nil)
	if long.InPlace || long.NewSize <= long.Size {
		t.Fatalf("a long Exif: InPlace %v, %d → %d bytes", long.InPlace, long.Size, long.NewSize)
	}
	grown := written(t, long)
	short := planMetadata(t, grown, photoTIFF(binary.BigEndian), nil)
	if !short.InPlace {
		t.Fatal("a shorter Exif isn't in place")
	}
	n := 0
	for _, pt := range short.Patches {
		n += len(pt.Data)
	}
	if n > len(photoTIFF(binary.BigEndian))+4+8 {
		t.Errorf("%d bytes patched for a %d-byte value", n, len(photoTIFF(binary.BigEndian))+4)
	}
	// Adding an XMP item grows the meta box: offsets move, not in place.
	add := planMetadata(t, file, nil, xmpPacket("x", 0))
	if add.InPlace {
		t.Error("adding an XMP item is in place")
	}
	checkPlan(t, file, add)
}

func TestSetMetadataErrors(t *testing.T) {
	file, _ := testbuild.Build(testbuild.Default())
	r := bytes.NewReader(file)
	if _, err := PlanExif(r, int64(len(file)), []byte("Exif\x00\x00II*\x00")); err == nil {
		t.Error("Exif without a TIFF header first accepted")
	}
	if _, err := PlanXMP(r, int64(len(file)), []byte("<html/>")); err == nil {
		t.Error("non-XMP accepted")
	}
	if _, err := PlanXMP(r, int64(len(file)), []byte("rdf:RDF \xff")); err == nil {
		t.Error("invalid UTF-8 accepted")
	}
	jpeg := plainJPEG(t)
	if _, err := PlanXMP(bytes.NewReader(jpeg), int64(len(jpeg)), xmpPacket("x", 0)); !errors.Is(err, ErrNotHEIF) && !errors.Is(err, ErrMalformed) {
		t.Errorf("a JPEG: %v", err)
	}
}

// ExifTool reads what was written: the Exif fields on the sample files
// and the XMP title. Runs where exiftool is installed.
func TestMetadataItemsAgreeWithExifTool(t *testing.T) {
	tool, err := exec.LookPath("exiftool")
	if err != nil {
		t.Skip("exiftool not installed")
	}
	dir := t.TempDir()
	var names []string
	for _, f := range planFiles(t) {
		out := written(t, planMetadata(t, f.data, paddedTIFF(100), nil))
		out = written(t, planMetadata(t, out, nil, xmpPacket("Harbour at dusk", 0)))
		name := fmt.Sprintf("%02d.heic", len(names))
		names = append(names, f.name)
		if err := os.WriteFile(filepath.Join(dir, name), out, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := exec.Command(tool, "-j", "-n", "-q", "-EXIF:DateTimeOriginal", "-Composite:GPSLatitude", "-XMP-dc:Title", dir).Output()
	if err != nil {
		t.Fatalf("exiftool: %v", err)
	}
	var files []struct {
		SourceFile       string
		DateTimeOriginal string
		GPSLatitude      *float64
		Title            string
	}
	if err := json.Unmarshal(got, &files); err != nil {
		t.Fatal(err)
	}
	if len(files) != len(names) {
		t.Fatalf("exiftool read %d files, want %d", len(files), len(names))
	}
	for _, f := range files {
		if f.DateTimeOriginal != "2025:05:04 13:14:15" || f.GPSLatitude == nil || f.Title != "Harbour at dusk" {
			t.Errorf("%s: ExifTool reads %+v", f.SourceFile, f)
		}
	}
}

// A file without an iref box gets one, after iinf, with the new item's
// cdsc reference to the primary image.
func TestAddMetadataItemCreatesIref(t *testing.T) {
	o := testbuild.Default()
	o.Grid, o.Thumbnail, o.Irot, o.ExifOrientation = false, false, -1, -1
	file, _ := testbuild.Build(o)
	f, err := parseHEIF(bytes.NewReader(file), int64(len(file)))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.m.Refs) != 0 {
		t.Skip("the fixture has references")
	}
	out := written(t, planMetadata(t, file, nil, xmpPacket("x", 0)))
	g, err := parseHEIF(bytes.NewReader(out), int64(len(out)))
	if err != nil {
		t.Fatal(err)
	}
	id, found, err := g.metadataItem(kindXMP)
	if err != nil || !found {
		t.Fatalf("no XMP item: %v", err)
	}
	if len(g.m.Refs) != 1 || g.m.Refs[0].Type != [4]byte{'c', 'd', 's', 'c'} || g.m.Refs[0].From != id || g.m.Refs[0].To[0] != g.m.Primary {
		t.Errorf("references %+v", g.m.Refs)
	}
}
