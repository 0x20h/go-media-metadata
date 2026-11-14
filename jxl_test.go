package mediameta

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/andybalholm/brotli"
)

// jxlBits writes a codestream's bits, least significant first.
type jxlBits struct {
	b   []byte
	pos int
}

func (w *jxlBits) put(v uint32, n int) {
	for i := range n {
		if w.pos/8 >= len(w.b) {
			w.b = append(w.b, 0)
		}
		w.b[w.pos/8] |= byte((v>>i)&1) << (w.pos % 8)
		w.pos++
	}
}

// jxlCodestreamOf is a codestream whose header says an 8×8 image shown as
// o (0: default metadata), followed by stand-in image data.
func jxlCodestreamOf(o Orientation) []byte {
	w := &jxlBits{}
	w.put(1, 1) // small
	w.put(0, 5) // ysize_div8_minus1: 8
	w.put(1, 3) // ratio 1:1
	if o == 0 {
		w.put(1, 1) // all_default
	} else {
		w.put(0, 1)           // all_default
		w.put(1, 1)           // extra_fields
		w.put(uint32(o-1), 3) // orientation
	}
	return append(append(append([]byte(nil), jxlCodestream...), w.b...), bytes.Repeat([]byte{0xA5, 0x3C}, 200)...)
}

func jxlBox(typ string, payload []byte) []byte {
	b := binary.BigEndian.AppendUint32(nil, uint32(8+len(payload)))
	return append(append(b, typ...), payload...)
}

func brob(t testing.TB, typ string, content []byte) []byte {
	var buf bytes.Buffer
	w := brotli.NewWriter(&buf)
	if _, err := w.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return jxlBox("brob", append([]byte(typ), buf.Bytes()...))
}

