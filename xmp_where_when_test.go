package mediameta

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func whereWhenXMP(lat, lon, alt, ref, created, create string) string {
	return `<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
		`<rdf:Description rdf:about="" xmlns:exif="http://ns.adobe.com/exif/1.0/" xmlns:photoshop="http://ns.adobe.com/photoshop/1.0/" xmlns:xmp="http://ns.adobe.com/xap/1.0/"` +
		fmt.Sprintf(` exif:GPSLatitude=%q exif:GPSLongitude=%q exif:GPSAltitude=%q exif:GPSAltitudeRef=%q photoshop:DateCreated=%q xmp:CreateDate=%q/>`,
			lat, lon, alt, ref, created, create) +
		`</rdf:RDF></x:xmpmeta>`
}

func TestParseXMPCoord(t *testing.T) {
	for _, c := range []struct {
		in       string
		pos, neg byte
		limit    float64
		want     float64
		ok       bool
	}{
		{"51,30N", 'N', 'S', 90, 51.5, true},
		{"51,30.5S", 'N', 'S', 90, -(51 + 30.5/60), true},
		{"10,7,30E", 'E', 'W', 180, 10.125, true},
		{"0,7.5W", 'E', 'W', 180, -0.125, true},
		{"51.5N", 'N', 'S', 90, 51.5, true},
		{"0.125w", 'E', 'W', 180, -0.125, true},
		{"-0.125", 'E', 'W', 180, -0.125, true},
		{"51.5", 'N', 'S', 90, 51.5, true},
		{" 51 30 N ", 'N', 'S', 90, 51.5, true},
		{"180,0E", 'E', 'W', 180, 180, true},
		{"91,0N", 'N', 'S', 90, 0, false},
		{"200", 'E', 'W', 180, 0, false},
		{"0,60W", 'E', 'W', 180, 0, false},
		{"51.5,30N", 'N', 'S', 90, 0, false},
		{"0.125N", 'E', 'W', 180, 0, false},
		{"51.5X", 'N', 'S', 90, 0, false},
		{"-51.5S", 'N', 'S', 90, 0, false},
		{"1,2,3,4N", 'N', 'S', 90, 0, false},
		{"NaN", 'N', 'S', 90, 0, false},
		{"", 'N', 'S', 90, 0, false},
		{"N", 'N', 'S', 90, 0, false},
		{"1e1N", 'N', 'S', 90, 0, false},
	} {
		got, ok := parseXMPCoord(c.in, c.pos, c.neg, c.limit)
		if ok != c.ok || ok && math.Abs(got-c.want) > 1e-12 {
			t.Errorf("%q: %v %v, want %v %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestParseXMPDate(t *testing.T) {
	plus2 := time.FixedZone("+02:00", 2*3600)
	for _, c := range []struct {
		in   string
		want time.Time
		off  bool
		prec DatePrecision
		ok   bool
	}{
		{"2025", time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), false, PrecisionYear, true},
		{"2025-05", time.Date(2025, 5, 1, 0, 0, 0, 0, time.UTC), false, PrecisionMonth, true},
		{"2025-05-04", time.Date(2025, 5, 4, 0, 0, 0, 0, time.UTC), false, PrecisionDay, true},
		{"2025-05-04T13:14Z", time.Date(2025, 5, 4, 13, 14, 0, 0, time.FixedZone("Z", 0)), true, PrecisionMinute, true},
		{"2025-05-04T13:14:15+02:00", time.Date(2025, 5, 4, 13, 14, 15, 0, plus2), true, PrecisionSecond, true},
		{"2025-05-04T13:14:15.25", time.Date(2025, 5, 4, 13, 14, 15, 250e6, time.UTC), false, PrecisionSecond, true},
		{"2025-05-04T13:14:15.123456789-05:30", time.Date(2025, 5, 4, 13, 14, 15, 123456789, time.FixedZone("-05:30", -(5*3600+1800))), true, PrecisionSecond, true},
		{"2025-05-04T13:14:15.2500000001Z", time.Date(2025, 5, 4, 13, 14, 15, 250e6, time.FixedZone("Z", 0)), true, PrecisionSecond, true},
		{"2025:05:04 13:14:15", time.Date(2025, 5, 4, 13, 14, 15, 0, time.UTC), false, PrecisionSecond, true},
		{"  2025-05-04 ", time.Date(2025, 5, 4, 0, 0, 0, 0, time.UTC), false, PrecisionDay, true},
		{"2025-13-01", time.Time{}, false, 0, false},
		{"2025-02-30", time.Time{}, false, 0, false},
		{"2025-05-04T24:00:00", time.Time{}, false, 0, false},
		{"1999-12-31T23:59:60Z", time.Time{}, false, 0, false},
		{"2025-05-04T13:14:15+2:00", time.Time{}, false, 0, false},
		{"2025-05-04T13", time.Time{}, false, 0, false},
		{"2025-05-04Z", time.Time{}, false, 0, false},
		{"2025-05-04T13:14:15.", time.Time{}, false, 0, false},
		{"2025-05:04", time.Time{}, false, 0, false},
		{"0000", time.Time{}, false, 0, false},
		{"", time.Time{}, false, 0, false},
		{"25", time.Time{}, false, 0, false},
	} {
		got, ok := parseXMPDate(c.in)
		if ok != c.ok || ok && (!got.Time.Equal(c.want) || got.OffsetKnown != c.off || got.Precision != c.prec ||
			got.Time.Location().String() != c.want.Location().String()) {
			t.Errorf("%q: %+v %v, want %v (offset %v, precision %d) %v", c.in, got, ok, c.want, c.off, c.prec, c.ok)
		}
	}
}

func TestParseXMPWhereWhen(t *testing.T) {
	x := parseXMP(t, []byte(whereWhenXMP("51,30N", "0,7.5W", "71/2", "1", "2025-05-04T13:14:15.25", "2025")))
	if g := x.GPS; g == nil || g.Latitude != 51.5 || g.Longitude != -0.125 || !g.HasAltitude || g.Altitude != -35.5 {
		t.Errorf("GPS %+v", x.GPS)
	}
	if d := x.DateCreated; d == nil || d.Precision != PrecisionSecond || d.OffsetKnown || d.Time.Nanosecond() != 250e6 {
		t.Errorf("DateCreated %+v", x.DateCreated)
	}
	if d := x.CreateDate; d == nil || d.Precision != PrecisionYear {
		t.Errorf("CreateDate %+v", x.CreateDate)
	}
	// Elements read as attributes do; no longitude is no position; a bad
	// altitude leaves the position.
	x = parseXMP(t, []byte(`<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#"><rdf:Description xmlns:exif="http://ns.adobe.com/exif/1.0/">`+
		`<exif:GPSLatitude>10.5S</exif:GPSLatitude><exif:GPSLongitude>20E</exif:GPSLongitude><exif:GPSAltitude>3/0</exif:GPSAltitude></rdf:Description></rdf:RDF>`))
	if g := x.GPS; g == nil || g.Latitude != -10.5 || g.Longitude != 20 || g.HasAltitude {
		t.Errorf("GPS %+v", x.GPS)
	}
	if x := parseXMP(t, []byte(whereWhenXMP("51,30N", "", "", "", "", ""))); x.GPS != nil || x.DateCreated != nil || x.CreateDate != nil {
		t.Errorf("got %+v", x)
	}
}

