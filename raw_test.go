package mediameta

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// rawXMP is an XMP packet saying the orientation is 6, as Lightroom
// writes it into a DNG.
var rawXMP = []byte(`<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
	`<rdf:Description rdf:about="" xmlns:tiff="http://ns.adobe.com/tiff/1.0/" tiff:Orientation="6"/></rdf:RDF></x:xmpmeta>`)

// subIFD is the IFD of photoTIFF at off as builder entries, sorted.
func subIFD(order byteOrder, off func(map[uint16]field, tiffReader) uint32) []tiffEntry {
	ph := photoTIFF(order)
	t, first, _ := newTIFF(ph)
	var out []tiffEntry
	for tag, f := range t.ifd(off(t.ifd(first), t)) {
		out = append(out, tiffEntry{tag, f.typ, uint32(f.count), f.value})
	}
	sortEntries(out)
	return out
}

func exifOf(ifd0 map[uint16]field, t tiffReader) uint32 { v, _ := t.uint(ifd0[tagExifIFD]); return v }
func gpsOf(ifd0 map[uint16]field, t tiffReader) uint32  { v, _ := t.uint(ifd0[tagGPSIFD]); return v }

// rawTIFFFile is a TIFF-based RAW with photoTIFF's fields: IFD0 with a
// Make, the orientation (SHORT or LONG) and the XMP packet; the Exif IFD
// with a maker note; the GPS IFD; then image data.
func rawTIFFFile(order byteOrder, orientationType uint16, extra ...tiffEntry) []byte {
	b := &tiffBuilder{order: order}
	orient := b.short(6)
	if orientationType == typeLong {
		orient = b.long(6)
	}
	b.ifd0 = append([]tiffEntry{
		{0x010F, typeASCII, 6, []byte("Canon\x00")},
		{tagOrientation, orientationType, 1, orient},
		{tagXMP, typeByte, uint32(len(rawXMP)), rawXMP},
	}, extra...)
	sortEntries(b.ifd0)
	b.exif = append(subIFD(order, exifOf), tiffEntry{tagMakerNote, typeUndef, 32, bytes.Repeat([]byte{0xC4}, 32)})
	sortEntries(b.exif)
	b.gp = subIFD(order, gpsOf)
	return append(b.build(), bytes.Repeat([]byte{0x3C}, 4096)...)
}

// rw2 is a Panasonic RW2: header "IIU\0", IFD0 with the orientation, the
// XMP and the Exif in an embedded JPEG (JpgFromRaw), no Exif IFD.
func rw2(t testing.TB) []byte {
	b := &tiffBuilder{order: binary.LittleEndian}
	jpg := withSegments(plainJPEG(t), exifSegment(photoTIFF(binary.LittleEndian)))
	b.ifd0 = []tiffEntry{
		{tagJpgFromRaw, typeUndef, uint32(len(jpg)), jpg},
		{tagOrientation, typeShort, 1, b.short(6)},
		{tagXMP, typeByte, uint32(len(rawXMP)), rawXMP},
	}
	out := b.build()
	out[2], out[3] = 'U', 0
	return out
}

// cr3File is a Canon CR3: ftyp "crx ", moov holding Canon's metadata
// uuid box with CMT1 (IFD0), CMT2 (Exif), CMT3 (maker note) and CMT4
// (GPS), the XMP uuid box, and media data.
func cr3File() []byte {
	box := func(typ string, body ...[]byte) []byte {
		b := bytes.Join(body, nil)
		return append(append(binary.BigEndian.AppendUint32(nil, uint32(8+len(b))), typ...), b...)
	}
	tiffOf := func(es []tiffEntry) []byte {
		return (&tiffBuilder{order: binary.BigEndian, ifd0: es}).build()
	}
	c1 := []tiffEntry{{0x010F, typeASCII, 6, []byte("Canon\x00")}, {tagOrientation, typeShort, 1, binary.BigEndian.AppendUint16(nil, 6)}}
	meta := box("uuid", cr3MetaUUID[:], box("CMT1", tiffOf(c1)), box("CMT2", tiffOf(subIFD(binary.BigEndian, exifOf))),
		box("CMT3", bytes.Repeat([]byte{0xC4}, 40)), box("CMT4", tiffOf(subIFD(binary.BigEndian, gpsOf))))
	return bytes.Join([][]byte{
		box("ftyp", []byte("crx \x00\x00\x00\x01crx isom")),
		box("moov", meta),
		box("uuid", cr3XMPUUID[:], rawXMP),
		box("mdat", bytes.Repeat([]byte{0x3C}, 4096)),
	}, nil)
}

// rawFiles are the synthetic RAW files, named with the extension ExifTool
// reads each as.
func rawFiles(t testing.TB) map[string][]byte {
	orf := rawTIFFFile(binary.LittleEndian, typeShort)
	orf[2], orf[3] = 'R', 'O'
	return map[string][]byte{
		"ii.dng": rawTIFFFile(binary.LittleEndian, typeShort),
		// A DNG proper: DNGVersion 1.4, as ExifTool tells DNG from TIFF.
		"dngversion.dng": rawTIFFFile(binary.LittleEndian, typeShort, tiffEntry{0xC612, typeByte, 4, []byte{1, 4, 0, 0}}),
		"mm.nef":         rawTIFFFile(binary.BigEndian, typeShort),
		"long.arw":       rawTIFFFile(binary.LittleEndian, typeLong),
		"olympus.orf":    orf,
		"panasonic.rw2":  rw2(t),
		"canon.cr3":      cr3File(),
	}
}

// checkPhotoExif checks photoTIFF's fields: orientation 6, taken
// 2025-05-04 13:14:15.25 at +02:00, at 51°30'N 0°7'30"W.
func checkPhotoExif(t *testing.T, name string, x Exif) {
	t.Helper()
	if x.Orientation != 6 {
		t.Errorf("%s: orientation %d", name, x.Orientation)
	}
	if got := x.DateTimeOriginal.Format("2006-01-02 15:04:05.00 -07:00"); got != "2025-05-04 13:14:15.25 +02:00" || !x.OffsetKnown {
		t.Errorf("%s: taken %s (offset known %v)", name, got, x.OffsetKnown)
	}
	if x.GPS == nil || math.Abs(x.GPS.Latitude-51.5) > 1e-9 || math.Abs(x.GPS.Longitude+0.125) > 1e-9 {
		t.Errorf("%s: GPS %+v", name, x.GPS)
	}
}

func TestReadRAW(t *testing.T) {
	for name, file := range rawFiles(t) {
		r := bytes.NewReader(file)
		x, err := ReadExif(r, int64(len(file)))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		checkPhotoExif(t, name, x)
		if o, err := ReadOrientation(r, int64(len(file))); err != nil || o != 6 {
			t.Errorf("%s: ReadOrientation %d, %v", name, o, err)
		}
		if xmp, err := ReadXMP(r, int64(len(file))); err != nil || !bytes.Equal(xmp, rawXMP) {
			t.Errorf("%s: XMP %q, %v", name, xmp, err)
		}
	}
}

// Setting the orientation changes the Orientation value and the XMP's
// digit, in place, and nothing else.
func TestPlanRAWOrientation(t *testing.T) {
	for name, file := range rawFiles(t) {
		for o := Orientation(1); o <= 8; o++ {
			t.Run(fmt.Sprintf("%s/%d", name, o), func(t *testing.T) {
				p, err := PlanRAWOrientation(bytes.NewReader(file), int64(len(file)), o)
				if err != nil {
					t.Fatal(err)
				}
				if !p.InPlace {
					t.Fatal("not in place")
				}
				checkPlan(t, file, p)
				n := 0
				for _, pt := range p.Patches {
					n += len(pt.Data)
				}
				if n > 5 {
					t.Errorf("%d bytes patched", n)
				}
				out := written(t, p)
				if got, err := ReadOrientation(bytes.NewReader(out), int64(len(out))); err != nil || got != o {
					t.Errorf("reads back %d, %v", got, err)
				}
				xmp, _ := ReadXMP(bytes.NewReader(out), int64(len(out)))
				if !bytes.Contains(xmp, []byte(fmt.Sprintf(`tiff:Orientation="%d"`, o))) {
					t.Errorf("the XMP says %q", xmp)
				}
				var direct bytes.Buffer
				if err := SetRAWOrientation(bytes.NewReader(file), int64(len(file)), &direct, o); err != nil || !bytes.Equal(direct.Bytes(), out) {
					t.Errorf("SetRAWOrientation differs: %v", err)
				}
			})
		}
	}
}

func TestPlanRAWOrientationErrors(t *testing.T) {
	file := rawTIFFFile(binary.LittleEndian, typeShort)
	if _, err := PlanRAWOrientation(bytes.NewReader(file), int64(len(file)), 9); err == nil {
		t.Error("orientation 9 accepted")
	}
	b := &tiffBuilder{order: binary.LittleEndian, ifd0: []tiffEntry{{0x010F, typeASCII, 6, []byte("Canon\x00")}}}
	none := append(b.build(), make([]byte, 64)...)
	if _, err := PlanRAWOrientation(bytes.NewReader(none), int64(len(none)), 3); !errors.Is(err, ErrUnsupported) {
		t.Errorf("no Orientation tag: %v", err)
	}
	jpg := plainJPEG(t)
	if _, err := PlanRAWOrientation(bytes.NewReader(jpg), int64(len(jpg)), 3); !errors.Is(err, ErrNotRAW) {
		t.Errorf("a JPEG: %v", err)
	}
	heic := planFiles(t)[0].data
	if _, err := PlanRAWOrientation(bytes.NewReader(heic), int64(len(heic)), 3); !errors.Is(err, ErrNotRAW) {
		t.Errorf("a HEIF file: %v", err)
	}
	garbage := []byte("not an image file at all")
	if _, err := ReadExif(bytes.NewReader(garbage), int64(len(garbage))); !errors.Is(err, ErrNotRAW) || !errors.Is(err, ErrNotJPEG) {
		t.Errorf("garbage: %v", err)
	}
}

// ExifTool reads the synthetic RAW files, and the orientation written,
// as this package does. Runs where exiftool is installed.
func TestRAWAgreesWithExifTool(t *testing.T) {
	tool, err := exec.LookPath("exiftool")
	if err != nil {
		t.Skip("exiftool not installed")
	}
	dir := t.TempDir()
	files := rawFiles(t)
	for name, file := range files {
		p, err := PlanRAWOrientation(bytes.NewReader(file), int64(len(file)), 8)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), written(t, p), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out, err := exec.Command(tool, "-j", "-n", "-q", "-G1", "-Orientation", "-DateTimeOriginal", "-Composite:GPSLatitude", dir).Output()
	if err != nil {
		t.Fatalf("exiftool: %v", err)
	}
	var got []map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(files) {
		t.Fatalf("exiftool read %d files, want %d", len(got), len(files))
	}
	for _, g := range got {
		name := filepath.Base(fmt.Sprint(g["SourceFile"]))
		orient, date, lat := []string{}, "", ""
		for k, v := range g {
			switch {
			case strings.HasSuffix(k, ":Orientation"):
				orient = append(orient, k+"="+fmt.Sprint(v))
				if fmt.Sprint(v) != "8" {
					t.Errorf("%s: ExifTool %s = %v", name, k, v)
				}
			case strings.HasSuffix(k, ":DateTimeOriginal"):
				date = fmt.Sprint(v)
			case strings.HasSuffix(k, ":GPSLatitude"):
				lat = fmt.Sprint(v)
			}
		}
		if len(orient) == 0 || !strings.HasPrefix(date, "2025:05:04 13:14:15") || !strings.HasPrefix(lat, "51.5") {
			t.Errorf("%s: ExifTool reads orientation %v, date %q, latitude %q", name, orient, date, lat)
		}
	}
}

// TestRealRAWFiles reads RAW files outside the repository and plans an
// orientation for each: set MEDIAMETA_RAW_DIR to a folder of them
// (scripts/fetch-raw-samples.sh fetches a CC0 set). No file is written.
func TestRealRAWFiles(t *testing.T) {
	dir := os.Getenv("MEDIAMETA_RAW_DIR")
	if dir == "" {
		t.Skip("set MEDIAMETA_RAW_DIR to test real RAW files")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		switch strings.ToLower(filepath.Ext(e.Name())) {
		case ".dng", ".cr2", ".cr3", ".nef", ".arw", ".orf", ".rw2", ".raf":
		default:
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		x, err := ReadExif(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			t.Errorf("%s: %v", e.Name(), err)
		}
		t.Logf("%s: %+v", e.Name(), x)
		if ps, err := ReadPreviews(bytes.NewReader(data), int64(len(data))); err == nil {
			t.Logf("%s: previews %+v", e.Name(), ps)
		}
		p, err := PlanRAWOrientation(bytes.NewReader(data), int64(len(data)), 3)
		if err != nil {
			t.Logf("%s: %v", e.Name(), err)
			continue
		}
		t.Run(e.Name(), func(t *testing.T) { checkPlan(t, data, p) })
		out := written(t, p)
		if o, _ := ReadOrientation(bytes.NewReader(out), int64(len(out))); o != 3 {
			t.Errorf("%s: orientation %d after setting 3", e.Name(), o)
		}
	}
}

func FuzzReadRAW(f *testing.F) {
	for _, file := range rawFiles(f) {
		f.Add(file)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		withinDeadline(t, func() {
			r := bytes.NewReader(data)
			_, _ = ReadExif(r, int64(len(data)))
			_, _ = ReadOrientation(r, int64(len(data)))
			_, _ = ReadXMP(r, int64(len(data)))
			if p, err := PlanRAWOrientation(r, int64(len(data)), 3); err == nil {
				checkPlan(t, data, p)
			}
		})
	})
}

// TestRealRAWAgreesWithExifTool compares the real files of
// MEDIAMETA_RAW_DIR with ExifTool: the date taken, the orientation and the
// GPS position, and every JPEG preview ExifTool extracts is one
// ReadPreviews finds (the same bytes, or those followed only by the zero
// padding of the box they're in, which ExifTool keeps).
func TestRealRAWAgreesWithExifTool(t *testing.T) {
	dir := os.Getenv("MEDIAMETA_RAW_DIR")
	if dir == "" {
		t.Skip("set MEDIAMETA_RAW_DIR to test real RAW files")
	}
	if _, err := exec.LookPath("exiftool"); err != nil {
		t.Skip("exiftool not installed")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		switch strings.ToLower(filepath.Ext(e.Name())) {
		case ".dng", ".cr2", ".cr3", ".nef", ".arw", ".orf", ".rw2", ".raf":
		default:
			continue
		}
		t.Run(e.Name(), func(t *testing.T) {
			path := filepath.Join(dir, e.Name())
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			x, err := ReadExif(bytes.NewReader(data), int64(len(data)))
			if err != nil {
				t.Fatal(err)
			}
			o, err := ReadOrientation(bytes.NewReader(data), int64(len(data)))
			if err != nil {
				t.Fatal(err)
			}
			out, err := exec.Command("exiftool", "-n", "-s3", "-f", "-DateTimeOriginal", "-Orientation", "-GPSLatitude", "-GPSLongitude", path).Output()
			if err != nil {
				t.Fatal(err)
			}
			f := strings.Split(strings.TrimSpace(string(out)), "\n")
			if len(f) != 4 {
				t.Fatalf("ExifTool: %q", out)
			}
			if f[0] != "-" && !strings.HasPrefix(f[0], x.DateTimeOriginal.Format("2006:01:02 15:04:05")) {
				t.Errorf("date taken %v, ExifTool %s", x.DateTimeOriginal, f[0])
			}
			if want := f[1]; want != "-" && fmt.Sprint(int(o)) != want {
				t.Errorf("orientation %d, ExifTool %s", o, want)
			}
			if f[2] != "-" {
				if x.GPS == nil || fmt.Sprintf("%.6f,%.6f", x.GPS.Latitude, x.GPS.Longitude) != func() string {
					var a, b float64
					_, _ = fmt.Sscan(f[2], &a)
					_, _ = fmt.Sscan(f[3], &b)
					return fmt.Sprintf("%.6f,%.6f", a, b)
				}() {
					t.Errorf("GPS %+v, ExifTool %s,%s", x.GPS, f[2], f[3])
				}
			}
			ps, err := ReadPreviews(bytes.NewReader(data), int64(len(data)))
			if err != nil {
				t.Fatal(err)
			}
			for _, tag := range []string{"PreviewImage", "JpgFromRaw", "ThumbnailImage", "OtherImage"} {
				got, err := exec.Command("exiftool", "-b", "-"+tag, path).Output()
				if err != nil || len(got) < 4 || got[0] != 0xFF || got[1] != 0xD8 {
					continue
				}
				found := false
				for _, p := range ps {
					ours := data[p.Offset : p.Offset+p.Length]
					found = found || bytes.Equal(got, ours) ||
						bytes.HasPrefix(got, ours) && len(bytes.Trim(got[len(ours):], "\x00")) == 0
				}
				if !found {
					t.Errorf("ExifTool's %s (%d bytes) isn't one of the previews found: %+v", tag, len(got), ps)
				}
			}
			t.Logf("%d previews: %+v", len(ps), ps)
		})
	}
}