var jxlXMP = []byte(`<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
	`<rdf:Description rdf:about="" xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title><rdf:Alt><rdf:li xml:lang="x-default">A title</rdf:li></rdf:Alt></dc:title>` +
	`</rdf:Description></rdf:RDF></x:xmpmeta>`)

type jxlLayout struct {
	name       string
	compressed bool // brob boxes
	noMeta     bool
	bare       bool // a bare codestream
	parts      bool // jxlp parts instead of jxlc
	jbrd       bool
}

var jxlLayouts = []jxlLayout{
	{name: "plain boxes"},
	{name: "brob boxes", compressed: true},
	{name: "no metadata", noMeta: true},
	{name: "bare codestream", bare: true},
	{name: "jxlp parts", parts: true},
	{name: "jxlp parts, brob", parts: true, compressed: true},
	{name: "JPEG reconstruction", jbrd: true},
}

// buildJXL is a JPEG XL file of layout l whose codestream shows o; it
// returns the file and its codestream.
func buildJXL(t testing.TB, l jxlLayout, o Orientation) (file, cs []byte) {
	cs = jxlCodestreamOf(o)
	if l.bare {
		return cs, cs
	}
	exif := append(make([]byte, 4), photoTIFF(binary.BigEndian)...)
	file = append([]byte(nil), jxlSignature...)
	file = append(file, jxlBox("ftyp", []byte("jxl \x00\x00\x00\x00jxl "))...)
	if !l.noMeta {
		if l.compressed {
			file = append(file, brob(t, "Exif", exif)...)
			file = append(file, brob(t, "xml ", jxlXMP)...)
		} else {
			file = append(file, jxlBox("Exif", exif)...)
			file = append(file, jxlBox("xml ", jxlXMP)...)
		}
	}
	if l.jbrd {
		file = append(file, jxlBox("jbrd", bytes.Repeat([]byte{1}, 16))...)
	}
	if l.parts {
		half := len(cs) / 2
		file = append(file, jxlBox("jxlp", append([]byte{0, 0, 0, 0}, cs[:half]...))...)
		file = append(file, jxlBox("jxlp", append([]byte{0x80, 0, 0, 1}, cs[half:]...))...)
	} else {
		file = append(file, jxlBox("jxlc", cs)...)
	}
	return file, cs
}

// jxlCodestreamBytes is a file's codestream: the jxlc payload, the jxlp
// payloads joined, or the whole of a bare one.
func jxlCodestreamBytes(t *testing.T, file []byte) []byte {
	t.Helper()
	f, err := parseJXL(bytes.NewReader(file), int64(len(file)))
	if err != nil {
		t.Fatal(err)
	}
	if !f.container() {
		return file
	}
	var out []byte
	for _, b := range f.boxes {
		switch string(b.Type[:]) {
		case "jxlc":
			return file[b.Body():b.End()]
		case "jxlp":
			out = append(out, file[b.Body()+4:b.End()]...)
		}
	}
	return out
}

func TestReadJXL(t *testing.T) {
	for _, l := range jxlLayouts {
		file, _ := buildJXL(t, l, 6)
		x, err := ReadExif(bytes.NewReader(file), int64(len(file)))
		if err != nil {
			t.Fatalf("%s: %v", l.name, err)
		}
		o, err := ReadOrientation(bytes.NewReader(file), int64(len(file)))
		if err != nil || o != 6 {
			t.Errorf("%s: orientation %v, %v; want 6, from the codestream", l.name, o, err)
		}
		xmp, err := ReadXMP(bytes.NewReader(file), int64(len(file)))
		if err != nil {
			t.Fatal(err)
		}
		if l.noMeta || l.bare {
			if x.GPS != nil || !x.DateTimeOriginal.IsZero() || xmp != nil {
				t.Errorf("%s: metadata %+v, %q", l.name, x, xmp)
			}
			continue
		}
		checkPhotoExif(t, l.name, x)
		if !bytes.Equal(xmp, jxlXMP) {
			t.Errorf("%s: XMP %q", l.name, xmp)
		}
	}
	// Default metadata: orientation 1.
	file, _ := buildJXL(t, jxlLayouts[0], 0)
	if o, err := ReadOrientation(bytes.NewReader(file), int64(len(file))); err != nil || o != 1 {
		t.Errorf("default metadata: %v, %v", o, err)
	}
}

// Every writer on every layout: the values read back, the codestream is
// byte for byte what it was, and the plan is consistent.
func TestWriteJXL(t *testing.T) {
	date := time.Date(2021, 3, 4, 5, 6, 7, 0, time.UTC)
	long := strings.Repeat("a longer title ", 30)
	short := "T"
	writes := []struct {
		name string
		edit MetadataEdit
		grow bool
	}{
		{"Exif date and place", MetadataEdit{Exif: &ExifEdit{DateTimeOriginal: &date, GPS: &GPS{Latitude: 10, Longitude: 20}}}, false},
		{"XMP longer title", MetadataEdit{XMP: &XMPEdit{Title: &long}}, true},
		{"XMP shorter title", MetadataEdit{XMP: &XMPEdit{Title: &short}}, false},
		{"both", MetadataEdit{Exif: &ExifEdit{DateTimeOriginal: &date}, XMP: &XMPEdit{Title: &long}}, true},
	}
	for _, l := range jxlLayouts {
		for _, w := range writes {
			name := l.name + "/" + w.name
			file, cs := buildJXL(t, l, 6)
			p, err := PlanMetadata(bytes.NewReader(file), int64(len(file)), w.edit)
			if l.jbrd && err != nil {
				if !errors.Is(err, ErrUnsupported) {
					t.Errorf("%s: %v, want ErrUnsupported", name, err)
				}
				continue
			}
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			checkPlan(t, file, p)
			var out bytes.Buffer
			if _, err := p.WriteTo(&out); err != nil {
				t.Fatal(err)
			}
			got := out.Bytes()
			if !bytes.Equal(jxlCodestreamBytes(t, got), cs) {
				t.Fatalf("%s: the codestream changed", name)
			}
			if w.edit.Exif != nil {
				x, err := ReadExif(bytes.NewReader(got), int64(len(got)))
				if err != nil || !x.DateTimeOriginal.Equal(date) {
					t.Errorf("%s: date %v, %v", name, x.DateTimeOriginal, err)
				}
			}
			if w.edit.XMP != nil {
				b, err := ReadXMP(bytes.NewReader(got), int64(len(got)))
				if err != nil {
					t.Fatal(err)
				}
				xm, err := ParseXMP(b)
				if err != nil || xm.Title != *w.edit.XMP.Title {
					t.Errorf("%s: title %q, %v", name, xm.Title, err)
				}
			}
			if o, err := ReadOrientation(bytes.NewReader(got), int64(len(got))); err != nil || o != 6 {
				t.Errorf("%s: orientation %v, %v", name, o, err)
			}
			// A plain box the new data fits is written in place.
			if !l.compressed && !l.noMeta && !l.bare && !w.grow && !p.InPlace {
				t.Errorf("%s: not in place", name)
			}
		}
	}
}

// A file with JPEG reconstruction data takes an edit that keeps the box's
// size.
func TestWriteJXLReconstructionInPlace(t *testing.T) {
	file, cs := buildJXL(t, jxlLayout{jbrd: true}, 1)
	short := "T"
	p, err := PlanMetadata(bytes.NewReader(file), int64(len(file)), MetadataEdit{XMP: &XMPEdit{Title: &short}})
	if err != nil || !p.InPlace {
		t.Fatalf("%v, in place %v", err, p != nil && p.InPlace)
	}
	var out bytes.Buffer
	if _, err := p.WriteTo(&out); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(jxlCodestreamBytes(t, out.Bytes()), cs) {
		t.Fatal("the codestream changed")
	}
}

func FuzzReadJXL(f *testing.F) {
	for _, l := range jxlLayouts {
		file, _ := buildJXL(f, l, 6)
		f.Add(file)
	}
	title := "fuzz"
	f.Fuzz(func(t *testing.T, data []byte) {
		withinDeadline(t, func() {
			_, _ = ReadExif(bytes.NewReader(data), int64(len(data)))
			_, _ = ReadXMP(bytes.NewReader(data), int64(len(data)))
			_, _ = ReadOrientation(bytes.NewReader(data), int64(len(data)))
			if !isJXL(bytes.NewReader(data), int64(len(data))) {
				return
			}
			p, err := PlanMetadata(bytes.NewReader(data), int64(len(data)), MetadataEdit{XMP: &XMPEdit{Title: &title}})
			if err != nil {
				return
			}
			checkPlan(t, data, p)
		})
	})
}

// ExifTool reads what's written, and finds no new problem in the file.
// Runs where exiftool is installed.
func TestJXLExifTool(t *testing.T) {
	if _, err := exec.LookPath("exiftool"); err != nil {
		t.Skip("exiftool not installed")
	}
	dir := t.TempDir()
	date := time.Date(2021, 3, 4, 5, 6, 7, 0, time.UTC)
	// No trailing space: ExifTool trims it.
	title := strings.TrimSpace(strings.Repeat("a longer title ", 30))
	for i, l := range jxlLayouts {
		if l.jbrd {
			continue
		}
		file, _ := buildJXL(t, l, 6)
		p, err := PlanMetadata(bytes.NewReader(file), int64(len(file)), MetadataEdit{
			Exif: &ExifEdit{DateTimeOriginal: &date, GPS: &GPS{Latitude: 10, Longitude: 20}},
			XMP:  &XMPEdit{Title: &title},
		})
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if _, err := p.WriteTo(&out); err != nil {
			t.Fatal(err)
		}
		before, after := filepath.Join(dir, fmt.Sprintf("%d-before.jxl", i)), filepath.Join(dir, fmt.Sprintf("%d.jxl", i))
		if err := os.WriteFile(before, file, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(after, out.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
		b, err := exec.Command("exiftool", "-n", "-s3", "-f", "-DateTimeOriginal", "-GPSLatitude", "-GPSLongitude", "-XMP-dc:Title", after).Output()
		if err != nil {
			t.Fatal(err)
		}
		want := "2021:03:04 05:06:07\n10\n20\n" + title + "\n"
		if string(b) != want {
			t.Errorf("%s: ExifTool reads %q, want %q", l.name, b, want)
		}
		warnings := func(p string) string {
			b, _ := exec.Command("exiftool", "-validate", "-warning", "-error", "-a", "-s3", p).Output()
			return string(b)
		}
		if w0, w1 := warnings(before), warnings(after); w0 != w1 {
			t.Errorf("%s: ExifTool's validation changed:\nbefore: %s\nafter: %s", l.name, w0, w1)
		}
	}
}

// Brotli streams of any content, or none, decompress or fail, within the
// limit.
func FuzzUnbrob(f *testing.F) {
	for _, c := range [][]byte{jxlXMP, photoTIFF(binary.BigEndian), nil} {
		var buf bytes.Buffer
		w := brotli.NewWriter(&buf)
		_, _ = w.Write(c)
		_ = w.Close()
		f.Add(buf.Bytes())
	}
	f.Fuzz(func(t *testing.T, stream []byte) {
		withinDeadline(t, func() {
			out, err := unbrob(stream)
			if err == nil && len(out) > maxJXLMetadata {
				t.Fatalf("%d bytes past the limit", len(out))
			}
		})
	})
}
