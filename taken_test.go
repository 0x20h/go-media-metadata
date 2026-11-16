package mediameta

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseIIMDate(t *testing.T) {
	ist := time.FixedZone("+05:30", 5*3600+1800)
	for _, c := range []struct {
		date, clock string
		want        time.Time
		off         bool
		prec        DatePrecision
		ok          bool
	}{
		{"20220102", "102030+0530", time.Date(2022, 1, 2, 10, 20, 30, 0, ist), true, PrecisionSecond, true},
		{"20220102", "102030", time.Date(2022, 1, 2, 10, 20, 30, 0, time.UTC), false, PrecisionSecond, true},
		{"20220102", "", time.Date(2022, 1, 2, 0, 0, 0, 0, time.UTC), false, PrecisionDay, true},
		{"20220102", "1020", time.Date(2022, 1, 2, 0, 0, 0, 0, time.UTC), false, PrecisionDay, true},
		{"20220102", "102030+9900", time.Date(2022, 1, 2, 10, 20, 30, 0, time.UTC), false, PrecisionSecond, true},
		{"20220102", "256030", time.Date(2022, 1, 2, 0, 0, 0, 0, time.UTC), false, PrecisionDay, true},
		{"20220100", "102030", time.Date(2022, 1, 1, 0, 0, 0, 0, time.UTC), false, PrecisionMonth, true},
		{"20220000", "", time.Date(2022, 1, 1, 0, 0, 0, 0, time.UTC), false, PrecisionYear, true},
		{"20220230", "", time.Time{}, false, 0, false},
		{"2022010", "", time.Time{}, false, 0, false},
		{"2022-1-2", "", time.Time{}, false, 0, false},
		{"00000102", "", time.Time{}, false, 0, false},
	} {
		got, ok := parseIIMDate(c.date, c.clock)
		want := Date{Time: c.want, OffsetKnown: c.off, Precision: c.prec}
		if ok != c.ok || ok && !sameXMPDate(&got, &want) {
			t.Errorf("%q %q: %+v %v, want %+v %v", c.date, c.clock, got, ok, want, c.ok)
		}
	}
}

func TestTakenFromOrder(t *testing.T) {
	at := func(h int) time.Time { return time.Date(2020, 1, 1, h, 0, 0, 0, time.UTC) }
	d := func(h int) *Date { return &Date{Time: at(h), Precision: PrecisionSecond} }
	e := Exif{DateTimeOriginal: at(1), DateTimeDigitized: at(2), DigitizedOffsetKnown: true}
	x := XMP{DateTimeOriginal: d(3), DateCreated: d(4), CreateDate: d(5)}
	i := IPTC{DateCreated: d(6)}
	want := []DateSource{SourceExifDateTimeOriginal, SourceExifDateTimeDigitized, SourceXMPDateTimeOriginal,
		SourceXMPDateCreated, SourceXMPCreateDate, SourceIPTCDateCreated, SourceNone}
	for n, src := range want {
		got := TakenFrom(e, x, i)
		if got.Source != src || src != SourceNone && got.Time.Hour() != n+1 {
			t.Errorf("step %d: %+v, want %v", n, got, src)
		}
		switch n {
		case 0:
			e.DateTimeOriginal = time.Time{}
		case 1:
			if !got.OffsetKnown {
				t.Errorf("DateTimeDigitized's offset lost: %+v", got)
			}
			e.DateTimeDigitized = time.Time{}
		case 2:
			x.DateTimeOriginal = nil
		case 3:
			x.DateCreated = nil
		case 4:
			x.CreateDate = nil
		case 5:
			i.DateCreated = nil
		}
	}
}

