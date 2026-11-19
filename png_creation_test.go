package mediameta

import (
	"bytes"
	"compress/zlib"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func zlibBytes(t *testing.T, s string) []byte {
	t.Helper()
	var b bytes.Buffer
	w := zlib.NewWriter(&b)
	if _, err := w.Write([]byte(s)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// A PNG's Creation Time text chunk, in each chunk type and the date
// forms PNG and its writers use, reads as ExifTool's PNG CreationTime;
// the keyword in any case, as ExifTool matches it.
func TestPNGCreationTime(t *testing.T) {
	plus1 := time.FixedZone("", 3600)
	at := func(loc *time.Location) time.Time { return time.Date(2014, 3, 4, 5, 6, 7, 0, loc) }
	cases := []struct {
		name   string
		chunk  []byte
		want   time.Time
		offset bool
		prec   DatePrecision
	}{
		{"RFC 1123 GMT", pngChunkBytes("tEXt", []byte("Creation Time\x00Mon, 04 Mar 2014 05:06:07 GMT")), at(time.UTC), true, PrecisionSecond},
		{"RFC 1123 numeric zone", pngChunkBytes("tEXt", []byte("Creation Time\x00Mon, 04 Mar 2014 05:06:07 +0100")), at(plus1), true, PrecisionSecond},
		{"RFC 822 without weekday", pngChunkBytes("tEXt", []byte("Creation Time\x004 Mar 2014 05:06:07 +0100")), at(plus1), true, PrecisionSecond},
		{"ISO 8601", pngChunkBytes("tEXt", []byte("Creation Time\x002014-03-04T05:06:07+01:00")), at(plus1), true, PrecisionSecond},
		{"Exif layout", pngChunkBytes("tEXt", []byte("Creation Time\x002014:03:04 05:06:07")), at(time.UTC), false, PrecisionSecond},
		{"date only", pngChunkBytes("tEXt", []byte("Creation Time\x002014-03-04")), time.Date(2014, 3, 4, 0, 0, 0, 0, time.UTC), false, PrecisionDay},
		{"zTXt", pngChunkBytes("zTXt", append([]byte("Creation Time\x00\x00"), zlibBytes(t, "2014-03-04T05:06:07Z")...)), at(time.UTC), true, PrecisionSecond},
		{"iTXt", pngChunkBytes("iTXt", []byte("Creation Time\x00\x00\x00\x00\x002014-03-04T05:06:07Z")), at(time.UTC), true, PrecisionSecond},
		{"lower-case keyword", pngChunkBytes("tEXt", []byte("creation time\x002014-03-04T05:06:07Z")), at(time.UTC), true, PrecisionSecond},
		{"free text", pngChunkBytes("tEXt", []byte("Creation Time\x00March 4, 2014")), time.Time{}, false, 0},
	}
	tool, _ := exec.LookPath("exiftool")
	dir := t.TempDir()
	for _, c := range cases {
		file := pngWithChunks(t, c.chunk)
		got, err := ReadPNGText(bytes.NewReader(file), int64(len(file)))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if c.want.IsZero() {
			if got.Created != nil {
				t.Errorf("%s: %+v, want none", c.name, *got.Created)
			}
		} else if d := got.Created; d == nil || !d.Time.Equal(c.want) || d.OffsetKnown != c.offset || d.Precision != c.prec {
			t.Errorf("%s: %+v, want %v (offset %v, precision %v)", c.name, d, c.want, c.offset, c.prec)
		}
		m, err := ReadAll(bytes.NewReader(file), int64(len(file)))
		if err != nil {
			t.Fatal(err)
		}
		if wantSrc := SourcePNGCreationTime; c.want.IsZero() && m.Taken.Source != SourceNone || !c.want.IsZero() && (m.Taken.Source != wantSrc || !m.Taken.Time.Equal(c.want)) {
			t.Errorf("%s: ReadAll %+v", c.name, m.Taken)
		}
		if tool == "" {
			continue
		}
		p := filepath.Join(dir, "c.png")
		if err := os.WriteFile(p, file, 0o644); err != nil {
			t.Fatal(err)
		}
		out, err := exec.Command(tool, "-j", "-q", "-PNG:CreationTime", p).Output()
		var et []map[string]any
		if err != nil || json.Unmarshal(out, &et) != nil || len(et) != 1 {
			t.Fatalf("%s: ExifTool %s (%v)", c.name, out, err)
		}
		s, _ := et[0]["CreationTime"].(string)
		if s == "" {
			t.Errorf("%s: ExifTool reads no CreationTime", c.name)
			continue
		}
		if c.want.IsZero() {
			continue
		}
		// ExifTool keeps the text, or turns RFC 1123 into the Exif
		// layout with the offset: the same instant.
		d, ok := parseXMPDate(s)
		if !ok || !d.Time.Equal(c.want) {
			t.Errorf("%s: ExifTool %q, this package %v", c.name, s, c.want)
		}
	}
}

// A Title or Description keyword whose words start in lower case reads,
// as ExifTool reads it; one in capitals is another keyword. An XMP title
// edit replaces the one that reads, keeping its case.
func TestPNGTextKeywordCase(t *testing.T) {
	file := pngWithChunks(t, pngChunkBytes("tEXt", []byte("TITLE\x00capitals")), pngChunkBytes("tEXt", []byte("title\x00lower")), pngChunkBytes("tEXt", []byte("description\x00lower too")))
	got, err := ReadPNGText(bytes.NewReader(file), int64(len(file)))
	if err != nil || got.Title != "lower" || got.Description != "lower too" {
		t.Fatalf("%+v, %v", got, err)
	}
	p, err := planXMPEdit(bytes.NewReader(file), int64(len(file)), XMPEdit{Title: ptr("new")})
	if err != nil {
		t.Fatal(err)
	}
	out := written(t, p)
	if after, err := ReadPNGText(bytes.NewReader(out), int64(len(out))); err != nil || after.Title != "new" || after.Description != "lower too" || !bytes.Contains(out, []byte("TITLE\x00capitals")) {
		t.Fatalf("after the edit %+v, %v", after, err)
	}
	if !bytes.Contains(out, []byte("title\x00new")) {
		t.Error("the keyword's case changed")
	}
	if tool, err := exec.LookPath("exiftool"); err == nil {
		path := filepath.Join(t.TempDir(), "k.png")
		if err := os.WriteFile(path, file, 0o644); err != nil {
			t.Fatal(err)
		}
		b, err := exec.Command(tool, "-j", "-q", "-PNG:Title", "-PNG:Description", path).Output()
		var et []map[string]any
		if err != nil || json.Unmarshal(b, &et) != nil || len(et) != 1 || et[0]["Title"] != "lower" || et[0]["Description"] != "lower too" {
			t.Errorf("ExifTool %s (%v)", b, err)
		}
	}
}
