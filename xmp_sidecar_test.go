package mediameta

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// The sidecars in testdata/sidecar were written by ExifTool, as a raw
// converter writes them: full.xmp has a title, keywords, a rating, GPS,
// dates, tiff:Orientation and a face region; partial.xmp a rating and a
// keyword; empty.xmp nothing.
func readSidecar(t *testing.T, name string) XMP {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "sidecar", name))
	if err != nil {
		t.Fatal(err)
	}
	x, err := ParseXMP(b)
	if err != nil {
		t.Fatalf("ParseXMP(%s): %v", name, err)
	}
	return x
}

func TestParseSidecar(t *testing.T) {
	x := readSidecar(t, "full.xmp")
	if x.Title != "Harbour at dusk" || !slices.Equal(x.Keywords, []string{"harbour", "boats"}) {
		t.Errorf("title %q, keywords %q", x.Title, x.Keywords)
	}
	if !x.HasRating || x.Rating != 4 {
		t.Errorf("rating %d (%v), want 4", x.Rating, x.HasRating)
	}
	if x.Orientation != 6 {
		t.Errorf("orientation %d, want 6 (tiff:Orientation)", x.Orientation)
	}
	if x.GPS == nil || math.Abs(x.GPS.Latitude-54.32) > 1e-9 || math.Abs(x.GPS.Longitude-10.13) > 1e-9 {
		t.Errorf("GPS %+v, want 54.32, 10.13", x.GPS)
	}
	if x.Regions == nil || len(x.Regions.List) != 1 || x.Regions.List[0].Name != "Anna" {
		t.Errorf("regions %+v", x.Regions)
	}
	want := time.Date(2024, 6, 1, 20, 15, 0, 0, time.FixedZone("", 2*3600))
	for name, d := range map[string]*Date{"DateCreated": x.DateCreated, "DateTimeOriginal": x.DateTimeOriginal} {
		if d == nil || !d.Time.Equal(want) || !d.OffsetKnown {
			t.Errorf("%s %+v, want %v", name, d, want)
		}
	}
	if e := readSidecar(t, "empty.xmp"); !xmpSame(e, XMP{}) {
		t.Errorf("empty sidecar reads %+v", e)
	}
}