// ReadTaken gives the date ExifTool's values give in TakenFrom's order,
// on every corpus file and, with MEDIAMETA_RAW_DIR set, on real RAW files.
func TestReadTakenAgreesWithExifTool(t *testing.T) {
	tool, err := exec.LookPath("exiftool")
	if err != nil {
		t.Skip("exiftool not installed")
	}
	var paths []string
	pats := []string{"testdata/exiftool/*.jpg", imageIOCorpus + "/*.jpg", imageIOCorpus + "/*.heic", "testdata/iphone/*.heic", "testdata/webp/*.webp"}
	if dir := os.Getenv("MEDIAMETA_RAW_DIR"); dir != "" {
		for _, ext := range []string{"dng", "cr2", "cr3", "nef", "arw", "orf", "rw2", "raf"} {
			pats = append(pats, filepath.Join(dir, "*."+ext))
		}
	}
	for _, pat := range pats {
		p, _ := filepath.Glob(pat)
		paths = append(paths, p...)
	}
	args := append([]string{"-j", "-n", "-q", "-G1", "-Composite:SubSecDateTimeOriginal", "-Composite:SubSecCreateDate", "-EXIF:DateTimeOriginal", "-EXIF:CreateDate",
		"-Composite:SubSecModifyDate", "-EXIF:ModifyDate",
		"-XMP-exif:DateTimeOriginal", "-XMP-photoshop:DateCreated", "-XMP-xmp:CreateDate", "-IPTC:DateCreated", "-IPTC:TimeCreated"}, paths...)
	out, err := exec.Command(tool, args...).Output()
	if err != nil {
		t.Fatalf("exiftool: %v", err)
	}
	var got []map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	seen := map[DateSource]int{}
	for _, e := range got {
		path := e["SourceFile"].(string)
		want, src := exifToolTaken(t, e)
		file := readFile(t, path)
		taken, err := ReadTaken(bytes.NewReader(file), int64(len(file)))
		if err != nil {
			t.Errorf("%s: %v", path, err)
			continue
		}
		seen[taken.Source]++
		if taken.Source != src || src != SourceNone && !sameXMPDate(&taken.Date, &want) {
			t.Errorf("%s: %+v from %v, ExifTool's %+v from %v", path, taken.Date, taken.Source, want, src)
		}
	}
	for _, src := range []DateSource{SourceExifDateTimeOriginal, SourceExifDateTimeDigitized, SourceXMPDateTimeOriginal,
		SourceXMPDateCreated, SourceIPTCDateCreated, SourceExifDateTime, SourceNone} {
		if seen[src] == 0 {
			t.Errorf("no corpus file dated by %v", src)
		}
	}
}

// exifToolTaken applies TakenFrom's order to ExifTool's -G1 -n values,
// reading each with parseXMPDate once it's in XMP's layout.
func exifToolTaken(t *testing.T, e map[string]any) (Date, DateSource) {
	t.Helper()
	asXMP := func(s string) string {
		// "YYYY:MM:DD hh:mm:ss…" → "YYYY-MM-DDThh:mm:ss…"
		if len(s) >= 10 && s[4] == ':' && s[7] == ':' {
			s = s[:4] + "-" + s[5:7] + "-" + s[8:]
		}
		if len(s) > 10 && s[10] == ' ' {
			s = s[:10] + "T" + s[11:]
		}
		return s
	}
	str := func(k string) string { return strings.TrimSpace(exifToolDate(e[k])) }
	// The SubSec composites are there only with a fraction or an offset;
	// else the Exif tag, in whichever IFD group ExifTool names.
	exifTag := func(tag string) string {
		for k, v := range e {
			if g, name, _ := strings.Cut(k, ":"); name == tag && !strings.HasPrefix(g, "XMP") && g != "Composite" {
				return strings.TrimSpace(exifToolDate(v))
			}
		}
		return ""
	}
	for _, c := range [][2]string{{"Composite:SubSecDateTimeOriginal", "DateTimeOriginal"}, {"Composite:SubSecCreateDate", "CreateDate"},
		{"Composite:SubSecModifyDate", "ModifyDate"}} {
		if str(c[0]) == "" {
			e[c[0]] = exifTag(c[1])
		}
	}
	for _, c := range []struct {
		key string
		src DateSource
	}{
		{"Composite:SubSecDateTimeOriginal", SourceExifDateTimeOriginal},
		{"Composite:SubSecCreateDate", SourceExifDateTimeDigitized},
		{"XMP-exif:DateTimeOriginal", SourceXMPDateTimeOriginal},
		{"XMP-photoshop:DateCreated", SourceXMPDateCreated},
		{"XMP-xmp:CreateDate", SourceXMPCreateDate},
	} {
		if s := str(c.key); s != "" {
			d, ok := parseXMPDate(asXMP(s))
			if !ok {
				t.Fatalf("%s: ExifTool's %s %q", e["SourceFile"], c.key, s)
			}
			return d, c.src
		}
	}
	if s := str("IPTC:DateCreated"); s != "" {
		if clock := str("IPTC:TimeCreated"); clock != "" {
			s += " " + clock
		}
		d, ok := parseXMPDate(asXMP(s))
		if !ok {
			t.Fatalf("%s: ExifTool's IPTC date %q", e["SourceFile"], s)
		}
		return d, SourceIPTCDateCreated
	}
	if s := str("Composite:SubSecModifyDate"); s != "" {
		d, ok := parseXMPDate(asXMP(s))
		if !ok {
			t.Fatalf("%s: ExifTool's ModifyDate %q", e["SourceFile"], s)
		}
		return d, SourceExifDateTime
	}
	return Date{}, SourceNone
}
