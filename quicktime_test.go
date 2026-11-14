package mediameta

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/0x20h/go-media-metadata/internal/bmff/qtbuild"
)

func readVideo(t *testing.T, file []byte) Video {
	t.Helper()
	v, err := ReadVideo(bytes.NewReader(file), int64(len(file)))
	if err != nil {
		t.Fatalf("ReadVideo: %v", err)
	}
	return v
}

func TestReadVideo(t *testing.T) {
	file, _, _ := qtbuild.Build(qtbuild.Default())
	v := readVideo(t, file)
	wantCreated := time.Date(2025, 11, 12, 10, 15, 30, 0, time.FixedZone("", 3600))
	if !v.Created.Equal(wantCreated) || !v.OffsetKnown {
		t.Errorf("Created %v (offset known %v), want %v", v.Created, v.OffsetKnown, wantCreated)
	}
	if _, off := v.Created.Zone(); off != 3600 {
		t.Errorf("Created in a zone of offset %d, want 3600", off)
	}
	if v.Duration != 3*time.Second {
		t.Errorf("Duration %v", v.Duration)
	}
	if v.GPS == nil || v.GPS.Latitude != 52.52 || v.GPS.Longitude != 13.405 || !v.GPS.HasAltitude || v.GPS.Altitude != 34 {
		t.Errorf("GPS %+v", v.GPS)
	}
	if v.Orientation != 6 {
		t.Errorf("Orientation %v, want 6", v.Orientation)
	}
	if v.Width != 1920 || v.Height != 1080 {
		t.Errorf("size %dx%d", v.Width, v.Height)
	}
	if v.Make != "Apple" || v.Model != "iPhone 12 Pro" {
		t.Errorf("Make %q, Model %q", v.Make, v.Model)
	}
	if v.ContentIdentifier != "6E2B5B8C-1F0A-4C3B-9D7E-2A1B3C4D5E6F" {
		t.Errorf("ContentIdentifier %q", v.ContentIdentifier)
	}
}

// movieLayouts are the structures a movie read and write must handle.
var movieLayouts = []struct {
	name string
	edit func(*qtbuild.Options)
}{
	{"iPhone-like", func(*qtbuild.Options) {}},
	{"moov first", func(o *qtbuild.Options) { o.MoovFirst = true }},
	{"64-bit headers", func(o *qtbuild.Options) { o.MvhdVersion, o.TkhdVersion = 1, 1 }},
	{"co64", func(o *qtbuild.Options) { o.Co64 = true; o.MoovFirst = true }},
	{"MP4 meta FullBox", func(o *qtbuild.Options) { o.Brand = "mp42"; o.FullBoxMeta = true }},
	{"Keys in udta", func(o *qtbuild.Options) { o.KeysInUdta = true }},
	{"no ftyp", func(o *qtbuild.Options) { o.Brand = "" }},
	{"video track first", func(o *qtbuild.Options) { o.AudioFirst = false }},
	{"audio only", func(o *qtbuild.Options) { o.NoVideo = true }},
	{"udta only", func(o *qtbuild.Options) {
		o.Brand, o.Keys = "mp42", nil
		o.Udta = []qtbuild.KV{
			{Key: "\xa9xyz", Value: "+48.8584+002.2945/"},
			{Key: "\xa9day", Value: "2024-07-14T21:00:00+02:00"},
			{Key: "\xa9mak", Value: "Example"},
			{Key: "\xa9mod", Value: "Camera 1"},
		}
	}},
	{"no metadata", func(o *qtbuild.Options) { o.Keys = nil }},
	{"empty", func(o *qtbuild.Options) { o.Keys, o.MediaSize, o.Created, o.Duration = nil, 0, 0, 0 }},
}

