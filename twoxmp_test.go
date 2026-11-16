package mediameta

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"testing"

	"github.com/0x20h/go-media-metadata/internal/bmff/testbuild"
)

func xmpTitled(title string) []byte {
	return []byte(`<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
		`<rdf:Description rdf:about="" xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title><rdf:Alt>` +
		`<rdf:li xml:lang="x-default">` + title + `</rdf:li></rdf:Alt></dc:title></rdf:Description></rdf:RDF></x:xmpmeta>`)
}

// gainMapXMP is the kind of packet an iPhone's gain map has: HDR
// properties, none of the photo's.
var gainMapXMP = []byte(`<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
	`<rdf:Description rdf:about="" xmlns:HDRGainMap="http://ns.apple.com/HDRGainMap/1.0/" HDRGainMap:HDRGainMapVersion="65536"/>` +
	`</rdf:RDF></x:xmpmeta>`)

// A HEIF file with two XMP items: the image's is read and written (the one
// that describes the primary, else the lowest id of those that describe no
// other image), the other kept byte for byte.
func TestTwoXMPItems(t *testing.T) {
	for _, c := range []struct {
		name  string
		items []testbuild.XMPItem
		want  string // the title read; "" for no XMP of the image
		keep  uint32 // the item that must stay byte for byte
	}{
		{"the primary's and the gain map's", []testbuild.XMPItem{
			{ID: 41, Packet: gainMapXMP, Of: testbuild.GainMap}, {ID: 40, Packet: xmpTitled("Lake"), Of: testbuild.Primary}}, "Lake", 41},
		{"the primary's, at the higher id", []testbuild.XMPItem{
			{ID: 40, Packet: xmpTitled("Other")}, {ID: 41, Packet: xmpTitled("Lake"), Of: testbuild.Primary}}, "Lake", 40},
		{"neither linked: the lowest id", []testbuild.XMPItem{
			{ID: 41, Packet: xmpTitled("Second")}, {ID: 40, Packet: xmpTitled("First")}}, "First", 41},
		{"only the gain map's: none of the image's", []testbuild.XMPItem{
			{ID: 40, Packet: gainMapXMP, Of: testbuild.GainMap}}, "", 40},
	} {
		o := testbuild.Default()
		o.GainMap = true
		o.XMP = c.items
		file, data := testbuild.Build(o)
		for range 10 { // map order
			packet, err := ReadXMP(bytes.NewReader(file), int64(len(file)))
			if err != nil {
				t.Fatalf("%s: %v", c.name, err)
			}
			var title string
			if packet != nil {
				x, err := ParseXMP(packet)
				if err != nil {
					t.Fatalf("%s: %v", c.name, err)
				}
				title = x.Title
			}
			if title != c.want {
				t.Fatalf("%s: title %q, want %q", c.name, title, c.want)
			}
		}
		renamed := "Renamed"
		p, err := PlanMetadata(bytes.NewReader(file), int64(len(file)), MetadataEdit{XMP: &XMPEdit{Title: &renamed}})
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		checkPlan(t, file, p)
		out := written(t, p)
		packet, err := ReadXMP(bytes.NewReader(out), int64(len(out)))
		if err != nil || packet == nil {
			t.Fatalf("%s: XMP after the write: %v", c.name, err)
		}
		if x, _ := ParseXMP(packet); x.Title != "Renamed" {
			t.Errorf("%s: title %q after the write", c.name, x.Title)
		}
		if got := itemData(t, out)[c.keep]; !bytes.Equal(got, data[c.keep]) {
			t.Errorf("%s: item %d changed:\n%s", c.name, c.keep, got)
		}
	}
}

// testdata/heif-xmp/primary-and-gainmap.heic: a synthetic HEIC whose primary
// image and gain map have an XMP item each. ExifTool reads its title as
// this package does (the gain map's packet holds none), and after a write
// still does.
func TestTwoXMPItemsExifTool(t *testing.T) {
	const path = "testdata/heif-xmp/primary-and-gainmap.heic"
	file := readFile(t, path)
	m, err := ReadAll(bytes.NewReader(file), int64(len(file)))
	if err != nil || m.Errors.Err() != nil || m.Title != "Lake" {
		t.Fatalf("ReadAll: title %q, %v, %v", m.Title, err, m.Errors.Err())
	}
	tool, err := exec.LookPath("exiftool")
	if err != nil {
		t.Skip("exiftool not installed")
	}
	renamed := "Renamed"
	p, err := PlanMetadata(bytes.NewReader(file), int64(len(file)), MetadataEdit{XMP: &XMPEdit{Title: &renamed}})
	if err != nil {
		t.Fatal(err)
	}
	after := t.TempDir() + "/after.heic"
	if err := os.WriteFile(after, written(t, p), 0o644); err != nil {
		t.Fatal(err)
	}
	for f, want := range map[string]string{path: "Lake", after: "Renamed"} {
		out, err := exec.Command(tool, "-j", "-XMP:Title", "-XMP-HDRGainMap:all", f).Output()
		if err != nil {
			t.Fatal(err)
		}
		var got []map[string]any
		if err := json.Unmarshal(out, &got); err != nil || len(got) != 1 {
			t.Fatalf("%v: %s", err, out)
		}
		if got[0]["Title"] != want || got[0]["HDRGainMapVersion"] == nil {
			t.Errorf("%s: ExifTool reads %v, want title %q and the gain map's version", f, got[0], want)
		}
	}
}

// TestMakeTwoXMPCorpus writes testdata/heif-xmp/primary-and-gainmap.heic
// with MEDIAMETA_WRITE_CORPUS=1.
func TestMakeTwoXMPCorpus(t *testing.T) {
	if os.Getenv("MEDIAMETA_WRITE_CORPUS") == "" {
		t.Skip("set MEDIAMETA_WRITE_CORPUS=1 to write the corpus file")
	}
	o := testbuild.Default()
	o.GainMap = true
	o.XMP = []testbuild.XMPItem{{ID: 40, Packet: xmpTitled("Lake"), Of: testbuild.Primary}, {ID: 41, Packet: gainMapXMP, Of: testbuild.GainMap}}
	file, _ := testbuild.Build(o)
	if err := os.MkdirAll("testdata/heif-xmp", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("testdata/heif-xmp/primary-and-gainmap.heic", file, 0o644); err != nil {
		t.Fatal(err)
	}
}
