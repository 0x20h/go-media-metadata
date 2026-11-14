package mediameta

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0x20h/go-media-metadata/internal/bmff/qtbuild"
)

// Every layout to every orientation: in place, only matrix bytes change,
// the media data stays, and the orientation reads back.
func TestPlanVideoOrientation(t *testing.T) {
	for _, l := range movieLayouts {
		for o := Orientation(1); o <= 8; o++ {
			t.Run(fmt.Sprintf("%s/%d", l.name, o), func(t *testing.T) {
				opts := qtbuild.Default()
				l.edit(&opts)
				file, start, end := qtbuild.Build(opts)
				p, err := planVideoOrientation(bytes.NewReader(file), int64(len(file)), o)
				if opts.NoVideo {
					if !errors.Is(err, ErrUnsupported) {
						t.Fatalf("no video track: %v, want ErrUnsupported", err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if !p.InPlace {
					t.Fatal("not in place")
				}
				checkPlan(t, file, p)
				n := 0
				for _, pt := range p.Patches {
					n += len(pt.Data)
				}
				if n > 36 {
					t.Errorf("%d bytes patched for one video track", n)
				}
				var out bytes.Buffer
				if err := SetOrientation(bytes.NewReader(file), int64(len(file)), &out, o); err != nil {
					t.Fatal(err)
				}
				written := out.Bytes()
				if !bytes.Equal(written[start:end], file[start:end]) {
					t.Fatal("the media data changed")
				}
				v := readVideo(t, written)
				if v.Orientation != o {
					t.Errorf("reads %v back", v.Orientation)
				}
				before := readVideo(t, file)
				before.Orientation = v.Orientation
				if (before.GPS == nil) != (v.GPS == nil) || before.GPS != nil && *before.GPS != *v.GPS {
					t.Errorf("GPS %+v, was %+v", v.GPS, before.GPS)
				}
				before.GPS, v.GPS = nil, nil
				if before != v {
					t.Errorf("other fields changed: %+v, was %+v", v, before)
				}
			})
		}
	}
}

// The matrix written for a portrait recording is the one cameras write:
// rotated 90° with the stored height as the x translation.
func TestVideoOrientationMatrix(t *testing.T) {
	o := qtbuild.Default()
	o.Matrix = [4]int32{1 << 16, 0, 0, 1 << 16}
	file, _, _ := qtbuild.Build(o)
	p, err := planVideoRotation(bytes.NewReader(file), int64(len(file)), 90)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Patches) != 1 {
		t.Fatalf("%d patches", len(p.Patches))
	}
	var out bytes.Buffer
	if _, err := p.WriteTo(&out); err != nil {
		t.Fatal(err)
	}
	tracks, err := videoTracks(bytes.NewReader(out.Bytes()), int64(out.Len()))
	if err != nil {
		t.Fatal(err)
	}
	got := tracks[0].matrix
	want := []int32{0, 1 << 16, 0, -1 << 16, 0, 0, 1080 << 16, 0, 1 << 30}
	for i, w := range want {
		if v := int32(binary.BigEndian.Uint32(got[4*i:])); v != w {
			t.Errorf("matrix[%d] = %d, want %d", i, v, w)
		}
	}
}

// A rotation keeps a mirroring, as the Exif numbering has it.
func TestVideoRotationKeepsMirror(t *testing.T) {
	o := qtbuild.Default()
	o.Matrix = [4]int32{-1 << 16, 0, 0, 1 << 16} // 2: mirrored left-right
	file, _, _ := qtbuild.Build(o)
	for deg, want := range map[int]Orientation{0: 2, 90: 7, 180: 4, 270: 5, -90: 5, 450: 7} {
		var out bytes.Buffer
		if err := SetRotation(bytes.NewReader(file), int64(len(file)), &out, deg); err != nil {
			t.Fatal(err)
		}
		if got := readVideo(t, out.Bytes()).Orientation; got != want {
			t.Errorf("%d°: %v, want %v", deg, got, want)
		}
	}
	if _, err := planVideoRotation(bytes.NewReader(file), int64(len(file)), 45); err == nil {
		t.Error("45° accepted")
	}
	if _, err := planVideoOrientation(bytes.NewReader(file), int64(len(file)), 9); err == nil {
		t.Error("orientation 9 accepted")
	}
}

// Setting the orientation a movie has changes nothing.
func TestVideoOrientationUnchanged(t *testing.T) {
	file, _, _ := qtbuild.Build(qtbuild.Default())
	p, err := planVideoOrientation(bytes.NewReader(file), int64(len(file)), 6)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Patches) != 0 {
		t.Errorf("%d patches", len(p.Patches))
	}
}

// Every video track turns; a movie header with its own matrix is refused,
// and read as no orientation.
func TestVideoOrientationTracks(t *testing.T) {
	o := qtbuild.Default()
	o.VideoTracks = 2
	file, _, _ := qtbuild.Build(o)
	p, err := planVideoOrientation(bytes.NewReader(file), int64(len(file)), 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Patches) != 2 {
		t.Errorf("%d patches for two video tracks", len(p.Patches))
	}
	checkPlan(t, file, p)

	o = qtbuild.Default()
	o.MovieMatrix = [4]int32{0, 1 << 16, -1 << 16, 0}
	file, _, _ = qtbuild.Build(o)
	if _, err := planVideoOrientation(bytes.NewReader(file), int64(len(file)), 1); !errors.Is(err, ErrUnsupported) {
		t.Errorf("a turned movie header: %v, want ErrUnsupported", err)
	}
	if v := readVideo(t, file); v.Orientation != 0 {
		t.Errorf("a turned movie header reads %v", v.Orientation)
	}
}

func FuzzVideoOrientation(f *testing.F) {
	movieSeeds(f)
	f.Fuzz(func(t *testing.T, data []byte) {
		withinDeadline(t, func() {
			// The writer reads only the track headers, so it takes
			// some movies the reader refuses; those needn't read back.
			_, readErr := ReadVideo(bytes.NewReader(data), int64(len(data)))
			for _, o := range []Orientation{1, 6, 7} {
				p, err := planVideoOrientation(bytes.NewReader(data), int64(len(data)), o)
				if err != nil {
					continue
				}
				if !p.InPlace {
					t.Fatal("a video orientation plan not in place")
				}
				checkPlan(t, data, p)
				var out bytes.Buffer
				if _, err := p.WriteTo(&out); err != nil {
					t.Fatal(err)
				}
				if readErr != nil {
					continue
				}
				v, err := ReadVideo(bytes.NewReader(out.Bytes()), int64(out.Len()))
				if err != nil {
					t.Fatalf("the written movie doesn't read: %v", err)
				}
				if v.Orientation != o && v.Orientation != 0 {
					t.Fatalf("reads %v, set %v", v.Orientation, o)
				}
			}
		})
	})
}

func BenchmarkPlanVideoRotation(b *testing.B) {
	o := qtbuild.Default()
	o.MediaSize = 1 << 20
	file, _, _ := qtbuild.Build(o)
	cr := &countingReader{r: bytes.NewReader(file)}
	b.ReportAllocs()
	for range b.N {
		if _, err := planVideoRotation(cr, int64(len(file)), 180); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(cr.bytes)/float64(b.N), "read-B/op")
}

// ExifTool and FFmpeg's ffprobe read the rotation written. Each runs where
// it's installed; skipped elsewhere (CI).
func TestVideoRotationOtherTools(t *testing.T) {
	_, errET := exec.LookPath("exiftool")
	_, errFF := exec.LookPath("ffprobe")
	if errET != nil && errFF != nil {
		t.Skip("neither exiftool nor ffprobe installed")
	}
	dir := t.TempDir()
	base := qtbuild.Default()
	base.Matrix = [4]int32{1 << 16, 0, 0, 1 << 16}
	file, _, _ := qtbuild.Build(base)
	for _, deg := range []int{0, 90, 180, 270} {
		var out bytes.Buffer
		if err := SetRotation(bytes.NewReader(file), int64(len(file)), &out, deg); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, fmt.Sprintf("r%d.mov", deg))
		if err := os.WriteFile(p, out.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
		if errET == nil {
			b, err := exec.Command("exiftool", "-json", "-n", "-Rotation", p).Output()
			if err != nil {
				t.Fatal(err)
			}
			var got []struct{ Rotation int }
			if err := json.Unmarshal(b, &got); err != nil || len(got) != 1 || got[0].Rotation != deg {
				t.Errorf("%d°: ExifTool reads %s", deg, b)
			}
		}
		if errFF == nil {
			// ffprobe gives the display matrix's rotation anticlockwise,
			// as a side data "rotation" of the video stream.
			b, err := exec.Command("ffprobe", "-v", "error", "-select_streams", "v:0",
				"-show_entries", "stream_side_data=rotation", "-of", "csv=p=0", p).Output()
			if err != nil {
				t.Fatalf("ffprobe: %v", err)
			}
			// Anticlockwise, in (-180, 180]: 90° clockwise is -90, 270° is
			// 90, 180° either sign; no rotation, no side data.
			s := strings.TrimSpace(string(b))
			want := map[int]string{0: "", 90: "-90", 180: "180", 270: "90"}[deg]
			if s != want && (deg != 180 || s != "-180") {
				t.Errorf("%d°: ffprobe reads rotation %q, want %q", deg, s, want)
			}
		}
	}
}
