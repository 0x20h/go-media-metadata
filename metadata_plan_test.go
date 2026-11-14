package mediameta

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// combinedEdits are combined edits: every part, and one part alone.
func combinedEdits(jpeg bool) map[string]MetadataEdit {
	d := time.Date(2031, 7, 8, 9, 10, 11, 0, time.FixedZone("", 2*3600))
	exif := &ExifEdit{DateTimeOriginal: &d, DateTimeDigitized: &d, OffsetKnown: true, GPS: &GPS{Latitude: 48.8584, Longitude: 2.2945}}
	xmp := &XMPEdit{Title: ptr("Café 東京"), Description: ptr("Boats & nets"), Keywords: &[]string{"東京", "boats"}, Rating: ptr(5)}
	out := map[string]MetadataEdit{
		"exif and xmp": {Exif: exif, XMP: xmp},
		"xmp alone":    {XMP: xmp},
		"date in place": {Exif: &ExifEdit{DateTimeOriginal: &d, OffsetKnown: true},
			XMP: &XMPEdit{Rating: ptr(4)}},
		"nothing": {Exif: &ExifEdit{}, XMP: &XMPEdit{}},
	}
	if jpeg {
		iptc := &IPTCEdit{Title: ptr("Café 東京"), Description: ptr("Boats & nets"), Keywords: &[]string{"東京", "boats"}}
		out["all three"] = MetadataEdit{Exif: exif, XMP: xmp, IPTC: iptc}
		out["iptc alone"] = MetadataEdit{IPTC: iptc}
	}
	return out
}

// sequential writes the parts of e one after another, each with its own
// plan on the file the one before wrote: what PlanMetadata must write.
func sequential(t *testing.T, file []byte, e MetadataEdit) []byte {
	t.Helper()
	step := func(p *Plan, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		file = written(t, p)
	}
	if e.Exif != nil && *e.Exif != (ExifEdit{}) {
		step(planExifEdit(bytes.NewReader(file), int64(len(file)), *e.Exif))
	}
	if e.IPTC != nil && *e.IPTC != (IPTCEdit{}) {
		step(planIPTCEdit(bytes.NewReader(file), int64(len(file)), *e.IPTC))
	}
	if e.XMP != nil && *e.XMP != (XMPEdit{}) {
		step(planXMPEdit(bytes.NewReader(file), int64(len(file)), *e.XMP))
	}
	return file
}

// Every combined edit of every file: the plan writes what the parts
// written one after another write, byte for byte; it keeps the plan's
// invariants (so Apply on a copy gives the same, when in place); every
// standard reads back with its part; and the image data is as it was.
func TestPlanMetadata(t *testing.T) {
	for name, file := range xmpEditFiles(t) {
		isJ := isJPEG(bytes.NewReader(file), int64(len(file)))
		for ename, e := range combinedEdits(isJ) {
			t.Run(name+"/"+ename, func(t *testing.T) {
				p, err := PlanMetadata(bytes.NewReader(file), int64(len(file)), e)
				if err != nil {
					t.Fatal(err)
				}
				checkPlan(t, file, p)
				out := written(t, p)
				if want := sequential(t, file, e); !bytes.Equal(out, want) {
					t.Fatalf("%d bytes, the parts one after another %d", len(out), len(want))
				}
				if ename == "nothing" {
					if !p.InPlace || len(p.Patches) != 0 || !bytes.Equal(out, file) {
						t.Error("an empty edit changes the file")
					}
					return
				}
				sameImage(t, file, out)
				if e.XMP != nil && e.XMP.Title != nil {
					if x := readXMPFields(t, out); x.Title != *e.XMP.Title || !slices.Equal(x.Keywords, *e.XMP.Keywords) {
						t.Errorf("XMP reads back as %+v", x)
					}
				}
				if e.Exif != nil && e.Exif.GPS != nil {
					x, err := ReadExif(bytes.NewReader(out), int64(len(out)))
					if err != nil || x.GPS == nil || x.DateTimeOriginal.IsZero() {
						t.Errorf("Exif reads back as %+v, %v", x, err)
					}
				}
				if e.IPTC != nil {
					iim, err := ReadIPTC(bytes.NewReader(out), int64(len(out)))
					if err != nil {
						t.Fatal(err)
					}
					if x, err := ParseIPTC(iim); err != nil || x.Title != *e.IPTC.Title || !slices.Equal(x.Keywords, *e.IPTC.Keywords) {
						t.Errorf("IPTC reads back as %+v, %v", x, err)
					}
				}
				// Again: nothing changes, in place.
				again, err := PlanMetadata(bytes.NewReader(out), int64(len(out)), e)
				if err != nil {
					t.Fatal(err)
				}
				if !again.InPlace || !bytes.Equal(written(t, again), out) {
					t.Error("the same edit again changes the file")
				}
			})
		}
	}
}

