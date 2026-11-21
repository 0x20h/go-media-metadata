package mediameta

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"math"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The video corpus (testdata/video, see its make.sh): every field ReadVideo
// gives is ExifTool's, in ReadVideo's order of sources.
func TestVideoCorpusAgreesWithExifTool(t *testing.T) {
	tool, err := exec.LookPath("exiftool")
	if err != nil {
		t.Skip("exiftool not installed")
	}
	var paths []string
	for _, pat := range []string{"*.mov", "*.mp4", "*.3gp"} {
		p, _ := filepath.Glob(filepath.Join("testdata/video", pat))
		paths = append(paths, p...)
	}
	if len(paths) < 7 {
		t.Fatalf("%d corpus files", len(paths))
	}
	out, err := exec.Command(tool, append([]string{"-j", "-n", "-G1", "-api", "QuickTimeUTC", "-Keys:all", "-UserData:all", "-ItemList:all",
		"-XMP-exif:all", "-XMP-photoshop:DateCreated", "-XMP-xmp:CreateDate", "-QuickTime:CreateDate", "-QuickTime:Duration",
		"-Track1:ImageWidth", "-Track1:ImageHeight", "-Composite:Rotation", "-Composite:GPSLatitude", "-Composite:GPSLongitude",
		"-Composite:GPSAltitude", "-IFD0:Make", "-IFD0:Model", "-ExifIFD:DateTimeOriginal", "-ExifIFD:LensModel", "-VideoKeys:LensModel", "-GPS:GPSLatitude"}, paths...)...).Output()
	if err != nil {
		t.Fatal(err)
	}
	var got []map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	for _, e := range got {
		path := e["SourceFile"].(string)
		str := func(k string) string {
			switch v := e[k].(type) {
			case string:
				return v
			case float64:
				return strconv.FormatFloat(v, 'f', -1, 64)
			}
			return ""
		}
		num := func(k string) (float64, bool) { f, ok := e[k].(float64); return f, ok }
		file := readFile(t, path)
		v, err := ReadVideo(bytes.NewReader(file), int64(len(file)))
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}

		// The date: Keys, ©day, a Canon thumbnail's Exif (ExifTool's
		// first sub-document), XMP, the movie header.
		var wantDate string
		for _, k := range []string{"Keys:CreationDate", "UserData:ContentCreateDate", "ExifIFD:DateTimeOriginal", "XMP-exif:DateTimeOriginal",
			"XMP-photoshop:DateCreated", "XMP-xmp:CreateDate", "QuickTime:CreateDate"} {
			if s := str(k); s != "" && !strings.HasPrefix(s, "0000") {
				wantDate = s
				break
			}
		}
		if wantDate == "" {
			if !v.Created.IsZero() {
				t.Errorf("%s: created %v, ExifTool none", path, v.Created)
			}
		} else {
			layout := "2006:01:02 15:04:05"
			if len(wantDate) > len(layout) {
				layout += "-07:00"
			}
			at, err := time.Parse(layout, wantDate)
			if err != nil {
				t.Fatalf("%s: ExifTool's date %q: %v", path, wantDate, err)
			}
			if !v.Created.Equal(at) || v.OffsetKnown != (len(wantDate) > 19) {
				t.Errorf("%s: created %v (offset %v), ExifTool %q", path, v.Created, v.OffsetKnown, wantDate)
			}
		}

		// The movie header's own date: ExifTool's QuickTime:CreateDate
		// (with QuickTimeUTC, in the local zone; the same instant).
		if s := str("QuickTime:CreateDate"); s == "" || strings.HasPrefix(s, "0000") {
			if !v.MovieCreated.IsZero() {
				t.Errorf("%s: movie created %v, ExifTool none", path, v.MovieCreated)
			}
		} else if at, err := time.Parse("2006:01:02 15:04:05-07:00", s); err != nil || !v.MovieCreated.Equal(at) {
			t.Errorf("%s: movie created %v, ExifTool %q (%v)", path, v.MovieCreated, s, err)
		}

		// The place: Keys, ©xyz, loci (ExifTool's composite of it), XMP.
		var lat, lon, alt float64
		hasGPS, hasAlt, tol := false, false, 1e-6
		for _, k := range []string{"Keys:GPSCoordinates", "ItemList:GPSCoordinates", "UserData:GPSCoordinates"} {
			if f := strings.Fields(str(k)); !hasGPS && len(f) >= 2 {
				lat, _ = strconv.ParseFloat(f[0], 64)
				lon, _ = strconv.ParseFloat(f[1], 64)
				if len(f) > 2 {
					alt, _ = strconv.ParseFloat(f[2], 64)
					hasAlt = true
				}
				hasGPS = true
			}
		}
		if !hasGPS && str("UserData:LocationInformation") != "" {
			lat, _ = num("Composite:GPSLatitude")
			lon, _ = num("Composite:GPSLongitude")
			alt, hasAlt = num("Composite:GPSAltitude")
			hasGPS, tol = true, 1e-4 // 16.16 fixed point, which ExifTool prints to 5 decimals
		}
		if _, ok := num("GPS:GPSLatitude"); !hasGPS && ok {
			// A camera's Exif block (camera_video_exif.go).
			lat, _ = num("Composite:GPSLatitude")
			lon, _ = num("Composite:GPSLongitude")
			hasGPS, tol = true, 1e-4 // degrees, minutes and seconds to 1/100
		}
		if !hasGPS {
			if la, ok := num("XMP-exif:GPSLatitude"); ok {
				lat, hasGPS = la, true
				lon, _ = num("XMP-exif:GPSLongitude")
			}
		}
		if (v.GPS != nil) != hasGPS {
			t.Errorf("%s: GPS %+v, ExifTool has one: %v", path, v.GPS, hasGPS)
		} else if g := v.GPS; g != nil && (math.Abs(g.Latitude-lat) > tol || math.Abs(g.Longitude-lon) > tol ||
			g.HasAltitude != hasAlt || hasAlt && math.Abs(g.Altitude-alt) > 1e-3) {
			t.Errorf("%s: GPS %+v, ExifTool %v %v %v (%v)", path, *g, lat, lon, alt, hasAlt)
		}

		// The title and description: Keys, the item list, user data
		// (ExifTool's order for its own -Title and -Description).
		for _, c := range []struct{ tag, got string }{{"Title", v.Title}, {"Description", v.Description}} {
			want := ""
			for _, g := range []string{"Keys:", "ItemList:", "UserData:"} {
				if want = str(g + c.tag); want != "" {
					break
				}
			}
			if want != "" && c.got != want {
				t.Errorf("%s: %s %q, ExifTool %q", path, c.tag, c.got, want)
			}
		}

		dur, _ := num("QuickTime:Duration")
		w, _ := num("Track1:ImageWidth")
		h, _ := num("Track1:ImageHeight")
		rot, _ := num("Composite:Rotation")
		wantO := map[float64]Orientation{0: 1, 90: 6, 180: 3, 270: 8}[rot]
		if math.Abs(v.Duration.Seconds()-dur) > 1e-3 || v.Width != int(w) || v.Height != int(h) || v.Orientation != wantO {
			t.Errorf("%s: %v, %d×%d, orientation %d; ExifTool %v s, %v×%v, rotation %v", path, v.Duration, v.Width, v.Height, v.Orientation, dur, w, h, rot)
		}
		sw, sh := v.Shown()
		if rot == 90 || rot == 270 {
			w, h = h, w
		}
		if sw != int(w) || sh != int(h) {
			t.Errorf("%s: shown %d×%d, want %v×%v", path, sw, sh, w, h)
		}
		if mk := first(first(str("Keys:Make"), str("UserData:Make")), str("IFD0:Make")); v.Make != mk {
			t.Errorf("%s: make %q, ExifTool %q", path, v.Make, mk)
		}
		if md := first(first(str("Keys:Model"), str("UserData:Model")), str("IFD0:Model")); v.Model != md {
			t.Errorf("%s: model %q, ExifTool %q", path, v.Model, md)
		}
		if lens := first(first(str("Keys:LensModel"), str("VideoKeys:LensModel")), str("ExifIFD:LensModel")); v.LensModel != lens {
			t.Errorf("%s: lens %q, ExifTool %q", path, v.LensModel, lens)
		}
		if v.ContentIdentifier != str("Keys:ContentIdentifier") {
			t.Errorf("%s: content identifier %q, ExifTool %q", path, v.ContentIdentifier, str("Keys:ContentIdentifier"))
		}
		// ReadXMP gives a movie's packet.
		packet, err := ReadXMP(bytes.NewReader(file), int64(len(file)))
		if hasXMP := str("XMP-exif:GPSLatitude") != ""; err != nil || (packet != nil) != hasXMP {
			t.Errorf("%s: ReadXMP %d bytes, %v; ExifTool reads XMP: %v", path, len(packet), err, hasXMP)
		}
	}
}