func TestReadVideoLayouts(t *testing.T) {
	for _, l := range movieLayouts {
		t.Run(l.name, func(t *testing.T) {
			o := qtbuild.Default()
			l.edit(&o)
			file, _, _ := qtbuild.Build(o)
			v := readVideo(t, file)
			switch l.name {
			case "audio only":
				if v.Orientation != 0 || v.Width != 0 {
					t.Errorf("audio only: orientation %v, size %dx%d", v.Orientation, v.Width, v.Height)
				}
			default:
				if v.Orientation != 6 || v.Width != 1920 {
					t.Errorf("orientation %v, size %dx%d", v.Orientation, v.Width, v.Height)
				}
			}
			switch l.name {
			case "udta only":
				want := time.Date(2024, 7, 14, 21, 0, 0, 0, time.FixedZone("", 7200))
				if !v.Created.Equal(want) || v.GPS == nil || v.GPS.Latitude != 48.8584 || v.GPS.HasAltitude || v.Make != "Example" || v.Model != "Camera 1" {
					t.Errorf("udta: %+v, GPS %+v", v, v.GPS)
				}
			case "no metadata":
				// The movie header's time, in UTC.
				want := time.Date(2025, 11, 12, 0, 0, 0, 0, time.UTC)
				if !v.Created.Equal(want) || !v.OffsetKnown || v.GPS != nil || v.Make != "" || v.ContentIdentifier != "" {
					t.Errorf("no metadata: %+v", v)
				}
			case "empty":
				if !v.Created.IsZero() || v.Duration != 0 {
					t.Errorf("empty: %+v", v)
				}
			default:
				if v.GPS == nil || v.ContentIdentifier == "" || v.Created.IsZero() {
					t.Errorf("Keys not read: %+v", v)
				}
			}
		})
	}
}

// Every rotation and mirroring a track matrix shows, and one it can't.
func TestVideoOrientation(t *testing.T) {
	const one = 1 << 16
	for _, c := range []struct {
		m    [4]int32
		want Orientation
	}{
		{[4]int32{one, 0, 0, one}, 1},
		{[4]int32{-one, 0, 0, one}, 2},
		{[4]int32{-one, 0, 0, -one}, 3},
		{[4]int32{one, 0, 0, -one}, 4},
		{[4]int32{0, one, one, 0}, 5},
		{[4]int32{0, one, -one, 0}, 6},
		{[4]int32{0, -one, -one, 0}, 7},
		{[4]int32{0, -one, one, 0}, 8},
		{[4]int32{2 * one, 0, 0, 2 * one}, 0}, // scaled
		{[4]int32{one, one / 2, 0, one}, 0},   // skewed
	} {
		o := qtbuild.Default()
		o.Matrix = c.m
		file, _, _ := qtbuild.Build(o)
		if got := readVideo(t, file).Orientation; got != c.want {
			t.Errorf("matrix %v: %v, want %v", c.m, got, c.want)
		}
	}
}

func TestParseISO6709(t *testing.T) {
	for _, c := range []struct {
		in       string
		lat, lon float64
		alt      float64
		hasAlt   bool
		ok       bool
	}{
		{"+52.5200+013.4050+034.000/", 52.52, 13.405, 34, true, true},
		{"+37.3317-122.0302/", 37.3317, -122.0302, 0, false, true},
		{"-33.8688+151.2093-005.5/", -33.8688, 151.2093, -5.5, true, true},
		{"+4852.50+00217.67/", 48.875, 2.2945, 0, false, true},          // DDMM.MM
		{"+485230.0+0021704.2/", 48.875, 2.2845, 0, false, true},        // DDMMSS.S
		{"+52.52+013.405+034CRSWGS_84/", 52.52, 13.405, 34, true, true}, // with a CRS
		{"+91.0+000.0/", 0, 0, 0, false, false},
		{"+00.0+181.0/", 0, 0, 0, false, false},
		{"+4860.0+00200.0/", 0, 0, 0, false, false}, // 60 minutes
		{"52.52+013.405/", 0, 0, 0, false, false},
		{"+52.52/", 0, 0, 0, false, false},
		{"+52.+013.4/", 0, 0, 0, false, false},
		{"", 0, 0, 0, false, false},
		{"+1+2+3+4/", 0, 0, 0, false, false},
	} {
		g, ok := parseISO6709(c.in)
		if ok != c.ok {
			t.Errorf("%q: ok %v", c.in, ok)
			continue
		}
		if !ok {
			continue
		}
		if math.Abs(g.Latitude-c.lat) > 1e-4 || math.Abs(g.Longitude-c.lon) > 1e-4 || g.HasAltitude != c.hasAlt || g.Altitude != c.alt {
			t.Errorf("%q: %+v", c.in, g)
		}
	}
}

