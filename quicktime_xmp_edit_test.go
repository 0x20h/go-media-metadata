package mediameta

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0x20h/go-media-metadata/internal/bmff"
	"github.com/0x20h/go-media-metadata/internal/bmff/qtbuild"
)

func movieHash(t *testing.T, file []byte) [32]byte {
	t.Helper()
	h := sha256.New()
	if _, err := HashImageData(bytes.NewReader(file), int64(len(file)), h); err != nil {
		t.Fatalf("HashImageData: %v", err)
	}
	return [32]byte(h.Sum(nil))
}

// movieXMPEdits are a small edit, which fits an MP4's uuid box, and one
// too big for it, which moves the packet into the user data.
func movieXMPEdits() map[string]XMPEdit {
	var many []Region
	var names []string
	for i := range 60 {
		n := fmt.Sprintf("Person number %d with a long name", i)
		names = append(names, n)
		many = append(many, Region{Name: n, Type: "Face", X: 0.5, Y: 0.5, W: 0.1, H: 0.1})
	}
	return map[string]XMPEdit{
		"small": {PersonInImage: &[]string{"Ada"}, Regions: &Regions{AppliedTo: Dimensions{W: 64, H: 48, Unit: "pixel"},
			List: []Region{{Name: "Ada", Type: "Face", X: 0.5, Y: 0.5, W: 0.2, H: 0.3}}}},
		"large": {PersonInImage: &names, Regions: &Regions{AppliedTo: Dimensions{W: 64, H: 48, Unit: "pixel"}, List: many}},
	}
}

