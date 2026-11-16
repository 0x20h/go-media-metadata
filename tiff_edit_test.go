package mediameta

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// metaTIFF builds an uncompressed 4×3 RGB TIFF with a description, an
// ExifIFD (a DateTimeOriginal, and a maker note when makerNote is set)
// and an XMP packet with a title: the metadata the edits change.
func metaTIFF(order binary.ByteOrder, makerNote bool) []byte {
	ord := order.(binary.AppendByteOrder)
	var b []byte
	if order == binary.LittleEndian {
		b = append(b, 'I', 'I', '*', 0)
	} else {
		b = append(b, 'M', 'M', 0, '*')
	}
	b = ord.AppendUint32(b, 0)
	pixels := bytes.Repeat([]byte{10, 20, 30}, 12)
	stripAt := len(b)
	b = append(b, pixels...)
	value := func(v []byte) uint32 {
		if len(b)%2 == 1 {
			b = append(b, 0)
		}
		at := len(b)
		b = append(b, v...)
		return uint32(at)
	}
	descAt := value([]byte("Old description\x00"))
	dtoAt := value([]byte("2001:02:03 04:05:06\x00"))
	packet := []byte(`<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
		`<rdf:Description rdf:about="" xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title><rdf:Alt>` +
		`<rdf:li xml:lang="x-default">Old title</rdf:li></rdf:Alt></dc:title></rdf:Description></rdf:RDF></x:xmpmeta>`)
	xmpAt := value(packet)
	mnAt := value([]byte("Maker notes!"))
	type ent struct {
		tag, typ uint16
		count    uint32
		v        uint32
	}
	ifd := func(es []ent) uint32 {
		sort.Slice(es, func(i, j int) bool { return es[i].tag < es[j].tag })
		out := ord.AppendUint16(nil, uint16(len(es)))
		for _, e := range es {
			out = ord.AppendUint16(out, e.tag)
			out = ord.AppendUint16(out, e.typ)
			out = ord.AppendUint32(out, e.count)
			if e.typ == typeShort && e.count == 1 {
				out = ord.AppendUint16(out, uint16(e.v))
				out = append(out, 0, 0)
			} else {
				out = ord.AppendUint32(out, e.v)
			}
		}
		return value(ord.AppendUint32(out, 0))
	}
	exif := []ent{{0x9003, typeASCII, 20, dtoAt}}
	if makerNote {
		exif = append(exif, ent{tagMakerNote, typeUndef, 12, mnAt})
	}
	exifAt := ifd(exif)
	ifd0At := ifd([]ent{{256, typeShort, 1, 4}, {257, typeShort, 1, 3}, {259, typeShort, 1, 1}, {262, typeShort, 1, 2},
		{270, typeASCII, 16, descAt}, {273, typeLong, 1, uint32(stripAt)}, {277, typeShort, 1, 3},
		{278, typeShort, 1, 3}, {279, typeLong, 1, uint32(len(pixels))}, {tagXMP, typeByte, uint32(len(packet)), xmpAt},
		{tagExifIFD, typeLong, 1, exifAt}})
	order.PutUint32(b[4:], ifd0At)
	return b
}

func tiffHash(t *testing.T, file []byte) [32]byte {
	t.Helper()
	h := sha256.New()
	if _, err := HashImageData(bytes.NewReader(file), int64(len(file)), h); err != nil {
		t.Fatalf("HashImageData: %v", err)
	}
	return [32]byte(h.Sum(nil))
}

func planned(t *testing.T, file []byte, edit MetadataEdit) []byte {
	t.Helper()
	p, err := PlanMetadata(bytes.NewReader(file), int64(len(file)), edit)
	if err != nil {
		t.Fatalf("PlanMetadata: %v", err)
	}
	var buf bytes.Buffer
	if _, err := p.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

var (
	tiffWhen = time.Date(2021, 7, 8, 9, 10, 11, 0, time.FixedZone("", 2*3600))
	tiffGPS  = GPS{Latitude: 52.52, Longitude: 13.405, Altitude: 34, HasAltitude: true}
)

// tiffEdit is every field the importer writes, in Exif and XMP.
func tiffEdit() MetadataEdit {
	return MetadataEdit{
		Exif: &ExifEdit{DateTimeOriginal: &tiffWhen, OffsetKnown: true, GPS: &tiffGPS, ImageDescription: ptr("Calm water"), XPTitle: ptr("Lake")},
		XMP: &XMPEdit{Title: ptr("Lake"), Description: ptr("Calm water"), Keywords: &[]string{"lake", "summer"}, Rating: ptr(4),
			PersonInImage: &[]string{"Ada"}, Regions: &Regions{AppliedTo: Dimensions{W: 4, H: 3, Unit: "pixel"},
				List: []Region{{Name: "Ada", Type: "Face", X: 0.5, Y: 0.5, W: 0.25, H: 0.5}}}},
	}
}

func TestTIFFMetadataEdit(t *testing.T) {
	for _, order := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		src := metaTIFF(order, false)
		hash := tiffHash(t, src)
		out := planned(t, src, tiffEdit())
		m, err := ReadAll(bytes.NewReader(out), int64(len(out)))
		if err != nil || m.Errors.Err() != nil {
			t.Fatalf("%v: ReadAll %v, %v", order, err, m.Errors.Err())
		}
		e, x := m.Exif, m.XMP
		if !e.DateTimeOriginal.Equal(tiffWhen) || !e.OffsetKnown || e.ImageDescription != "Calm water" || e.XPTitle != "Lake" ||
			e.GPS == nil || !samePosition(e.GPS, &tiffGPS) {
			t.Errorf("%v: Exif %+v", order, e)
		}
		if x.Title != "Lake" || x.Description != "Calm water" || strings.Join(x.Keywords, ",") != "lake,summer" || x.Rating != 4 ||
			strings.Join(x.PersonInImage, ",") != "Ada" || x.Regions == nil || len(x.Regions.List) != 1 {
			t.Errorf("%v: XMP %+v", order, x)
		}
		if !m.Taken.Time.Equal(tiffWhen) {
			t.Errorf("%v: taken %+v", order, m.Taken)
		}
		if tiffHash(t, out) != hash {
			t.Errorf("%v: the image data hash changed", order)
		}
		for _, old := range []string{"Old description", "Old title", "2001:02:03"} {
			if bytes.Contains(out, []byte(old)) {
				t.Errorf("%v: %q is still in the file", order, old)
			}
		}
		// An edit that changes nothing is in place, without patches.
		p, err := PlanMetadata(bytes.NewReader(out), int64(len(out)), tiffEdit())
		if err != nil || !p.InPlace || len(p.Patches) != 0 {
			t.Errorf("%v: the same edit again: %+v, %v", order, p, err)
		}
		// A shorter value fits where the old one was: in place.
		p, err = PlanMetadata(bytes.NewReader(out), int64(len(out)), MetadataEdit{Exif: &ExifEdit{ImageDescription: ptr("Calm")}})
		if err != nil || !p.InPlace {
			t.Errorf("%v: a shorter description: %+v, %v", order, p, err)
		}
	}
	// A camera RAW (a TIFF with a maker note) isn't written; IPTC isn't
	// written to a TIFF.
	raw := metaTIFF(binary.LittleEndian, true)
	if _, err := PlanMetadata(bytes.NewReader(raw), int64(len(raw)), tiffEdit()); !errors.Is(err, ErrUnsupported) {
		t.Errorf("a TIFF with a maker note: %v", err)
	}
	src := metaTIFF(binary.LittleEndian, false)
	if _, err := PlanMetadata(bytes.NewReader(src), int64(len(src)), MetadataEdit{IPTC: &IPTCEdit{Title: ptr("x")}}); !errors.Is(err, ErrUnsupported) {
		t.Errorf("IPTC in a TIFF: %v", err)
	}
}

// ExifTool reads what PlanMetadata wrote into TIFFs (synthetic, and
// ffmpeg's) and a DNG, and finds nothing wrong with the files that its
// own write of the same tags doesn't give.
func TestTIFFMetadataEditAgreesWithExifTool(t *testing.T) {
	tool, err := exec.LookPath("exiftool")
	if err != nil {
		t.Skip("exiftool not installed")
	}
	dir := t.TempDir()
	inputs := map[string][]byte{"meta-le.tif": metaTIFF(binary.LittleEndian, false), "meta-be.tif": metaTIFF(binary.BigEndian, false)}
	if ff, err := exec.LookPath("ffmpeg"); err == nil {
		p := filepath.Join(dir, "ffmpeg-src.tif")
		if exec.Command(ff, "-v", "error", "-y", "-f", "lavfi", "-i", "testsrc=size=64x48", "-frames:v", "1", "-c:v", "tiff", p).Run() == nil {
			inputs["ffmpeg.tif"] = readFile(t, p)
		}
	}
	if raw := os.Getenv("MEDIAMETA_RAW_DIR"); raw != "" {
		for _, name := range []string{"dng-camera.dng", "dng-phone.dng", "dng-iphone.dng"} {
			if b, err := os.ReadFile(filepath.Join(raw, name)); err == nil {
				inputs[name] = b
			}
		}
	}
	if len(inputs) < 3 {
		t.Errorf("%d inputs: ffmpeg missing?", len(inputs))
	}
	warnings := func(p string) map[string]bool {
		out, _ := exec.Command(tool, "-validate", "-warning", "-a", "-s3", p).Output()
		w := map[string]bool{}
		for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			// Every warning, not the "N Warnings" summary.
			if l != "" && !strings.Contains(l, " Warning") {
				w[l] = true
			}
		}
		return w
	}
	// The warnings ExifTool's own write of these tags gives a TIFF: a new
	// ExifIFD with the tags it adds, a GPS IFD without a processing method.
	allowed := []string{"ComponentsConfiguration is not allowed in TIFF", "FlashpixVersion", "GPSProcessingMethod"}
	for name, src := range inputs {
		srcPath := filepath.Join(dir, "src-"+name)
		if err := os.WriteFile(srcPath, src, 0o644); err != nil {
			t.Fatal(err)
		}
		before := warnings(srcPath)
		hash := tiffHash(t, src)
		out := planned(t, src, tiffEdit())
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, out, 0o644); err != nil {
			t.Fatal(err)
		}
		if tiffHash(t, out) != hash {
			t.Errorf("%s: the image data hash changed", name)
		}
	next:
		for w := range warnings(p) {
			if before[w] {
				continue
			}
			for _, a := range allowed {
				if strings.Contains(w, a) {
					continue next
				}
			}
			t.Errorf("%s: ExifTool warns %q", name, w)
		}
		res, err := exec.Command(tool, "-j", "-n", "-q", "-ExifIFD:DateTimeOriginal", "-ExifIFD:OffsetTimeOriginal",
			"-GPS:GPSLatitude", "-GPS:GPSLatitudeRef", "-IFD0:ImageDescription", "-IFD0:XPTitle", "-XMP-dc:Title",
			"-XMP-dc:Subject", "-XMP-xmp:Rating", "-XMP-iptcExt:PersonInImage", "-XMP-mwg-rs:RegionName", p).Output()
		if err != nil {
			t.Fatal(err)
		}
		var got []map[string]any
		if err := json.Unmarshal(res, &got); err != nil || len(got) != 1 {
			t.Fatalf("%s: %v\n%s", name, err, res)
		}
		e := got[0]
		for k, want := range map[string]any{"DateTimeOriginal": "2021:07:08 09:10:11", "OffsetTimeOriginal": "+02:00",
			"GPSLatitude": 52.52, "GPSLatitudeRef": "N", "ImageDescription": "Calm water", "XPTitle": "Lake", "Title": "Lake",
			"Rating": 4.0, "PersonInImage": "Ada", "RegionName": "Ada"} {
			if g := e[k]; g != want {
				t.Errorf("%s: ExifTool %s %v (%T), want %v", name, k, g, g, want)
			}
		}
	}
}