func TestParseISO8601(t *testing.T) {
	for _, c := range []struct {
		in   string
		want string // RFC 3339, "" for no date
	}{
		{"2025-11-12T10:15:30+0100", "2025-11-12T10:15:30+01:00"},
		{"2025-11-12T10:15:30+01:00", "2025-11-12T10:15:30+01:00"},
		{"2025-11-12T10:15:30Z", "2025-11-12T10:15:30Z"},
		{"2025-11-12T10:15:30.25-0500", "2025-11-12T10:15:30.25-05:00"},
		{"2025-11-12T10:15+0100", "2025-11-12T10:15:00+01:00"},
		{"2025-11-12T10:15:30", ""}, // no zone: not a known time
		{"2025-13-12T10:15:30Z", ""},
		{"", ""},
	} {
		got, ok := parseISO8601(c.in)
		switch {
		case c.want == "" && ok:
			t.Errorf("%q: %v, want none", c.in, got)
		case c.want != "" && (!ok || got.Format(time.RFC3339Nano) != c.want):
			t.Errorf("%q: %v (%v), want %s", c.in, got.Format(time.RFC3339Nano), ok, c.want)
		}
	}
}

// Other formats aren't movies.
func TestReadVideoNotAMovie(t *testing.T) {
	for name, file := range map[string][]byte{
		"heif":  readFile(t, "testdata/imageio/a-orientation1.heic"),
		"jpeg":  plainJPEG(t),
		"short": {0, 0, 0},
		"text":  []byte("this is not a movie file at all"),
	} {
		if _, err := ReadVideo(bytes.NewReader(file), int64(len(file))); !errors.Is(err, errNotQuickTime) {
			t.Errorf("%s: %v, want errNotQuickTime", name, err)
		}
	}
}

// ReadVideo reads a few KB, nothing inside mdat, wherever moov is.
func TestReadVideoReadsLittle(t *testing.T) {
	for _, moovFirst := range []bool{false, true} {
		o := qtbuild.Default()
		o.MoovFirst, o.MediaSize = moovFirst, 1<<20
		file, start, end := qtbuild.Build(o)
		cr := &countingReader{r: bytes.NewReader(file)}
		if _, err := ReadVideo(cr, int64(len(file))); err != nil {
			t.Fatal(err)
		}
		if cr.bytes > 8<<10 {
			t.Errorf("moov first %v: read %d bytes", moovFirst, cr.bytes)
		}
		for _, rg := range cr.ranges {
			if rg[0] < end && rg[1] > start {
				t.Errorf("moov first %v: read [%d, %d) inside mdat [%d, %d)", moovFirst, rg[0], rg[1], start, end)
			}
		}
		t.Logf("moov first %v: %d bytes in %d reads of a %d-byte file", moovFirst, cr.bytes, cr.calls, len(file))
	}
}

func movieSeeds(f *testing.F) {
	for _, l := range movieLayouts {
		o := qtbuild.Default()
		l.edit(&o)
		o.MediaSize = 64
		file, _, _ := qtbuild.Build(o)
		f.Add(file)
	}
}

func FuzzReadVideo(f *testing.F) {
	movieSeeds(f)
	f.Fuzz(func(t *testing.T, data []byte) {
		withinDeadline(t, func() {
			v, err := ReadVideo(bytes.NewReader(data), int64(len(data)))
			if err != nil {
				return
			}
			if v.Orientation != 0 && !v.Orientation.Valid() {
				t.Fatalf("orientation %d", v.Orientation)
			}
			if g := v.GPS; g != nil && (math.Abs(g.Latitude) > 90 || math.Abs(g.Longitude) > 180) {
				t.Fatalf("GPS %+v", g)
			}
		})
	})
}

