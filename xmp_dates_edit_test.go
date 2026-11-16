package mediameta

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

const datesBase = `<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
	`<rdf:Description rdf:about="" xmlns:photoshop="http://ns.adobe.com/photoshop/1.0/" photoshop:DateCreated="2001-02-03T04:05:06"/>` +
	`</rdf:RDF></x:xmpmeta>`

var plus2 = time.FixedZone("", 2*3600)

// editDates are the dates TestEditXMPDates writes: every precision, with
// and without an offset, a fraction, a negative and a zero offset.
var editDates = []Date{
	{Time: time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC), Precision: PrecisionYear},
	{Time: time.Date(2021, 7, 1, 0, 0, 0, 0, time.UTC), Precision: PrecisionMonth},
	{Time: time.Date(2021, 7, 8, 0, 0, 0, 0, time.UTC), Precision: PrecisionDay},
	{Time: time.Date(2021, 7, 8, 9, 10, 0, 0, plus2), OffsetKnown: true, Precision: PrecisionMinute},
	{Time: time.Date(2021, 7, 8, 9, 10, 11, 0, time.UTC), Precision: PrecisionSecond},
	{Time: time.Date(2021, 7, 8, 9, 10, 11, 250_000_000, plus2), OffsetKnown: true, Precision: PrecisionSecond},
	{Time: time.Date(1999, 12, 31, 23, 59, 59, 0, time.FixedZone("", -(5*3600+30*60))), OffsetKnown: true, Precision: PrecisionSecond},
	{Time: time.Date(2021, 7, 8, 9, 10, 11, 0, time.UTC), OffsetKnown: true, Precision: PrecisionSecond},
	{Time: time.Date(2021, 7, 8, 9, 10, 11, 0, time.UTC)}, // no precision: to the second
}

func sameDate(a, b *Date) bool {
	if a == nil || b == nil {
		return a == b
	}
	pa, pb := a.Precision, b.Precision
	if pa == 0 {
		pa = PrecisionSecond
	}
	if pb == 0 {
		pb = PrecisionSecond
	}
	return a.Time.Equal(b.Time) && a.OffsetKnown == b.OffsetKnown && pa == pb &&
		a.Time.Format("15:04:05") == b.Time.Format("15:04:05") // the clock time, not only the instant
}

func TestEditXMPDates(t *testing.T) {
	for _, want := range editDates {
		for _, f := range []struct {
			name string
			set  func(*XMPEdit, *Date)
			get  func(XMP) *Date
		}{
			{"DateCreated", func(e *XMPEdit, d *Date) { e.DateCreated = d }, func(x XMP) *Date { return x.DateCreated }},
			{"CreateDate", func(e *XMPEdit, d *Date) { e.CreateDate = d }, func(x XMP) *Date { return x.CreateDate }},
			{"DateTimeOriginal", func(e *XMPEdit, d *Date) { e.DateTimeOriginal = d }, func(x XMP) *Date { return x.DateTimeOriginal }},
			{"ModifyDate", func(e *XMPEdit, d *Date) { e.ModifyDate = d }, func(x XMP) *Date { return x.ModifyDate }},
		} {
			var e XMPEdit
			d := want
			f.set(&e, &d)
			out, err := EditXMP([]byte(datesBase), e)
			if err != nil {
				t.Fatalf("%s %+v: %v", f.name, want, err)
			}
			x := parseXMP(t, out)
			if got := f.get(x); !sameDate(got, &want) {
				t.Errorf("%s: wrote %+v, read %+v\n%s", f.name, want, got, out)
			}
			if f.name != "DateCreated" && (x.DateCreated == nil || x.DateCreated.Time.Year() != 2001) {
				t.Errorf("%s: DateCreated changed: %+v", f.name, x.DateCreated)
			}
		}
	}
	// A zero Time removes; the same date again changes nothing.
	out, err := EditXMP([]byte(datesBase), XMPEdit{DateCreated: &Date{}})
	if err != nil || parseXMP(t, out).DateCreated != nil {
		t.Errorf("removal: %v\n%s", err, out)
	}
	again, err := EditXMP([]byte(datesBase), XMPEdit{DateCreated: &Date{Time: time.Date(2001, 2, 3, 4, 5, 6, 0, time.UTC), Precision: PrecisionSecond}})
	if err != nil || !bytes.Equal(again, []byte(datesBase)) {
		t.Errorf("the same date rewrote the packet: %v\n%s", err, again)
	}
	// Dates XMP can't hold.
	for _, bad := range []Date{
		{Time: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)},
		{Time: time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC), Precision: 9},
		{Time: time.Date(2021, 1, 1, 0, 0, 0, 0, time.FixedZone("", 30)), OffsetKnown: true},
	} {
		if _, err := EditXMP([]byte(datesBase), XMPEdit{CreateDate: &bad}); err == nil {
			t.Errorf("%+v written", bad)
		}
	}
	// With the Exif date in one edit, TakenFrom gives the new date.
	when := time.Date(2021, 7, 8, 9, 10, 11, 0, plus2)
	file := readFile(t, "testdata/exiftool/xmp-where-when.jpg")
	p, err := PlanMetadata(bytes.NewReader(file), int64(len(file)), MetadataEdit{
		Exif: &ExifEdit{DateTimeOriginal: &when, OffsetKnown: true},
		XMP:  &XMPEdit{DateCreated: &Date{Time: when, OffsetKnown: true, Precision: PrecisionSecond}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := p.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	m, err := ReadAll(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil || !m.Taken.Time.Equal(when) || m.XMP.DateCreated == nil || !m.XMP.DateCreated.Time.Equal(when) {
		t.Errorf("after the edit: %+v, XMP %+v, %v", m.Taken, m.XMP.DateCreated, err)
	}
}

// ExifTool reads every date EditXMP writes as written.
func TestEditXMPDatesAgreeWithExifTool(t *testing.T) {
	tool, err := exec.LookPath("exiftool")
	if err != nil {
		t.Skip("exiftool not installed")
	}
	dir := t.TempDir()
	for i, d := range editDates {
		d := d
		out, err := EditXMP([]byte(datesBase), XMPEdit{DateCreated: &d, CreateDate: &d, DateTimeOriginal: &d, ModifyDate: &d})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("d%d.xmp", i)), out, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out, err := exec.Command(tool, "-j", "-n", "-q", "-validate", "-warning", "-XMP:DateCreated", "-XMP:CreateDate",
		"-XMP:DateTimeOriginal", "-XMP:ModifyDate", dir).Output()
	if err != nil {
		t.Fatal(err)
	}
	var got []map[string]any
	if err := json.Unmarshal(out, &got); err != nil || len(got) != len(editDates) {
		t.Fatalf("%v (%d files)\n%s", err, len(got), out)
	}
	for _, e := range got {
		name := filepath.Base(e["SourceFile"].(string))
		var i int
		if _, err := fmt.Sscanf(name, "d%d.xmp", &i); err != nil || i >= len(editDates) {
			t.Fatalf("%s: %v", name, err)
		}
		want := editDates[i]
		if want.Precision == 0 {
			want.Precision = PrecisionSecond
		}
		if w, ok := e["Warning"]; ok {
			t.Errorf("%s: ExifTool warns %v", name, w)
		}
		for _, tag := range []string{"DateCreated", "CreateDate", "DateTimeOriginal", "ModifyDate"} {
			if g, w := exifToolDate(e[tag]), formatXMPDateAsExifTool(parsedDate(t, want)); g != w {
				t.Errorf("%s: %s %q, want %q", name, tag, g, w)
			}
		}
	}
}

// parsedDate is d as ParseXMP gives it back (its zone named as ParseXMP
// names it), for formatXMPDateAsExifTool.
func parsedDate(t *testing.T, d Date) *Date {
	t.Helper()
	at, ok := parseXMPDate(formatXMPDate(d))
	if !ok {
		t.Fatalf("%q doesn't parse", formatXMPDate(d))
	}
	return &at
}