// The plan is in place exactly when every part is, and then holds every
// changed byte: a date and a rating that fit their places (an XMP packet
// with padding) change a JPEG in place.
func TestPlanMetadataInPlace(t *testing.T) {
	file := written(t, planMetadata(t, jpegFiles(t)["photo little"], nil, xmpPacket("Old title", 3000)))
	d := time.Date(2031, 7, 8, 9, 10, 11, 0, time.FixedZone("", 2*3600))
	e := MetadataEdit{Exif: &ExifEdit{DateTimeOriginal: &d, OffsetKnown: true}, XMP: &XMPEdit{Rating: ptr(4)}}
	p, err := PlanMetadata(bytes.NewReader(file), int64(len(file)), e)
	if err != nil {
		t.Fatal(err)
	}
	if !p.InPlace || len(p.Patches) == 0 {
		t.Fatalf("in place %v with %d patches", p.InPlace, len(p.Patches))
	}
	checkPlan(t, file, p)
}

// IPTC in a file that isn't a JPEG is refused, and nothing is planned.
func TestPlanMetadataIPTCOnlyInJPEG(t *testing.T) {
	file := testPNG(t)
	_, err := PlanMetadata(bytes.NewReader(file), int64(len(file)), MetadataEdit{
		XMP: &XMPEdit{Title: ptr("x")}, IPTC: &IPTCEdit{Title: ptr("x")}})
	if !errors.Is(err, ErrUnsupported) {
		t.Errorf("IPTC in a PNG: %v", err)
	}
}

// Every combined edit that succeeds writes what its parts written one
// after another write, keeps the plan's invariants and reads back.
func FuzzPlanMetadata(f *testing.F) {
	seeds(f)
	jpegSeeds(f)
	for _, file := range chunkedFiles(f) {
		f.Add(file)
	}
	d := time.Date(2031, 7, 8, 9, 10, 11, 0, time.UTC)
	edits := []MetadataEdit{
		{Exif: &ExifEdit{DateTimeOriginal: &d, GPS: &GPS{Latitude: 1, Longitude: 2}}, XMP: &XMPEdit{Title: ptr("t"), Rating: ptr(5)},
			IPTC: &IPTCEdit{Title: ptr("t"), Keywords: &[]string{"k"}}},
		{Exif: &ExifEdit{RemoveGPS: true}, XMP: &XMPEdit{Keywords: &[]string{}}},
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		withinDeadline(t, func() {
			for _, e := range edits {
				p, err := PlanMetadata(bytes.NewReader(data), int64(len(data)), e)
				if err != nil {
					continue
				}
				checkPlan(t, data, p)
				out := written(t, p)
				if want := sequential(t, data, e); !bytes.Equal(out, want) {
					t.Fatalf("%d bytes, the parts one after another %d", len(out), len(want))
				}
			}
		})
	})
}

// ExifTool reads every standard of a combined edit back from a JPEG and a
// HEIF file, and its -validate finds nothing the source didn't have.
func TestPlanMetadataWithExifTool(t *testing.T) {
	tool, err := exec.LookPath("exiftool")
	if err != nil {
		t.Skip("exiftool not installed")
	}
	dir := t.TempDir()
	files := xmpEditFiles(t)
	for _, name := range []string{"jpeg jfif+exif", "heif iPhone-like with xmp"} {
		src := files[name]
		isJ := isJPEG(bytes.NewReader(src), int64(len(src)))
		e := combinedEdits(isJ)["exif and xmp"]
		if isJ {
			e = combinedEdits(true)["all three"]
		}
		p, err := PlanMetadata(bytes.NewReader(src), int64(len(src)), e)
		if err != nil {
			t.Fatal(err)
		}
		before, after := filepath.Join(dir, "before.bin"), filepath.Join(dir, "after.bin")
		if err := os.WriteFile(before, src, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(after, written(t, p), 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := exec.Command(tool, "-j", "-q", "-n", "-validate", "-warning", "-error",
			"-XMP-dc:Title", "-XMP-dc:Subject", "-XMP-xmp:Rating", "-IPTC:ObjectName", "-IPTC:Keywords",
			"-EXIF:DateTimeOriginal", "-Composite:GPSLatitude", before, after).Output()
		if err != nil {
			t.Fatalf("exiftool: %v", err)
		}
		var r []map[string]any
		if err := json.Unmarshal(got, &r); err != nil || len(r) != 2 {
			t.Fatalf("%v: %s", err, got)
		}
		a := r[1]
		if a["Title"] != "Café 東京" || fmt.Sprint(a["Subject"]) != "[東京 boats]" || fmt.Sprint(a["Rating"]) != "5" ||
			a["DateTimeOriginal"] != "2031:07:08 09:10:11" || fmt.Sprint(a["GPSLatitude"]) != "48.8584" {
			t.Errorf("%s: ExifTool reads %v", name, a)
		}
		if isJ && (a["ObjectName"] != "Café 東京" || fmt.Sprint(a["Keywords"]) != "[東京 boats]") {
			t.Errorf("%s: ExifTool reads IPTC %v %v", name, a["ObjectName"], a["Keywords"])
		}
		var e0, w0, m0, e1, w1, m1 int
		_, _ = fmt.Sscan(fmt.Sprint(r[0]["Validate"]), &e0, &w0, &m0)
		_, _ = fmt.Sscan(fmt.Sprint(a["Validate"]), &e1, &w1, &m1)
		if e1 > e0 || w1 > w0 || m1 > m0 {
			t.Errorf("%s: ExifTool validates it as %v (%v), the source as %v", name, a["Validate"], a["Warning"], r[0]["Validate"])
		}
	}
}
