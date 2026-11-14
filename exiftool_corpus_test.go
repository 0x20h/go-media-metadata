package mediameta

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// The ExifTool corpus (testdata/exiftool): JPEGs whose metadata ExifTool
// wrote, and the values it wrote (expected.json). Reading runs everywhere;
// the write-back check against ExifTool's validation runs where exiftool
// is installed.

type corpusWant struct {
	Orientation Orientation `json:"orientation"`
	Taken       string      `json:"taken"`
	OffsetKnown bool        `json:"offsetKnown"`
	GPS         *struct {
		Lat, Lon float64
		Alt      *float64
	} `json:"gps"`
	XMP *struct {
		Title, Description string
		Keywords           []string
		Rating             *int
		People             []string
		Regions            *struct {
			W, H float64
			Unit string
			List []struct {
				Name, Type string
				X, Y, W, H float64
			}
		}
	} `json:"xmp"`
	IPTC *struct {
		Title, Description string
		Keywords           []string
	} `json:"iptc"`
}

func corpus(t *testing.T) map[string][]byte {
	t.Helper()
	files := map[string][]byte{}
	paths, _ := filepath.Glob("testdata/exiftool/*.jpg")
	for _, p := range paths {
		files[filepath.Base(p)] = readFile(t, p)
	}
	if len(files) == 0 {
		t.Fatal("no corpus files")
	}
	return files
}

func corpusExpected(t *testing.T) map[string]corpusWant {
	t.Helper()
	var want map[string]corpusWant
	if err := json.Unmarshal(readFile(t, "testdata/exiftool/expected.json"), &want); err != nil {
		t.Fatal(err)
	}
	return want
}

// checkCorpusFile compares what this package reads from file with want.
func checkCorpusFile(t *testing.T, name string, file []byte, want corpusWant) {
	t.Helper()
	if o := orientationOfFile(t, file); o != want.Orientation {
		t.Errorf("%s: orientation %d, want %d", name, o, want.Orientation)
	}
	x := readExif(t, file)
	if want.Taken != "" {
		taken, err := time.Parse(time.RFC3339Nano, want.Taken)
		if err != nil {
			t.Fatal(err)
		}
		if !x.DateTimeOriginal.Equal(taken) || x.OffsetKnown != want.OffsetKnown {
			t.Errorf("%s: taken %v (offset %v), want %v", name, x.DateTimeOriginal, x.OffsetKnown, taken)
		}
	}
	if (x.GPS == nil) != (want.GPS == nil) {
		t.Errorf("%s: GPS %+v, want %+v", name, x.GPS, want.GPS)
	} else if g := want.GPS; g != nil {
		if math.Abs(x.GPS.Latitude-g.Lat) > 1e-6 || math.Abs(x.GPS.Longitude-g.Lon) > 1e-6 ||
			x.GPS.HasAltitude != (g.Alt != nil) || g.Alt != nil && math.Abs(x.GPS.Altitude-*g.Alt) > 1e-6 {
			t.Errorf("%s: GPS %+v, want %+v", name, *x.GPS, *g)
		}
	}
	packet, err := ReadXMP(bytes.NewReader(file), int64(len(file)))
	if err != nil {
		t.Fatal(err)
	}
	if w := want.XMP; w != nil {
		p, err := ParseXMP(packet)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if p.Title != w.Title || p.Description != w.Description || !slices.Equal(p.Keywords, w.Keywords) ||
			p.HasRating != (w.Rating != nil) || w.Rating != nil && p.Rating != *w.Rating || !slices.Equal(p.PersonInImage, w.People) {
			t.Errorf("%s: XMP %+v, want %+v", name, p, *w)
		}
		if r := w.Regions; r != nil {
			want := Regions{AppliedTo: Dimensions{W: r.W, H: r.H, Unit: r.Unit}}
			for _, g := range r.List {
				want.List = append(want.List, Region{Name: g.Name, Type: g.Type, X: g.X, Y: g.Y, W: g.W, H: g.H})
			}
			if p.Regions == nil || !sameRegions(p.Regions, &want) {
				t.Errorf("%s: regions %+v, want %+v", name, p.Regions, want)
			}
		}
	} else if packet != nil && want.IPTC == nil {
		t.Errorf("%s: XMP where none was written", name)
	}
	// IPTC-IIM is read and written in JPEG only.
	if !isJPEG(bytes.NewReader(file), int64(len(file))) {
		if want.IPTC != nil {
			t.Errorf("%s: IPTC expected in a file that isn't a JPEG", name)
		}
		return
	}
	iim, err := ReadIPTC(bytes.NewReader(file), int64(len(file)))
	if err != nil {
		t.Fatal(err)
	}
	if w := want.IPTC; w != nil {
		p, err := ParseIPTC(iim)
		if err != nil || p.Title != w.Title || p.Description != w.Description || !slices.Equal(p.Keywords, w.Keywords) {
			t.Errorf("%s: IPTC %+v (%v), want %+v", name, p, err, *w)
		}
	}
}

func TestExifToolCorpusReads(t *testing.T) {
	want := corpusExpected(t)
	files := corpus(t)
	for name, w := range want {
		file, ok := files[name]
		if !ok {
			t.Errorf("%s: missing", name)
			continue
		}
		checkCorpusFile(t, name, file, w)
	}
}

