package mediameta

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testPNG is a PNG Go encodes: a pattern no metadata edit may change.
func testPNG(t testing.TB) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, 24, 16))
	for y := range 16 {
		for x := range 24 {
			img.Set(x, y, color.NRGBA{uint8(x * 10), uint8(y * 15), 90, uint8(255 - x)})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// pngWithChunks is a PNG with chunks inserted after IHDR.
func pngWithChunks(t testing.TB, chunks ...[]byte) []byte {
	p := testPNG(t)
	ihdrEnd := 8 + 12 + 13
	return bytes.Join([][]byte{p[:ihdrEnd], bytes.Join(chunks, nil), p[ihdrEnd:]}, nil)
}

// chunkedFiles are the PNG and WebP files the tests edit: plain, and
// with metadata already there (Exif with its "Exif\0\0" prefix, XMP
// compressed and, a second time, stale).
func chunkedFiles(t testing.TB) map[string][]byte {
	files := map[string][]byte{"plain.png": testPNG(t)}
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	_, _ = zw.Write(xmpPacket("Old title", 0))
	_ = zw.Close()
	compressed := append([]byte(xmpKeyword+"\x00\x01\x00\x00\x00"), z.Bytes()...)
	files["meta.png"] = pngWithChunks(t,
		pngChunkBytes("eXIf", append(append([]byte(nil), exifHeader...), photoTIFF(binary.BigEndian)...)),
		pngChunkBytes("iTXt", compressed),
		pngChunkBytes("iTXt", xmpITXt(xmpPacket("Stale title", 0))))
	for _, name := range []string{"lossy.webp", "lossless-alpha.webp", "lossy-alpha.webp"} {
		b, err := os.ReadFile(filepath.Join("testdata", "webp", name))
		if err != nil {
			t.Fatal(err)
		}
		files[name] = b
	}
	return files
}

// imageData is a file's image data: a PNG's IDAT chunks, a WebP's image
// chunks (VP8, VP8L, ALPH), byte for byte.
func imageData(t *testing.T, file []byte) []byte {
	t.Helper()
	r := bytes.NewReader(file)
	var out []byte
	if isPNG(r, int64(len(file))) {
		cs, err := pngChunks(r, int64(len(file)))
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range cs {
			if string(c.typ[:]) == "IDAT" || string(c.typ[:]) == "IHDR" {
				out = append(out, file[c.at:c.end()]...)
			}
		}
		return out
	}
	f, err := parseWebP(r, int64(len(file)))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range f.chunks {
		switch string(c.fourcc[:]) {
		case "VP8 ", "VP8L", "ALPH":
			out = append(out, file[c.at:c.end()]...)
		}
	}
	return out
}

// checkChunked checks an edited file's structure: a PNG decodes (Go's
// decoder checks every CRC) to the pixels it had; a WebP's RIFF size is
// its data's, and VP8X says which metadata it holds and keeps the
// canvas.
func checkChunked(t *testing.T, before, after []byte) {
	t.Helper()
	if !bytes.Equal(imageData(t, before), imageData(t, after)) {
		t.Error("the image data changed")
	}
	r := bytes.NewReader(after)
	if isPNG(r, int64(len(after))) {
		a, err := png.Decode(bytes.NewReader(after))
		if err != nil {
			t.Fatalf("Go's PNG decoder: %v", err)
		}
		b, _ := png.Decode(bytes.NewReader(before))
		if !samePixels(a, b) {
			t.Error("the pixels changed")
		}
		return
	}
	f, err := parseWebP(r, int64(len(after)))
	if err != nil {
		t.Fatal(err)
	}
	if f.riffEnd != int64(len(after)) {
		t.Errorf("RIFF data ends at %d, the file at %d", f.riffEnd, len(after))
	}
	if string(f.chunks[0].fourcc[:]) != "VP8X" {
		t.Fatal("no VP8X")
	}
	flags := after[f.chunks[0].data()]
	if (flags&vp8xEXIF != 0) != (len(f.find("EXIF")) > 0) || (flags&vp8xXMP != 0) != (len(f.find("XMP ")) > 0) {
		t.Errorf("VP8X flags %#x for EXIF %d, XMP %d", flags, len(f.find("EXIF")), len(f.find("XMP ")))
	}
	if len(f.find("EXIF")) > 1 || len(f.find("XMP ")) > 1 {
		t.Error("two chunks of a kind")
	}
	bf, _ := parseWebP(bytes.NewReader(before), int64(len(before)))
	w, h := webpCanvas(after, f)
	if string(bf.chunks[0].fourcc[:]) == "VP8X" {
		if bw, bh := webpCanvas(before, bf); bw != w || bh != h {
			t.Errorf("canvas %dx%d, was %dx%d", w, h, bw, bh)
		}
	} else if bw, bh, _, err := bf.canvas(bytes.NewReader(before)); err != nil || bw != w || bh != h {
		t.Errorf("canvas %dx%d, the image is %dx%d (%v)", w, h, bw, bh, err)
	}
}

func webpCanvas(file []byte, f *webpFile) (int, int) {
	d := file[f.chunks[0].data():]
	return 1 + int(d[4]) | int(d[5])<<8 | int(d[6])<<16, 1 + int(d[7]) | int(d[8])<<8 | int(d[9])<<16
}

const chunkSecret = "SECRET-51.5007N-0.1246W"

func TestChunkedMetadata(t *testing.T) {
	for name, file := range chunkedFiles(t) {
		size := int64(len(file))
		t.Run(name, func(t *testing.T) {
			// Exif, then XMP, then the same again (in place).
			p, err := PlanExif(bytes.NewReader(file), size, photoTIFF(binary.LittleEndian))
			if err != nil {
				t.Fatal(err)
			}
			checkPlan(t, file, p)
			withExif := written(t, p)
			checkChunked(t, file, withExif)
			x, err := ReadExif(bytes.NewReader(withExif), int64(len(withExif)))
			if err != nil {
				t.Fatal(err)
			}
			checkPhotoFields(t, name, x)
			if o, err := ReadOrientation(bytes.NewReader(withExif), int64(len(withExif))); err != nil || o != 6 {
				t.Errorf("ReadOrientation %d, %v", o, err)
			}
			packet := xmpPacket("Harbour at dusk", 0)
			p, err = PlanXMP(bytes.NewReader(withExif), int64(len(withExif)), packet)
			if err != nil {
				t.Fatal(err)
			}
			checkPlan(t, withExif, p)
			both := written(t, p)
			checkChunked(t, file, both)
			if got, err := ReadXMP(bytes.NewReader(both), int64(len(both))); err != nil || !bytes.Equal(got, packet) {
				t.Errorf("XMP %q, %v", got, err)
			}
			if bytes.Contains(both, []byte("Stale title")) || bytes.Contains(both, []byte("Old title")) {
				t.Error("an old XMP is still in the file")
			}
			for _, again := range []func() (*Plan, error){
				func() (*Plan, error) { return PlanExif(bytes.NewReader(both), int64(len(both)), photoTIFF(binary.LittleEndian)) },
				func() (*Plan, error) { return PlanXMP(bytes.NewReader(both), int64(len(both)), packet) },
			} {
				p, err := again()
				if err != nil {
					t.Fatal(err)
				}
				if !p.InPlace {
					t.Errorf("the same value again isn't in place (%d → %d)", p.Size, p.NewSize)
				}
				checkPlan(t, both, p)
			}
			// Fields through EditExif, orientation included.
			d := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
			p, err = PlanExifEdit(bytes.NewReader(both), int64(len(both)), ExifEdit{Orientation: 3, DateTimeOriginal: &d})
			if err != nil {
				t.Fatal(err)
			}
			checkPlan(t, both, p)
			edited := written(t, p)
			checkChunked(t, file, edited)
			if x, _ := ReadExif(bytes.NewReader(edited), int64(len(edited))); x.Orientation != 3 || !x.DateTimeOriginal.Equal(d) {
				t.Errorf("edited Exif reads %+v", x)
			}
		})
	}
}

// An old value isn't left in the file, whether the new one fits (in
// place, padded) or not (the chunk rewritten).
func TestChunkedMetadataZeroesOldValues(t *testing.T) {
	oldExif := append(paddedTIFF(100), chunkSecret...)
	oldXMP := xmpPacket(chunkSecret, 400)
	for name, file := range chunkedFiles(t) {
		for _, c := range []struct {
			name    string
			exif    bool
			old     []byte
			new     []byte
			inPlace bool
		}{
			{"exif shorter", true, oldExif, photoTIFF(binary.LittleEndian), true},
			{"exif longer", true, oldExif, paddedTIFF(3000), false},
			{"xmp shorter", false, oldXMP, xmpPacket("x", 0), true},
			{"xmp longer", false, oldXMP, xmpPacket("x", 3000), false},
		} {
			t.Run(name+"/"+c.name, func(t *testing.T) {
				plan := func(f, v []byte) *Plan {
					var p *Plan
					var err error
					if c.exif {
						p, err = PlanExif(bytes.NewReader(f), int64(len(f)), v)
					} else {
						p, err = PlanXMP(bytes.NewReader(f), int64(len(f)), v)
					}
					if err != nil {
						t.Fatal(err)
					}
					return p
				}
				with := written(t, plan(file, c.old))
				if !bytes.Contains(with, []byte(chunkSecret)) {
					t.Fatal("the old value isn't there to begin with")
				}
				p := plan(with, c.new)
				if p.InPlace != c.inPlace {
					t.Errorf("InPlace %v, want %v", p.InPlace, c.inPlace)
				}
				checkPlan(t, with, p)
				out := written(t, p)
				checkChunked(t, file, out)
				if bytes.Contains(out, []byte(chunkSecret)) {
					t.Error("the old value is still in the file")
				}
			})
		}
	}
}

// libwebp's tools accept the edited WebPs (webpinfo finds nothing wrong,
// dwebp decodes them), and ExifTool reads what was written to PNG and
// WebP. Each runs where the tool is installed.
func TestChunkedMetadataWithTools(t *testing.T) {
	dir := t.TempDir()
	written := map[string][]byte{}
	for name, file := range chunkedFiles(t) {
		p, err := PlanExif(bytes.NewReader(file), int64(len(file)), photoTIFF(binary.BigEndian))
		if err != nil {
			t.Fatal(err)
		}
		var b bytes.Buffer
		if _, err := p.WriteTo(&b); err != nil {
			t.Fatal(err)
		}
		p, err = PlanXMP(bytes.NewReader(b.Bytes()), int64(b.Len()), xmpPacket("Harbour at dusk", 0))
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if _, err := p.WriteTo(&out); err != nil {
			t.Fatal(err)
		}
		written[name] = out.Bytes()
		if err := os.WriteFile(filepath.Join(dir, name), out.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ran := false
	if info, err := exec.LookPath("webpinfo"); err == nil {
		ran = true
		for name := range written {
			if !strings.HasSuffix(name, ".webp") {
				continue
			}
			out, err := exec.Command(info, filepath.Join(dir, name)).CombinedOutput()
			if err != nil || !strings.Contains(string(out), "No error detected") {
				t.Errorf("webpinfo %s: %v\n%s", name, err, out)
			}
		}
	}
	if dec, err := exec.LookPath("dwebp"); err == nil {
		ran = true
		decoded := t.TempDir() // not where ExifTool looks
		for name := range written {
			if !strings.HasSuffix(name, ".webp") {
				continue
			}
			if out, err := exec.Command(dec, filepath.Join(dir, name), "-o", filepath.Join(decoded, name+".png")).CombinedOutput(); err != nil {
				t.Errorf("dwebp %s: %v\n%s", name, err, out)
			}
		}
	}
	if tool, err := exec.LookPath("exiftool"); err == nil {
		ran = true
		out, err := exec.Command(tool, "-j", "-n", "-q", "-EXIF:Orientation", "-EXIF:DateTimeOriginal", "-XMP-dc:Title",
			"-Composite:GPSLatitude", "-ext", "png", "-ext", "webp", dir).Output()
		if err != nil {
			t.Fatalf("exiftool: %v", err)
		}
		var got []struct {
			SourceFile       string
			Orientation      int
			DateTimeOriginal string
			Title            string
			GPSLatitude      float64
		}
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatal(err)
		}
		if len(got) != len(written) {
			t.Errorf("exiftool read %d files, want %d", len(got), len(written))
		}
		for _, g := range got {
			if g.Orientation != 6 || g.DateTimeOriginal != "2025:05:04 13:14:15" || g.Title != "Harbour at dusk" || fmt.Sprintf("%.4f", g.GPSLatitude) != "51.5000" {
				t.Errorf("ExifTool reads %+v", g)
			}
		}
	}
	if !ran {
		t.Skip("none of webpinfo, dwebp and exiftool is installed")
	}
}

func TestChunkedErrors(t *testing.T) {
	p := testPNG(t)
	trunc := p[:40]
	if _, err := PlanExif(bytes.NewReader(trunc), int64(len(trunc)), photoTIFF(binary.LittleEndian)); err == nil {
		t.Error("a truncated PNG accepted")
	}
	webp := []byte("RIFF\xff\x00\x00\x00WEBPVP8 ")
	if _, err := PlanXMP(bytes.NewReader(webp), int64(len(webp)), xmpPacket("x", 0)); err == nil {
		t.Error("a RIFF size past the file accepted")
	}
}

func FuzzChunked(f *testing.F) {
	for _, file := range chunkedFiles(f) {
		f.Add(file)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		withinDeadline(t, func() {
			r := bytes.NewReader(data)
			_, _ = ReadExif(r, int64(len(data)))
			_, _ = ReadXMP(r, int64(len(data)))
			_, _ = ReadOrientation(r, int64(len(data)))
			if p, err := PlanExif(r, int64(len(data)), photoTIFF(binary.LittleEndian)); err == nil {
				checkPlan(t, data, p)
			}
			if p, err := PlanXMP(r, int64(len(data)), xmpPacket("x", 0)); err == nil {
				checkPlan(t, data, p)
			}
		})
	})
}

// checkPhotoFields checks photoTIFF's fields: orientation 6, taken
// 2025-05-04 13:14:15.25 at +02:00, at 51°30'N 0°7'30"W.
func checkPhotoFields(t *testing.T, name string, x Exif) {
	t.Helper()
	if x.Orientation != 6 {
		t.Errorf("%s: orientation %d", name, x.Orientation)
	}
	if got := x.DateTimeOriginal.Format("2006-01-02 15:04:05.00 -07:00"); got != "2025-05-04 13:14:15.25 +02:00" || !x.OffsetKnown {
		t.Errorf("%s: taken %s (offset known %v)", name, got, x.OffsetKnown)
	}
	if x.GPS == nil || x.GPS.Latitude != 51.5 || x.GPS.Longitude != -0.125 {
		t.Errorf("%s: GPS %+v", name, x.GPS)
	}
}
