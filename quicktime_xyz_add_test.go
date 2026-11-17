package mediameta

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0x20h/go-media-metadata/internal/bmff/qtbuild"
)

// A position set by VideoEdit goes into the user data's ©xyz too, added
// where the movie has none, as ExifTool writes it; RemoveGPS removes it.
func TestVideoGPSAddsUserData(t *testing.T) {
	inputs := map[string][]byte{}
	for _, name := range []string{"keys.mov", "xmp.mp4", "udta.mov", "rotate90.mp4"} {
		inputs[name] = readFile(t, filepath.Join("testdata/video", name))
	}
	o := qtbuild.Default()
	o.MoovFirst = true
	inputs["qtbuild.mov"], _, _ = qtbuild.Build(o)
	g := GPS{Latitude: -33.8568, Longitude: 151.2153, Altitude: 5, HasAltitude: true}
	tool, _ := exec.LookPath("exiftool")
	dir := t.TempDir()
	for name, src := range inputs {
		hash := movieHash(t, src)
		for _, c := range []struct {
			kind string
			edit VideoEdit
		}{{"set", VideoEdit{GPS: &g}}, {"removed", VideoEdit{RemoveGPS: true}}} {
			var buf bytes.Buffer
			if err := SetVideoMetadata(bytes.NewReader(src), int64(len(src)), &buf, c.edit); err != nil {
				t.Fatalf("%s %s: %v", name, c.kind, err)
			}
			out := buf.Bytes()
			if c.kind == "removed" {
				// Removing from the movie with the position set.
				set := new(bytes.Buffer)
				if err := SetVideoMetadata(bytes.NewReader(src), int64(len(src)), set, VideoEdit{GPS: &g}); err != nil {
					t.Fatal(err)
				}
				buf.Reset()
				if err := SetVideoMetadata(bytes.NewReader(set.Bytes()), int64(set.Len()), &buf, c.edit); err != nil {
					t.Fatal(err)
				}
				out = buf.Bytes()
			}
			if movieHash(t, out) != hash {
				t.Errorf("%s %s: the media data hash changed", name, c.kind)
			}
			// After a removal, a position in the movie's XMP (which
			// RemoveGPS leaves to VideoEdit.XMP) is what's left.
			packet, _ := ReadXMP(bytes.NewReader(src), int64(len(src)))
			x, _ := ParseXMP(packet)
			v, err := ReadVideo(bytes.NewReader(out), int64(len(out)))
			want := &g
			if c.kind == "removed" {
				want = x.GPS
			}
			if err != nil || (want == nil) != (v.GPS == nil) || want != nil && math.Abs(v.GPS.Latitude-want.Latitude) > 1e-5 {
				t.Errorf("%s %s: GPS %+v, want %+v (%v)", name, c.kind, v.GPS, want, err)
			}
			if tool == "" {
				continue
			}
			p := filepath.Join(dir, c.kind+"-"+name)
			if err := os.WriteFile(p, out, 0o644); err != nil {
				t.Fatal(err)
			}
			res, err := exec.Command(tool, "-j", "-n", "-q", "-G1", "-UserData:GPSCoordinates", "-Keys:GPSCoordinates", p).Output()
			var got []map[string]any
			if err != nil || json.Unmarshal(res, &got) != nil || len(got) != 1 {
				t.Fatalf("%v %s", err, res)
			}
			for _, k := range []string{"UserData:GPSCoordinates", "Keys:GPSCoordinates"} {
				s := fmt.Sprint(got[0][k])
				if c.kind == "removed" {
					if _, ok := got[0][k]; ok {
						t.Errorf("%s removed: ExifTool still has %s %s", name, k, s)
					}
					continue
				}
				f := strings.Fields(s)
				if len(f) < 2 {
					t.Errorf("%s set: ExifTool %s %q", name, k, s)
					continue
				}
				var lat, lon float64
				if _, err := fmt.Sscan(f[0], &lat); err != nil {
					t.Fatal(err)
				}
				if _, err := fmt.Sscan(f[1], &lon); err != nil {
					t.Fatal(err)
				}
				if math.Abs(lat-g.Latitude) > 1e-5 || math.Abs(lon-g.Longitude) > 1e-5 {
					t.Errorf("%s set: ExifTool %s %q", name, k, s)
				}
			}
		}
	}
}