// ParseXMP reads the position and dates as ExifTool does, where exiftool
// is installed: the same position within 1e-6°, the same dates. What
// ExifTool reads leniently (a latitude past 90°, 30 February) this package
// reads as none.
func TestXMPWhereWhenAgreesWithExifTool(t *testing.T) {
	tool, err := exec.LookPath("exiftool")
	if err != nil {
		t.Skip("exiftool not installed")
	}
	dir := t.TempDir()
	packets := [][6]string{
		{"51,30N", "0,7.5W", "71/2", "0", "2025", "2025-05"},
		{"51,30.5S", "10,7,30E", "35.5", "1", "2025-05-04", "2025-05-04T13:14Z"},
		{"51.5N", "0.125W", "3", "0", "2025-05-04T13:14:15+02:00", "2025-05-04T13:14:15.123456789-05:30"},
		{"51.5", "-0.125", "12", "1", "2025:05:04 13:14:15", "2025-05-04T13:14:15.25"},
		{"33,51.9S", "151,12.564E", "", "", "2019-12-31T23:59:59-05:00", "2019-12-31T23:59"},
	}
	files := map[string][]byte{}
	for i, p := range packets {
		name := fmt.Sprintf("p%d.xmp", i)
		files[name] = []byte(whereWhenXMP(p[0], p[1], p[2], p[3], p[4], p[5]))
		if err := os.WriteFile(filepath.Join(dir, name), files[name], 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out, err := exec.Command(tool, "-j", "-n", "-q", "-XMP:GPSLatitude", "-XMP:GPSLongitude", "-XMP:GPSAltitude", "-XMP:GPSAltitudeRef",
		"-XMP:DateCreated", "-XMP:CreateDate", dir).Output()
	if err != nil {
		t.Fatalf("exiftool: %v", err)
	}
	var got []map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("exiftool's JSON: %v\n%s", err, out)
	}
	if len(got) != len(files) {
		t.Fatalf("exiftool read %d files, want %d", len(got), len(files))
	}
	for _, e := range got {
		name := filepath.Base(e["SourceFile"].(string))
		checkWhereWhen(t, name, parseXMP(t, files[name]), e)
	}
}

// checkWhereWhen compares the position and dates ParseXMP read with
// ExifTool's -j -n values.
func checkWhereWhen(t *testing.T, name string, x XMP, e map[string]any) {
	t.Helper()
	lat, hasLat := e["GPSLatitude"].(float64)
	lon, hasLon := e["GPSLongitude"].(float64)
	if (x.GPS != nil) != (hasLat && hasLon) {
		t.Errorf("%s: GPS %+v, ExifTool %v, %v", name, x.GPS, e["GPSLatitude"], e["GPSLongitude"])
	} else if x.GPS != nil {
		if math.Abs(x.GPS.Latitude-lat) > 1e-6 || math.Abs(x.GPS.Longitude-lon) > 1e-6 {
			t.Errorf("%s: GPS %+v, ExifTool %v, %v", name, *x.GPS, lat, lon)
		}
		alt, hasAlt := e["GPSAltitude"].(float64)
		if ref, _ := e["GPSAltitudeRef"].(float64); ref == 1 {
			alt = -alt
		}
		if x.GPS.HasAltitude != hasAlt || hasAlt && math.Abs(x.GPS.Altitude-alt) > 1e-6 {
			t.Errorf("%s: altitude %v (%v), ExifTool %v", name, x.GPS.Altitude, x.GPS.HasAltitude, e["GPSAltitude"])
		}
	}
	for _, c := range []struct {
		tag string
		d   *XMPDate
	}{{"DateCreated", x.DateCreated}, {"CreateDate", x.CreateDate}} {
		want := exifToolDate(e[c.tag])
		if got := formatXMPDateAsExifTool(c.d); got != want {
			t.Errorf("%s: %s %q, ExifTool %q", name, c.tag, got, want)
		}
	}
}

// exifToolDate is an ExifTool -j date value as a string (it gives a year
// alone as a number).
func exifToolDate(v any) string {
	switch v := v.(type) {
	case string:
		return v
	case float64:
		return fmt.Sprint(v)
	}
	return ""
}

// formatXMPDateAsExifTool writes d as ExifTool prints a date: "YYYY:MM:DD
// hh:mm:ss.s+hh:mm", as much as was written.
func formatXMPDateAsExifTool(d *XMPDate) string {
	if d == nil {
		return ""
	}
	layout := map[DatePrecision]string{PrecisionYear: "2006", PrecisionMonth: "2006:01", PrecisionDay: "2006:01:02",
		PrecisionMinute: "2006:01:02 15:04", PrecisionSecond: "2006:01:02 15:04:05.999999999"}[d.Precision]
	s := d.Time.Format(layout)
	if d.OffsetKnown {
		s += d.Time.Location().String()
	}
	return s
}

// The ExifTool and ImageIO corpora: ParseXMP reads the position and dates
// in each file's XMP as ExifTool does.
func TestCorporaXMPWhereWhen(t *testing.T) {
	tool, err := exec.LookPath("exiftool")
	if err != nil {
		t.Skip("exiftool not installed")
	}
	var paths []string
	for _, pat := range []string{"testdata/exiftool/*.jpg", imageIOCorpus + "/*.jpg", imageIOCorpus + "/*.heic"} {
		p, _ := filepath.Glob(pat)
		paths = append(paths, p...)
	}
	args := append([]string{"-j", "-n", "-q", "-XMP:GPSLatitude", "-XMP:GPSLongitude", "-XMP:GPSAltitude", "-XMP:GPSAltitudeRef",
		"-XMP:DateCreated", "-XMP:CreateDate"}, paths...)
	out, err := exec.Command(tool, args...).Output()
	if err != nil {
		t.Fatalf("exiftool: %v", err)
	}
	var got []map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, e := range got {
		path := e["SourceFile"].(string)
		file := readFile(t, path)
		packet, err := ReadXMP(strings.NewReader(string(file)), int64(len(file)))
		if err != nil {
			t.Fatal(err)
		}
		var x XMP
		if packet != nil {
			x = parseXMP(t, packet)
		}
		if x.GPS != nil || x.DateCreated != nil || x.CreateDate != nil {
			seen++
		}
		checkWhereWhen(t, path, x, e)
	}
	if seen < 4 {
		t.Errorf("only %d corpus files with an XMP position or date", seen)
	}
}

// checkWhereWhenValid checks what ParseXMP may return for the position
// and dates: a position on the globe, dates with a precision, written back
// as they read.
func checkWhereWhenValid(t *testing.T, x XMP) {
	t.Helper()
	if g := x.GPS; g != nil && (math.IsNaN(g.Latitude) || math.Abs(g.Latitude) > 90 || math.IsNaN(g.Longitude) || math.Abs(g.Longitude) > 180 ||
		g.HasAltitude && (math.IsNaN(g.Altitude) || math.IsInf(g.Altitude, 0)) || !g.HasAltitude && g.Altitude != 0) {
		t.Fatalf("GPS %+v", *g)
	}
	for _, d := range []*XMPDate{x.DateCreated, x.CreateDate} {
		if d == nil {
			continue
		}
		if d.Precision < PrecisionYear || d.Precision > PrecisionSecond || d.Time.Year() < 1 || d.OffsetKnown && d.Precision < PrecisionMinute ||
			!d.OffsetKnown && d.Time.Location() != time.UTC {
			t.Fatalf("date %+v", *d)
		}
		// The date in the form XMP writes reads back the same.
		layout := map[DatePrecision]string{PrecisionYear: "2006", PrecisionMonth: "2006-01", PrecisionDay: "2006-01-02",
			PrecisionMinute: "2006-01-02T15:04", PrecisionSecond: "2006-01-02T15:04:05.999999999"}[d.Precision]
		s := d.Time.Format(layout)
		if d.OffsetKnown {
			s += d.Time.Location().String()
		}
		if back, ok := parseXMPDate(s); !ok || !sameXMPDate(&back, d) {
			t.Fatalf("date %+v wrote as %q, read back as %+v (%v)", *d, s, back, ok)
		}
	}
}

func sameGPS(a, b *GPS) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func sameXMPDate(a, b *XMPDate) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Time.Equal(b.Time) && a.OffsetKnown == b.OffsetKnown && a.Precision == b.Precision &&
		a.Time.Location().String() == b.Time.Location().String()
}
