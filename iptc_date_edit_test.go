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

var iptcDates = []Date{
	{Time: time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC), Precision: PrecisionYear},
	{Time: time.Date(2021, 7, 1, 0, 0, 0, 0, time.UTC), Precision: PrecisionMonth},
	{Time: time.Date(2021, 7, 8, 0, 0, 0, 0, time.UTC), Precision: PrecisionDay},
	{Time: time.Date(2021, 7, 8, 9, 10, 0, 0, time.UTC), Precision: PrecisionMinute},
	{Time: time.Date(2021, 7, 8, 9, 10, 11, 0, time.UTC), Precision: PrecisionSecond},
	{Time: time.Date(2021, 7, 8, 9, 10, 11, 0, time.FixedZone("", 2*3600)), OffsetKnown: true, Precision: PrecisionSecond},
	{Time: time.Date(1999, 12, 31, 23, 59, 59, 0, time.FixedZone("", -(5*3600+30*60))), OffsetKnown: true},
}

// iptcWant is d as ParseIPTC reads it back: a minute's seconds are
// written, so it reads to the second.
func iptcWant(d Date) Date {
	if d.Precision == 0 || d.Precision == PrecisionMinute {
		d.Precision = PrecisionSecond
	}
	return d
}

func TestEditIPTCDate(t *testing.T) {
	base, err := EditIPTC(nil, IPTCEdit{Title: ptr("Lake"), DateCreated: &Date{Time: time.Date(2001, 2, 3, 4, 5, 6, 0, time.UTC), Precision: PrecisionSecond}})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range iptcDates {
		d := d
		out, err := EditIPTC(base, IPTCEdit{DateCreated: &d})
		if err != nil {
			t.Fatalf("%+v: %v", d, err)
		}
		x, err := ParseIPTC(out)
		want := iptcWant(d)
		if err != nil || !sameDate(x.DateCreated, &want) || x.Title != "Lake" {
			t.Errorf("wrote %+v, read %+v (%v), title %q", d, x.DateCreated, err, x.Title)
		}
		// Below a minute's precision no time is left.
		if d.Precision != 0 && d.Precision <= PrecisionDay && bytes.Contains(out, []byte("040506")) {
			t.Errorf("%+v: the old time stayed", d)
		}
		again, err := EditIPTC(out, IPTCEdit{DateCreated: &d})
		if err != nil || !bytes.Equal(again, out) {
			t.Errorf("%+v: the same date again changed the data", d)
		}
	}
	out, err := EditIPTC(base, IPTCEdit{DateCreated: &Date{}})
	if x, _ := ParseIPTC(out); err != nil || x.DateCreated != nil || x.Title != "Lake" {
		t.Errorf("removal: %+v, %v", x, err)
	}
	for _, bad := range []Date{
		{Time: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)},
		{Time: time.Date(2021, 1, 1, 0, 0, 0, 0, time.FixedZone("", 30)), OffsetKnown: true},
	} {
		if _, err := EditIPTC(base, IPTCEdit{DateCreated: &bad}); err == nil {
			t.Errorf("%+v written", bad)
		}
	}
}

// ExifTool reads the date and time written into a JPEG's IPTC.
func TestEditIPTCDateAgreesWithExifTool(t *testing.T) {
	tool, err := exec.LookPath("exiftool")
	if err != nil {
		t.Skip("exiftool not installed")
	}
	src := readFile(t, "testdata/exiftool/iptc-utf8.jpg")
	dir := t.TempDir()
	for i, d := range iptcDates {
		d := d
		p, err := PlanMetadata(bytes.NewReader(src), int64(len(src)), MetadataEdit{IPTC: &IPTCEdit{DateCreated: &d}})
		if err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		if _, err := p.WriteTo(&buf); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("d%d.jpg", i)), buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out, err := exec.Command(tool, "-j", "-q", "-IPTC:DateCreated", "-IPTC:TimeCreated", dir).Output()
	if err != nil {
		t.Fatal(err)
	}
	var got []map[string]any
	if err := json.Unmarshal(out, &got); err != nil || len(got) != len(iptcDates) {
		t.Fatalf("%v (%d files)\n%s", err, len(got), out)
	}
	for _, e := range got {
		name := filepath.Base(e["SourceFile"].(string))
		var i int
		if _, err := fmt.Sscanf(name, "d%d.jpg", &i); err != nil || i >= len(iptcDates) {
			t.Fatalf("%s: %v", name, err)
		}
		d := iptcDates[i]
		wantDate := d.Time.Format("2006:01:02")
		switch d.Precision {
		case PrecisionYear:
			wantDate = d.Time.Format("2006") + ":00:00"
		case PrecisionMonth:
			wantDate = d.Time.Format("2006:01") + ":00"
		}
		wantTime := ""
		if d.Precision == 0 || d.Precision >= PrecisionMinute {
			wantTime = d.Time.Format("15:04:05")
			if d.OffsetKnown {
				wantTime += d.Time.Format("-07:00")
			}
		}
		gotTime, _ := e["TimeCreated"].(string)
		if fmt.Sprint(e["DateCreated"]) != wantDate || gotTime != wantTime {
			t.Errorf("%s: ExifTool %v %v, want %s %s", name, e["DateCreated"], e["TimeCreated"], wantDate, wantTime)
		}
	}
}