func BenchmarkReadVideo(b *testing.B) {
	for _, moovFirst := range []bool{false, true} {
		o := qtbuild.Default()
		o.MoovFirst, o.MediaSize = moovFirst, 1<<20
		file, _, _ := qtbuild.Build(o)
		b.Run(fmt.Sprintf("moov-first-%v", moovFirst), func(b *testing.B) {
			cr := &countingReader{r: bytes.NewReader(file)}
			b.ReportAllocs()
			for range b.N {
				if _, err := ReadVideo(cr, int64(len(file))); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(cr.bytes)/float64(b.N), "read-B/op")
		})
	}
}

// ReadVideo agrees with ExifTool on the synthetic movies. Runs where
// exiftool is installed; skipped elsewhere (CI).
func TestReadVideoExifTool(t *testing.T) {
	if _, err := exec.LookPath("exiftool"); err != nil {
		t.Skip("exiftool not installed")
	}
	dir := t.TempDir()
	var paths []string
	for i, l := range movieLayouts {
		o := qtbuild.Default()
		l.edit(&o)
		file, _, _ := qtbuild.Build(o)
		p := filepath.Join(dir, fmt.Sprintf("%02d.mov", i))
		if err := os.WriteFile(p, file, 0o600); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	out, err := exec.Command("exiftool", append([]string{"-json", "-n", "-api", "LargeFileSupport=1",
		"-Rotation", "-GPSCoordinates", "-ContentIdentifier", "-Make", "-Model", "-ImageWidth", "-ImageHeight"}, paths...)...).Output()
	if err != nil {
		t.Fatal(err)
	}
	var got []struct {
		SourceFile        string
		Rotation          *int
		GPSCoordinates    string
		ContentIdentifier string
		Make, Model       string
		ImageWidth        int
		ImageHeight       int
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	for _, g := range got {
		file, err := os.ReadFile(g.SourceFile)
		if err != nil {
			t.Fatal(err)
		}
		v := readVideo(t, file)
		name := movieLayouts[mustAtoi(t, strings.TrimSuffix(filepath.Base(g.SourceFile), ".mov"))].name
		// ExifTool reads Keys only in moov's QuickTime-style meta box;
		// this package also takes them from an MP4 FullBox meta or one in
		// udta.
		otherKeys := name == "MP4 meta FullBox" || name == "Keys in udta"
		if g.Rotation != nil && v.Orientation.Valid() && v.Orientation.Degrees() != *g.Rotation {
			t.Errorf("%s: rotation %d°, ExifTool %d°", name, v.Orientation.Degrees(), *g.Rotation)
		}
		if !otherKeys && g.ContentIdentifier != v.ContentIdentifier {
			t.Errorf("%s: content identifier %q, ExifTool %q", name, v.ContentIdentifier, g.ContentIdentifier)
		}
		if !otherKeys && (g.Make != v.Make || g.Model != v.Model) {
			t.Errorf("%s: %q %q, ExifTool %q %q", name, v.Make, v.Model, g.Make, g.Model)
		}
		if g.ImageWidth != v.Width || g.ImageHeight != v.Height {
			t.Errorf("%s: %dx%d, ExifTool %dx%d", name, v.Width, v.Height, g.ImageWidth, g.ImageHeight)
		}
		if g.GPSCoordinates != "" {
			var lat, lon float64
			if _, err := fmt.Sscanf(g.GPSCoordinates, "%g %g", &lat, &lon); err != nil || v.GPS == nil ||
				math.Abs(lat-v.GPS.Latitude) > 1e-6 || math.Abs(lon-v.GPS.Longitude) > 1e-6 {
				t.Errorf("%s: GPS %+v, ExifTool %q", name, v.GPS, g.GPSCoordinates)
			}
		}
	}
}

func mustAtoi(t *testing.T, s string) int {
	t.Helper()
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		t.Fatal(err)
	}
	return n
}