func TestParseLoci(t *testing.T) {
	fixed := func(f float64) []byte { return binary.BigEndian.AppendUint32(nil, uint32(int32(f*65536))) }
	loci := func(name []byte, lat, lon, alt float64) []byte {
		b := append(make([]byte, 6), name...) // version, flags, language
		b = append(b, 0)                      // role
		b = append(append(append(b, fixed(lon)...), fixed(lat)...), fixed(alt)...)
		return append(b, "earth\x00\x00"...)
	}
	for _, c := range []struct {
		name string
		b    []byte
		want *GPS
	}{
		{"UTF-8 name", loci([]byte("Home\x00"), 48.5, 11.25, 520), &GPS{Latitude: 48.5, Longitude: 11.25, Altitude: 520, HasAltitude: true}},
		{"UTF-16 name", loci([]byte("\xfe\xff\x00H\x00i\x00\x00"), -33.75, 151.5, -2), &GPS{Latitude: -33.75, Longitude: 151.5, Altitude: -2, HasAltitude: true}},
		{"no name", loci([]byte{0}, 1, 2, 3), &GPS{Latitude: 1, Longitude: 2, Altitude: 3, HasAltitude: true}},
		{"latitude past 90", loci([]byte{0}, 91, 2, 3), nil},
		{"cut short", loci([]byte{0}, 1, 2, 3)[:12], nil},
		{"name without its end", append(make([]byte, 6), "Home"...), nil},
	} {
		g := parseLoci(c.b)
		if (g == nil) != (c.want == nil) || g != nil && *g != *c.want {
			t.Errorf("%s: %+v, want %+v", c.name, g, c.want)
		}
	}
}

// A movie whose Keys date differs from its movie header's: Created is the
// Keys date, MovieCreated the header's.
func TestMovieCreatedApartFromKeys(t *testing.T) {
	file := readFile(t, "testdata/video/keys-mvhd.mov")
	v, err := ReadVideo(bytes.NewReader(file), int64(len(file)))
	if err != nil {
		t.Fatal(err)
	}
	keys := time.Date(2025, 5, 4, 13, 14, 15, 0, time.FixedZone("", 2*3600))
	mvhd := time.Date(2024, 6, 1, 10, 0, 0, 0, time.UTC)
	if !v.Created.Equal(keys) || !v.MovieCreated.Equal(mvhd) {
		t.Errorf("created %v, movie created %v; want %v and %v", v.Created, v.MovieCreated, keys, mvhd)
	}
}
