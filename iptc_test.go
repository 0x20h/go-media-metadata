package mediameta

import (
	"bytes"
	"crypto/md5"
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// iim builds IIM data from (record, dataset, value) triples.
func iim(ds ...any) []byte {
	var out []iimDataset
	for i := 0; i < len(ds); i += 3 {
		out = append(out, iimDataset{tag: [2]byte{byte(ds[i].(int)), byte(ds[i+1].(int))}, value: []byte(ds[i+2].(string))})
	}
	return writeIIM(out)
}

// photoshopAPP13 is an APP13 segment holding resources: a thumbnail-ish
// resource Photoshop writes, the IIM, and its digest when digest.
func photoshopAPP13(data []byte, digest bool) []byte {
	rs := []irb{
		{sig: [4]byte{'8', 'B', 'I', 'M'}, id: 0x03ED, name: []byte{0, 0}, data: []byte("resolution-info!")},
		{sig: [4]byte{'8', 'B', 'I', 'M'}, id: irbIPTC, name: []byte{0, 0}, data: data},
	}
	if digest {
		sum := md5.Sum(data)
		rs = append(rs, irb{sig: [4]byte{'8', 'B', 'I', 'M'}, id: irbIPTCMD5, name: []byte{0, 0}, data: sum[:]})
	}
	return app13(append(append([]byte(nil), photoshopHeader...), writeIRBs(rs)...))
}

// latin1IIM is IIM as older writers have it: no character set, Latin-1
// text, a byline this package doesn't touch.
var latin1IIM = iim(2, 0, "\x00\x04", 2, 5, "Gr\xfcn", 2, 25, "See", 2, 25, "Br\xfccke", 2, 80, "Fotograf", 2, 120, "Am See \x80")

func TestParseIPTC(t *testing.T) {
	x, err := ParseIPTC(latin1IIM)
	if err != nil {
		t.Fatal(err)
	}
	if x.Title != "Grün" || x.Description != "Am See €" || !slices.Equal(x.Keywords, []string{"See", "Brücke"}) {
		t.Errorf("got %+v", x)
	}
	utf := iim(1, 90, "\x1b%G", 2, 5, "Grün", 2, 25, "日本")
	if x, _ := ParseIPTC(utf); x.Title != "Grün" || x.Keywords[0] != "日本" {
		t.Errorf("UTF-8: got %+v", x)
	}
	// Even-length padding is fine; an extended length reads.
	ext := append([]byte{0x1C, 2, 120, 0x80, 0x04, 0, 0, 0, 3}, "abc"...)
	if x, err := ParseIPTC(append(ext, 0)); err != nil || x.Description != "abc" {
		t.Errorf("extended length: %+v, %v", x, err)
	}
}

func TestParseIPTCErrors(t *testing.T) {
	for name, b := range map[string][]byte{
		"no tag marker":       {0x1C, 2, 5, 0, 1, 'x', 0x55},
		"length past the end": {0x1C, 2, 5, 0, 9, 'x'},
		"short header":        {0x1C, 2},
		"extended of 0":       {0x1C, 2, 5, 0x80, 0x00},
		"extended past":       {0x1C, 2, 5, 0x80, 0x04, 0xFF, 0xFF, 0xFF, 0xFF},
	} {
		if _, err := ParseIPTC(b); !errors.Is(err, ErrMalformed) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestEditIPTC(t *testing.T) {
	out, err := EditIPTC(latin1IIM, IPTCEdit{Title: ptr("Blau"), Keywords: &[]string{"Wasser", " ", "Ufer"}})
	if err != nil {
		t.Fatal(err)
	}
	x, _ := ParseIPTC(out)
	if x.Title != "Blau" || x.Description != "Am See €" || !slices.Equal(x.Keywords, []string{"Wasser", "Ufer"}) {
		t.Errorf("got %+v", x)
	}
	ds, _ := parseIIM(out)
	// UTF-8 declared first, and the Latin-1 text re-encoded and kept.
	if !iimIsUTF8(ds) || ds[0].tag != iimCharset {
		t.Errorf("1:90 not first or not UTF-8: %v", ds[0])
	}
	if !bytes.Contains(out, []byte("Fotograf")) || !bytes.Contains(out, []byte("Am See €")) {
		t.Errorf("lost or kept as Latin-1: %q", out)
	}
	// Removing; a no-op returns the bytes as they were.
	out, _ = EditIPTC(out, IPTCEdit{Title: ptr(""), Keywords: &[]string{}, Description: ptr("")})
	if x, _ := ParseIPTC(out); x.Title != "" || x.Keywords != nil || x.Description != "" {
		t.Errorf("not removed: %+v", x)
	}
	same, _ := EditIPTC(latin1IIM, IPTCEdit{Title: ptr("Grün"), Keywords: &[]string{"See", "Brücke"}})
	if !bytes.Equal(same, latin1IIM) {
		t.Error("an edit that changes nothing changed the data")
	}
	// From nothing.
	out, _ = EditIPTC(nil, IPTCEdit{Title: ptr("T")})
	if x, _ := ParseIPTC(out); x.Title != "T" {
		t.Errorf("new: %+v", x)
	}
	if ds, _ := parseIIM(out); !hasRecord(ds, iimVersion) {
		t.Error("no 2:00 record version")
	}
}

// Values past IIM's limits are cut at a character boundary.
func TestEditIPTCLimits(t *testing.T) {
	long := strings.Repeat("ä", 40) // 80 bytes
	out, _ := EditIPTC(nil, IPTCEdit{Title: &long, Keywords: &[]string{long}, Description: ptr(strings.Repeat("x", 3000))})
	x, _ := ParseIPTC(out)
	if len(x.Title) != 64 || x.Title != strings.Repeat("ä", 32) || len(x.Keywords[0]) != 64 || len(x.Description) != 2000 {
		t.Errorf("title %d bytes, keyword %d, description %d", len(x.Title), len(x.Keywords[0]), len(x.Description))
	}
}

func setIPTC(t *testing.T, file, data []byte) ([]byte, *Plan) {
	t.Helper()
	p, err := PlanIPTC(bytes.NewReader(file), int64(len(file)), data)
	if err != nil {
		t.Fatalf("PlanIPTC: %v", err)
	}
	checkPlan(t, file, p)
	var out bytes.Buffer
	if err := SetIPTC(bytes.NewReader(file), int64(len(file)), &out, data); err != nil {
		t.Fatal(err)
	}
	checkJPEGEdit(t, file, out.Bytes())
	return out.Bytes(), p
}

func readIPTC(t *testing.T, file []byte) []byte {
	t.Helper()
	b, err := ReadIPTC(bytes.NewReader(file), int64(len(file)))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The other resources stay, the digest follows the data; a JPEG without
// APP13 gets one after its APPn segments; empty data removes the IIM and
// its digest.
func TestSetIPTCJPEG(t *testing.T) {
	src := withSegments(plainJPEG(t), exifSegment(photoTIFF(binary.BigEndian)), photoshopAPP13(latin1IIM, true))
	edited, _ := EditIPTC(readIPTC(t, src), IPTCEdit{Title: ptr("Neu")})
	out, _ := setIPTC(t, src, edited)
	if got := readIPTC(t, out); !bytes.Equal(got, edited) {
		t.Fatalf("read back %q", got)
	}
	f, _ := parseJPEG(bytes.NewReader(out), int64(len(out)))
	_, rs, err := f.photoshopSegments(bytes.NewReader(out))
	if err != nil || len(rs) != 3 || rs[0].id != 0x03ED || string(rs[0].data) != "resolution-info!" {
		t.Fatalf("resources %v, %v", rs, err)
	}
	if sum := md5.Sum(edited); !bytes.Equal(rs[2].data, sum[:]) {
		t.Error("the IPTC digest wasn't updated")
	}
	if bytes.Contains(out, []byte("Gr\xfcn")) {
		t.Error("the old title is still in the file")
	}
	if x := readExif(t, out); x.Orientation != 6 {
		t.Error("the Exif changed")
	}

	// Same length: in place.
	again, _ := EditIPTC(edited, IPTCEdit{Title: ptr("Alt")})
	if _, plan := setIPTC(t, out, again); !plan.InPlace {
		t.Error("a same-length edit isn't in place")
	}

	// No APP13: a new one after the APPn segments, before the tables.
	plain := withSegments(plainJPEG(t), exifSegment(photoTIFF(binary.BigEndian)))
	out, plan := setIPTC(t, plain, iim(2, 5, "T"))
	if plan.InPlace {
		t.Error("adding a segment is in place")
	}
	f, _ = parseJPEG(bytes.NewReader(out), int64(len(out)))
	if f.segs[0].marker != markerAPP1 || f.segs[1].marker != markerAPP13 {
		t.Errorf("segments %x, %x: want APP1 then APP13", f.segs[0].marker, f.segs[1].marker)
	}

	// Removing the IIM removes its digest; the other resource stays.
	out, _ = setIPTC(t, src, nil)
	if b := readIPTC(t, out); b != nil {
		t.Errorf("IIM still there: %q", b)
	}
	f, _ = parseJPEG(bytes.NewReader(out), int64(len(out)))
	if _, rs, _ := f.photoshopSegments(bytes.NewReader(out)); len(rs) != 1 || rs[0].id != 0x03ED {
		t.Errorf("resources %v", rs)
	}
}

func TestReadIPTCWithoutIPTC(t *testing.T) {
	if b := readIPTC(t, plainJPEG(t)); b != nil {
		t.Errorf("got %q", b)
	}
	png := []byte("\x89PNG\r\n\x1a\n")
	if _, err := ReadIPTC(bytes.NewReader(png), int64(len(png))); !errors.Is(err, errNotJPEG) {
		t.Errorf("a PNG: %v", err)
	}
}

// ExifTool reads what SetIPTC writes, finds the digest current, and its
// validation adds no warning.
func TestIPTCAgreesWithExifTool(t *testing.T) {
	tool, err := exec.LookPath("exiftool")
	if err != nil {
		t.Skip("exiftool not installed")
	}
	src := withSegments(plainJPEG(t), photoshopAPP13(latin1IIM, true))
	edited, _ := EditIPTC(latin1IIM, IPTCEdit{Title: ptr("Grün & Blau"), Keywords: &[]string{"日本", "See"}, Description: ptr("Neu")})
	out, _ := setIPTC(t, src, edited)
	dir := t.TempDir()
	for name, b := range map[string][]byte{"source.jpg": src, "edited.jpg": out} {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	j, err := exec.Command(tool, "-j", "-q", "-charset", "iptc=latin", "-IPTC:ObjectName", "-IPTC:Keywords", "-IPTC:Caption-Abstract",
		"-IPTC:By-line", "-IPTCDigest", "-CurrentIPTCDigest", "-warning", dir).Output()
	if err != nil {
		t.Fatal(err)
	}
	var got []map[string]any
	if err := json.Unmarshal(j, &got); err != nil {
		t.Fatal(err)
	}
	for _, e := range got {
		name := filepath.Base(e["SourceFile"].(string))
		want, _ := ParseIPTC(readIPTC(t, map[string][]byte{"source.jpg": src, "edited.jpg": out}[name]))
		var kws []string
		switch v := e["Keywords"].(type) {
		case []any:
			for _, k := range v {
				kws = append(kws, k.(string))
			}
		case string:
			kws = []string{v}
		}
		if e["ObjectName"] != want.Title || e["Caption-Abstract"] != want.Description || !slices.Equal(kws, want.Keywords) || e["By-line"] != "Fotograf" {
			t.Errorf("%s: ExifTool %v, ParseIPTC %+v", name, e, want)
		}
		if e["IPTCDigest"] != e["CurrentIPTCDigest"] || e["Warning"] != nil {
			t.Errorf("%s: digest %v, current %v, warning %v", name, e["IPTCDigest"], e["CurrentIPTCDigest"], e["Warning"])
		}
	}
}

func FuzzIPTC(f *testing.F) {
	f.Add(latin1IIM)
	f.Add(iim(1, 90, "\x1b%G", 2, 5, "Grün"))
	f.Fuzz(func(t *testing.T, data []byte) {
		withinDeadline(t, func() {
			before, err := ParseIPTC(data)
			if err != nil {
				return
			}
			out, err := EditIPTC(data, IPTCEdit{Title: ptr("T"), Keywords: &[]string{"k"}})
			if err != nil {
				t.Fatalf("EditIPTC of data ParseIPTC reads: %v", err)
			}
			after, err := ParseIPTC(out)
			if err != nil || after.Title != "T" || !slices.Equal(after.Keywords, []string{"k"}) || after.Description != before.Description {
				t.Fatalf("got %+v (%v), the description was %q", after, err, before.Description)
			}
			jpg := withSegments(plainJPEG(t), photoshopAPP13(data, true))
			if p, err := PlanIPTC(bytes.NewReader(jpg), int64(len(jpg)), out); err == nil {
				checkPlan(t, jpg, p)
			}
		})
	})
}