func TestMovieXMPEdit(t *testing.T) {
	inputs := map[string][]byte{}
	for _, name := range []string{"xmp.mp4", "xmp.mov", "keys.mov", "rotate90.mp4"} {
		inputs[name] = readFile(t, filepath.Join("testdata/video", name))
	}
	for _, first := range []bool{true, false} {
		o := qtbuild.Default()
		o.MoovFirst = first
		f, _, _ := qtbuild.Build(o)
		inputs[fmt.Sprintf("qtbuild-moovfirst-%v.mov", first)] = f
	}
	tool, _ := exec.LookPath("exiftool")
	ffmpeg, _ := exec.LookPath("ffmpeg")
	dir := t.TempDir()
	for name, src := range inputs {
		before, _ := ReadXMP(bytes.NewReader(src), int64(len(src)))
		oldX, _ := ParseXMP(before)
		hash := movieHash(t, src)
		video, err := ReadVideo(bytes.NewReader(src), int64(len(src)))
		if err != nil {
			t.Fatal(err)
		}
		for kind, edit := range movieXMPEdits() {
			edit := edit
			// Through PlanVideoMetadata, and PlanMetadata's XMP part.
			p, err := PlanMetadata(bytes.NewReader(src), int64(len(src)), MetadataEdit{XMP: &edit})
			if err != nil {
				t.Fatalf("%s %s: %v", name, kind, err)
			}
			var buf bytes.Buffer
			if _, err := p.WriteTo(&buf); err != nil {
				t.Fatal(err)
			}
			out := buf.Bytes()
			p2, err := PlanVideoMetadata(bytes.NewReader(src), int64(len(src)), VideoEdit{XMP: &edit})
			var buf2 bytes.Buffer
			if err != nil {
				t.Fatal(err)
			}
			if _, err := p2.WriteTo(&buf2); err != nil || !bytes.Equal(buf2.Bytes(), out) {
				t.Errorf("%s %s: PlanMetadata and PlanVideoMetadata differ", name, kind)
			}
			packet, err := ReadXMP(bytes.NewReader(out), int64(len(out)))
			if err != nil {
				t.Fatalf("%s %s: ReadXMP %v", name, kind, err)
			}
			x, err := ParseXMP(packet)
			if err != nil || !slicesEqual(x.PersonInImage, *edit.PersonInImage) || x.Regions == nil || len(x.Regions.List) != len(edit.Regions.List) {
				t.Errorf("%s %s: XMP %+v, %v", name, kind, x, err)
			}
			// The rest of the packet stays (an old title, a date).
			if x.Title != oldX.Title || !sameXMPDate(x.CreateDate, oldX.CreateDate) || !sameXMPDate(x.DateCreated, oldX.DateCreated) {
				t.Errorf("%s %s: the packet's other fields changed: %+v, before %+v", name, kind, x, oldX)
			}
			if name == "xmp.mp4" {
				// The packet's place: its uuid box when it fits (in place),
				// else the user data, the box freed and zeroed.
				top, _ := bmff.ReadBoxes(bytes.NewReader(out), 0, int64(len(out)))
				_, hasUUID := bmff.Find(top, bmff.T("uuid"))
				switch kind {
				case "small":
					if !p.InPlace || !hasUUID {
						t.Errorf("small: in place %v, uuid box %v", p.InPlace, hasUUID)
					}
				case "large":
					if hasUUID {
						t.Error("large: the uuid box is still there")
					}
				}
				if n := bytes.Count(out, []byte("Madrid")); n != 1 {
					t.Errorf("%s: the old title %d times in the file", kind, n)
				}
			}
			if movieHash(t, out) != hash {
				t.Errorf("%s %s: the media data hash changed", name, kind)
			}
			v, err := ReadVideo(bytes.NewReader(out), int64(len(out)))
			if err != nil || v.Duration != video.Duration || v.Width != video.Width || !v.Created.Equal(video.Created) {
				t.Errorf("%s %s: ReadVideo %+v (%v), before %+v", name, kind, v, err, video)
			}
			path := filepath.Join(dir, kind+"-"+name)
			if err := os.WriteFile(path, out, 0o644); err != nil {
				t.Fatal(err)
			}
			if ffmpeg != "" && !strings.HasPrefix(name, "qtbuild") {
				if msg, err := exec.Command(ffmpeg, "-v", "error", "-i", path, "-f", "null", "-").CombinedOutput(); err != nil || len(msg) > 0 {
					t.Errorf("%s %s: ffmpeg: %v %s", name, kind, err, msg)
				}
			}
			if tool == "" {
				continue
			}
			res, err := exec.Command(tool, "-j", "-q", "-validate", "-warning", "-XMP-iptcExt:PersonInImage", "-XMP-mwg-rs:RegionName", path).Output()
			if err != nil {
				t.Fatal(err)
			}
			var got []map[string]any
			if err := json.Unmarshal(res, &got); err != nil || len(got) != 1 {
				t.Fatalf("%v\n%s", err, res)
			}
			if w, ok := got[0]["Warning"]; ok {
				t.Errorf("%s %s: ExifTool warns %v", name, kind, w)
			}
			if fmt.Sprint(got[0]["PersonInImage"]) != fmt.Sprint(oneOrList(*edit.PersonInImage)) {
				t.Errorf("%s %s: ExifTool PersonInImage %v", name, kind, got[0]["PersonInImage"])
			}
		}
	}
}

// oneOrList is a list as ExifTool's JSON gives it: a single value alone.
func oneOrList(v []string) any {
	if len(v) == 1 {
		return v[0]
	}
	out := make([]any, len(v))
	for i, s := range v {
		out[i] = s
	}
	return out
}

func slicesEqual(a, b []string) bool {
	return strings.Join(a, "\x00") == strings.Join(b, "\x00")
}

// A movie's Exif isn't written: the error says where its fields go.
func TestMovieExifRefused(t *testing.T) {
	src := readFile(t, "testdata/video/keys.mov")
	if _, err := PlanMetadata(bytes.NewReader(src), int64(len(src)), MetadataEdit{Exif: &ExifEdit{ImageDescription: ptr("x")}}); err == nil ||
		!strings.Contains(err.Error(), "VideoEdit") {
		t.Errorf("Exif in a movie: %v", err)
	}
}