func TestXMPOrientation(t *testing.T) {
	for v, want := range map[string]Orientation{"1": 1, "8": 8, " 3 ": 3, "0": 0, "9": 0, "6.0": 0, "x": 0, "": 0} {
		packet := `<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
			`<rdf:Description xmlns:tiff="http://ns.adobe.com/tiff/1.0/"><tiff:Orientation>` + v + `</tiff:Orientation></rdf:Description></rdf:RDF></x:xmpmeta>`
		x, err := ParseXMP([]byte(packet))
		if err != nil {
			t.Fatalf("%q: %v", v, err)
		}
		if x.Orientation != want {
			t.Errorf("tiff:Orientation %q reads %d, want %d", v, x.Orientation, want)
		}
	}
}

// The file's own XMP, as a camera or an organiser wrote it before the
// sidecar.
func fileXMP() XMP {
	d := &Date{Time: time.Date(2024, 6, 1, 20, 14, 0, 0, time.UTC), Precision: PrecisionSecond}
	return XMP{
		Title: "IMG_1", Description: "From the camera", Keywords: []string{"camera"},
		Rating: 1, HasRating: true, PersonInImage: []string{"Ben"},
		GPS: &GPS{Latitude: 1, Longitude: 2}, DateCreated: d, CreateDate: d, DateTimeOriginal: d,
		Orientation: 1,
	}
}

func TestMergeXMP(t *testing.T) {
	file, full := fileXMP(), readSidecar(t, "full.xmp")
	m := MergeXMP(file, full)
	// What the sidecar has wins.
	if m.Title != full.Title || !slices.Equal(m.Keywords, full.Keywords) || m.Rating != 4 || m.Orientation != 6 ||
		*m.GPS != *full.GPS || m.Regions == nil || !m.DateCreated.Time.Equal(full.DateCreated.Time) ||
		!m.DateTimeOriginal.Time.Equal(full.DateTimeOriginal.Time) {
		t.Errorf("merged with full.xmp: %+v", m)
	}
	// What it leaves out stays the file's.
	if m.Description != file.Description || !slices.Equal(m.PersonInImage, file.PersonInImage) ||
		!m.CreateDate.Time.Equal(file.CreateDate.Time) {
		t.Errorf("merged with full.xmp lost the file's description, people or CreateDate: %+v", m)
	}

	// A sidecar with a rating and a keyword changes those two.
	p := MergeXMP(file, readSidecar(t, "partial.xmp"))
	want := fileXMP()
	want.Rating, want.Keywords = 2, []string{"edited"}
	if !xmpSame(p, want) {
		t.Errorf("merged with partial.xmp:\n got %+v\nwant %+v", p, want)
	}

	// An empty sidecar changes nothing, and neither does none at all.
	if e := MergeXMP(file, readSidecar(t, "empty.xmp")); !xmpSame(e, file) {
		t.Errorf("merged with empty.xmp: %+v", e)
	}
	if e := MergeXMP(file, XMP{}); !xmpSame(e, file) {
		t.Errorf("merged with no sidecar: %+v", e)
	}

	// A rating of 0 is a rating; an orientation of 0 isn't one.
	z := MergeXMP(file, XMP{HasRating: true})
	if !z.HasRating || z.Rating != 0 || z.Orientation != 1 {
		t.Errorf("rating 0 sidecar: rating %d (%v), orientation %d", z.Rating, z.HasRating, z.Orientation)
	}
}

// The result shares nothing with its inputs: changing it leaves them as
// they were.
func TestMergeXMPCopies(t *testing.T) {
	file, side := fileXMP(), readSidecar(t, "full.xmp")
	m := MergeXMP(file, side)
	m.Keywords[0] = "changed"
	m.PersonInImage[0] = "changed"
	m.Regions.List[0].Name = "changed"
	m.GPS.Latitude = 0
	m.DateCreated.Time = time.Time{}
	m.CreateDate.Time = time.Time{}
	if side.Keywords[0] != "harbour" || file.PersonInImage[0] != "Ben" || side.Regions.List[0].Name != "Anna" ||
		side.GPS.Latitude == 0 || side.DateCreated.Time.IsZero() || file.CreateDate.Time.IsZero() {
		t.Error("changing MergeXMP's result changed its inputs")
	}
}

// A malformed sidecar is ErrMalformed, and leaving it out keeps the file's
// metadata.
func TestMalformedSidecar(t *testing.T) {
	for _, b := range []string{`<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF`, `<a:b>`, "\xff\xfe<x/>"} {
		if _, err := ParseXMP([]byte(b)); !errors.Is(err, ErrMalformed) {
			t.Errorf("ParseXMP(%q) = %v, want ErrMalformed", b, err)
		}
	}
}

// ExifTool reads the sidecars' values as ParseXMP does. Runs where
// exiftool is installed.
func TestSidecarsAgreeWithExifTool(t *testing.T) {
	tool, err := exec.LookPath("exiftool")
	if err != nil {
		t.Skip("exiftool not installed")
	}
	out, err := exec.Command(tool, "-j", "-n", "-q", "-struct", "-XMP:Title", "-XMP:Subject", "-XMP:Rating",
		"-XMP:Orientation", "-XMP:GPSLatitude", "-XMP:GPSLongitude", "-XMP:RegionInfo", filepath.Join("testdata", "sidecar")).Output()
	if err != nil {
		t.Fatalf("exiftool: %v", err)
	}
	var got []struct {
		SourceFile                string
		Title                     string
		Subject                   any
		Rating, Orientation       float64
		GPSLatitude, GPSLongitude float64
		RegionInfo                *struct{ RegionList []struct{ Name string } }
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("exiftool's JSON: %v\n%s", err, out)
	}
	if len(got) != 3 {
		t.Fatalf("exiftool read %d sidecars, want 3", len(got))
	}
	for _, e := range got {
		x := readSidecar(t, filepath.Base(e.SourceFile))
		var subject []string
		switch v := e.Subject.(type) {
		case string:
			subject = []string{v}
		case []any:
			for _, s := range v {
				subject = append(subject, s.(string))
			}
		}
		if x.Title != e.Title || !slices.Equal(x.Keywords, subject) || x.Rating != int(e.Rating) ||
			int(x.Orientation) != int(e.Orientation) {
			t.Errorf("%s: ParseXMP %q %q %d %d, ExifTool %q %q %v %v", e.SourceFile,
				x.Title, x.Keywords, x.Rating, x.Orientation, e.Title, subject, e.Rating, e.Orientation)
		}
		if (x.GPS != nil) != (e.GPSLatitude != 0) || x.GPS != nil &&
			(math.Abs(x.GPS.Latitude-e.GPSLatitude) > 1e-9 || math.Abs(x.GPS.Longitude-e.GPSLongitude) > 1e-9) {
			t.Errorf("%s: GPS %+v, ExifTool %v, %v", e.SourceFile, x.GPS, e.GPSLatitude, e.GPSLongitude)
		}
		if (x.Regions != nil) != (e.RegionInfo != nil) || x.Regions != nil && x.Regions.List[0].Name != e.RegionInfo.RegionList[0].Name {
			t.Errorf("%s: regions %+v, ExifTool %+v", e.SourceFile, x.Regions, e.RegionInfo)
		}
	}
}

// Each merged value comes from the sidecar when it has one, else from the
// file, and an empty sidecar changes nothing. The values are made from the
// fuzzer's input directly: parsing them is FuzzEditXMP's.
func FuzzMergeXMP(f *testing.F) {
	f.Add("IMG_1", "", "camera", "harbour,boats", int8(1), int8(4), true, uint8(1), uint8(6), true, false)
	f.Add("", "T", "", "", int8(0), int8(0), false, uint8(0), uint8(9), false, true)
	f.Fuzz(func(t *testing.T, fileTitle, sideTitle, fileKw, sideKw string, fileRating, sideRating int8, sideHasRating bool,
		fileO, sideO uint8, fileGPS, sideGPS bool) {
		split := func(s string) []string {
			if s == "" {
				return nil
			}
			return strings.Split(s, ",")
		}
		gps := func(on bool, lat float64) *GPS {
			if !on {
				return nil
			}
			return &GPS{Latitude: lat}
		}
		file := XMP{Title: fileTitle, Keywords: split(fileKw), Rating: int(fileRating), HasRating: true,
			Orientation: Orientation(fileO), GPS: gps(fileGPS, 1)}
		side := XMP{Title: sideTitle, Keywords: split(sideKw), Rating: int(sideRating), HasRating: sideHasRating,
			Orientation: Orientation(sideO), GPS: gps(sideGPS, 2)}
		m := MergeXMP(file, side)
		pick := func(sidecarHas bool) XMP {
			if sidecarHas {
				return side
			}
			return file
		}
		if m.Title != pick(side.Title != "").Title ||
			!slices.Equal(m.Keywords, pick(len(side.Keywords) > 0).Keywords) ||
			m.Rating != pick(side.HasRating).Rating || !m.HasRating ||
			m.Orientation != pick(side.Orientation.Valid()).Orientation ||
			!xmpSame(XMP{GPS: m.GPS}, XMP{GPS: pick(side.GPS != nil).GPS}) {
			t.Fatalf("MergeXMP took a value from the wrong side:\nfile %+v\nside %+v\ngot  %+v", file, side, m)
		}
		if !xmpSame(MergeXMP(file, XMP{}), file) {
			t.Fatal("an empty sidecar changed the result")
		}
	})
}

// xmpSame compares two XMP values by content, pointers by what they
// point to.
func xmpSame(a, b XMP) bool {
	eqDate := func(x, y *Date) bool {
		return x == nil && y == nil || x != nil && y != nil && x.Time.Equal(y.Time) && x.OffsetKnown == y.OffsetKnown && x.Precision == y.Precision
	}
	eqRegions := func(x, y *Regions) bool {
		return x == nil && y == nil || x != nil && y != nil && x.AppliedTo == y.AppliedTo && slices.Equal(x.List, y.List)
	}
	eqGPS := func(x, y *GPS) bool { return x == nil && y == nil || x != nil && y != nil && *x == *y }
	return a.Title == b.Title && a.Description == b.Description && slices.Equal(a.Keywords, b.Keywords) &&
		slices.Equal(a.HierarchicalKeywords, b.HierarchicalKeywords) && a.Rating == b.Rating && a.HasRating == b.HasRating &&
		slices.Equal(a.PersonInImage, b.PersonInImage) && eqRegions(a.Regions, b.Regions) && eqGPS(a.GPS, b.GPS) &&
		eqDate(a.DateCreated, b.DateCreated) && eqDate(a.CreateDate, b.CreateDate) && eqDate(a.DateTimeOriginal, b.DateTimeOriginal) &&
		a.Orientation == b.Orientation
}