// corpusEdits are this package's writers applied to a corpus file: each
// changes one thing, and checkCorpusFile with the expectation changed to
// match must still hold for everything else.
var corpusEdits = map[string]func(t *testing.T, file []byte, w *corpusWant) []byte{
	"orientation": func(t *testing.T, file []byte, w *corpusWant) []byte {
		w.Orientation = 5
		return setOrientationJPEG(t, file, 5)
	},
	"date": func(t *testing.T, file []byte, w *corpusWant) []byte {
		at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.FixedZone("+03:00", 3*3600))
		w.Taken, w.OffsetKnown = at.Format(time.RFC3339Nano), true
		return planOut(t, file, func() (*Plan, error) {
			return planExifEdit(bytes.NewReader(file), int64(len(file)), ExifEdit{DateTimeOriginal: &at, OffsetKnown: true})
		})
	},
	"xmp title": func(t *testing.T, file []byte, w *corpusWant) []byte {
		if w.XMP == nil {
			w.XMP = &struct {
				Title, Description string
				Keywords           []string
				Rating             *int
				People             []string
				Regions            *struct {
					W, H float64
					Unit string
					List []struct {
						Name, Type string
						X, Y, W, H float64
					}
				}
			}{}
		}
		w.XMP.Title = "Renamed"
		return planOut(t, file, func() (*Plan, error) {
			return planXMPEdit(bytes.NewReader(file), int64(len(file)), XMPEdit{Title: ptr("Renamed")})
		})
	},
	"iptc keywords": func(t *testing.T, file []byte, w *corpusWant) []byte {
		iim, err := ReadIPTC(bytes.NewReader(file), int64(len(file)))
		if err != nil {
			t.Fatal(err)
		}
		edited, err := EditIPTC(iim, IPTCEdit{Keywords: &[]string{"neu", "Straße"}})
		if err != nil {
			t.Fatal(err)
		}
		if w.IPTC == nil {
			w.IPTC = &struct {
				Title, Description string
				Keywords           []string
			}{}
		}
		w.IPTC.Keywords = []string{"neu", "Straße"}
		return planOut(t, file, func() (*Plan, error) { return PlanIPTC(bytes.NewReader(file), int64(len(file)), edited) })
	},
}

func planOut(t *testing.T, file []byte, plan func() (*Plan, error)) []byte {
	t.Helper()
	p, err := plan()
	if err != nil {
		t.Fatal(err)
	}
	checkPlan(t, file, p)
	var out bytes.Buffer
	if _, err := p.WriteTo(&out); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// Each writer, on each corpus file: the change reads back, everything else
// reads as ExifTool wrote it, and the image data is untouched. Where
// exiftool is installed, it validates every result with no warning its
// source didn't have, and finds the IPTC digest current.
func TestExifToolCorpusWriteBack(t *testing.T) {
	files := corpus(t)
	dir := t.TempDir()
	written := map[string]string{} // written file → its source
	for name, file := range files {
		for edit, apply := range corpusEdits {
			// A fresh copy each time: the edits change it.
			w := corpusExpected(t)[name]
			out := apply(t, file, &w)
			sos := func(b []byte) []byte { return b[bytes.Index(b, []byte{0xFF, markerSOS}):] }
			if !bytes.Equal(sos(file), sos(out)) {
				t.Errorf("%s after %s: the image data changed", name, edit)
			}
			checkCorpusFile(t, name+" after "+edit, out, w)
			fn := strings.TrimSuffix(name, ".jpg") + "--" + strings.ReplaceAll(edit, " ", "-") + ".jpg"
			if err := os.WriteFile(filepath.Join(dir, fn), out, 0o644); err != nil {
				t.Fatal(err)
			}
			written[fn] = name
		}
		if err := os.WriteFile(filepath.Join(dir, name), file, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	tool, err := exec.LookPath("exiftool")
	if err != nil {
		return
	}
	j, err := exec.Command(tool, "-j", "-q", "-validate", "-warning", "-a", "-IPTCDigest", "-CurrentIPTCDigest", dir).Output()
	if err != nil {
		t.Fatal(err)
	}
	var got []map[string]any
	if err := json.Unmarshal(j, &got); err != nil {
		t.Fatal(err)
	}
	warnings := map[string]string{}
	for _, e := range got {
		w, _ := json.Marshal(e["Warning"])
		warnings[filepath.Base(e["SourceFile"].(string))] = string(w)
	}
	for _, e := range got {
		fn := filepath.Base(e["SourceFile"].(string))
		src, ok := written[fn]
		if !ok {
			continue
		}
		// Exif this package creates lacks the image size, as Exif ExifTool
		// creates does (exif-ii.jpg): it isn't known from the TIFF data.
		if warnings[fn] != warnings[src] && warnings[fn] != warnings["exif-ii.jpg"] {
			t.Errorf("%s: ExifTool warns %s, its source %s", fn, warnings[fn], warnings[src])
		}
		if d, ok := e["IPTCDigest"]; ok && d != e["CurrentIPTCDigest"] {
			t.Errorf("%s: IPTC digest %v isn't current (%v)", fn, d, e["CurrentIPTCDigest"])
		}
	}
}
