package mediameta

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/0x20h/go-media-metadata/internal/bmff"
	"github.com/0x20h/go-media-metadata/internal/bmff/qtbuild"
)

var (
	editDate  = time.Date(2023, 5, 6, 7, 8, 9, 0, time.FixedZone("", -4*3600))
	editPlace = &GPS{Latitude: -33.856784, Longitude: 151.215297, Altitude: 12.5, HasAltitude: true}
)

// videoEdits are the edits every layout gets.
var videoEdits = []struct {
	name string
	edit VideoEdit
}{
	{"date", VideoEdit{Created: &editDate}},
	{"place", VideoEdit{GPS: editPlace}},
	{"place without altitude", VideoEdit{GPS: &GPS{Latitude: 1.5, Longitude: -2.25}}},
	{"date and place", VideoEdit{Created: &editDate, GPS: editPlace}},
	{"remove place", VideoEdit{RemoveGPS: true}},
	{"date, remove place", VideoEdit{Created: &editDate, RemoveGPS: true}},
}

// chunkOffsets are the chunk offsets of every track of a movie.
func chunkOffsets(t *testing.T, file []byte) []int64 {
	t.Helper()
	r := bytes.NewReader(file)
	top, err := bmff.ReadBoxes(r, 0, int64(len(file)))
	if err != nil {
		t.Fatal(err)
	}
	moov, _ := bmff.Find(top, bmff.T("moov"))
	var out []int64
	var walk func(start, end int64, depth int)
	walk = func(start, end int64, depth int) {
		boxes, err := bmff.ReadBoxes(r, start, end)
		if err != nil {
			return
		}
		for _, b := range boxes {
			switch string(b.Type[:]) {
			case "trak", "mdia", "minf", "stbl":
				walk(b.Body(), b.End(), depth+1)
			case "stco", "co64":
				p := file[b.Body():b.End()]
				n := int(binary.BigEndian.Uint32(p[4:]))
				for i := range n {
					if b.Type == bmff.T("co64") {
						out = append(out, int64(binary.BigEndian.Uint64(p[8+8*i:])))
					} else {
						out = append(out, int64(binary.BigEndian.Uint32(p[8+4*i:])))
					}
				}
			}
		}
	}
	walk(moov.Body(), moov.End(), 0)
	return out
}

// checkVideoEdit checks a written movie against its source and the edit:
// the plan's consistency, the values read back, the other fields kept, and
// every chunk offset pointing at the same media bytes as before.
func checkVideoEdit(t *testing.T, src []byte, edit VideoEdit, p *Plan) []byte {
	t.Helper()
	checkPlan(t, src, p)
	var out bytes.Buffer
	if _, err := p.WriteTo(&out); err != nil {
		t.Fatal(err)
	}
	got := out.Bytes()
	before, after := readVideo(t, src), readVideo(t, got)
	if edit.Created != nil {
		if !after.Created.Equal(*edit.Created) || !after.OffsetKnown {
			t.Errorf("Created %v, want %v", after.Created, *edit.Created)
		}
		if _, off := after.Created.Zone(); off != -4*3600 {
			t.Errorf("Created's offset %d", off)
		}
	} else if !after.Created.Equal(before.Created) {
		t.Errorf("Created %v, was %v", after.Created, before.Created)
	}
	switch {
	case edit.GPS != nil:
		g := after.GPS
		if g == nil || math.Abs(g.Latitude-edit.GPS.Latitude) > 1e-6 || math.Abs(g.Longitude-edit.GPS.Longitude) > 1e-6 ||
			g.HasAltitude != edit.GPS.HasAltitude || math.Abs(g.Altitude-edit.GPS.Altitude) > 1e-3 {
			t.Errorf("GPS %+v, want %+v", g, edit.GPS)
		}
	case edit.RemoveGPS:
		if after.GPS != nil {
			t.Errorf("GPS %+v after removing it", after.GPS)
		}
	default:
		if (after.GPS == nil) != (before.GPS == nil) || after.GPS != nil && *after.GPS != *before.GPS {
			t.Errorf("GPS %+v, was %+v", after.GPS, before.GPS)
		}
	}
	if after.Orientation != before.Orientation || after.Width != before.Width || after.Duration != before.Duration ||
		after.Make != before.Make || after.Model != before.Model || after.ContentIdentifier != before.ContentIdentifier {
		t.Errorf("other fields changed: %+v, was %+v", after, before)
	}
	oldOffs, newOffs := chunkOffsets(t, src), chunkOffsets(t, got)
	if len(oldOffs) != len(newOffs) {
		t.Fatalf("%d chunk offsets, were %d", len(newOffs), len(oldOffs))
	}
	// Chunks that point into an mdat's payload (a movie without media
	// data may point anywhere).
	top, err := bmff.ReadBoxes(bytes.NewReader(src), 0, int64(len(src)))
	if err != nil {
		t.Fatal(err)
	}
	inMdat := func(off, n int64) bool {
		for _, b := range top {
			if b.Type == bmff.T("mdat") && off >= b.Body() && off+n <= b.End() {
				return true
			}
		}
		return false
	}
	for i := range oldOffs {
		const n = 32
		if !inMdat(oldOffs[i], n) {
			continue
		}
		if newOffs[i]+n > int64(len(got)) || !bytes.Equal(got[newOffs[i]:newOffs[i]+n], src[oldOffs[i]:oldOffs[i]+n]) {
			t.Fatalf("chunk %d at %d (was %d) doesn't hold its media data", i, newOffs[i], oldOffs[i])
		}
	}
	return got
}

func TestPlanVideoEdit(t *testing.T) {
	for _, l := range movieLayouts {
		for _, e := range videoEdits {
			t.Run(l.name+"/"+e.name, func(t *testing.T) {
				o := qtbuild.Default()
				l.edit(&o)
				file, start, end := qtbuild.Build(o)
				p, err := PlanVideoEdit(bytes.NewReader(file), int64(len(file)), e.edit)
				if err != nil {
					t.Fatal(err)
				}
				got := checkVideoEdit(t, file, e.edit, p)
				// The media data itself is byte for byte there.
				if !bytes.Contains(got, file[start:end]) {
					t.Fatal("the media data isn't in the written file")
				}
				// A second identical edit changes nothing.
				p2, err := PlanVideoEdit(bytes.NewReader(got), int64(len(got)), e.edit)
				if err != nil {
					t.Fatal(err)
				}
				if !p2.InPlace || len(p2.Patches) != 0 {
					t.Errorf("a second identical edit: in place %v, %d patches", p2.InPlace, len(p2.Patches))
				}
			})
		}
	}
}

// An edit that changes nothing is a plan without patches.
func TestPlanVideoEditNothing(t *testing.T) {
	for _, l := range movieLayouts {
		o := qtbuild.Default()
		l.edit(&o)
		file, _, _ := qtbuild.Build(o)
		p, err := PlanVideoEdit(bytes.NewReader(file), int64(len(file)), VideoEdit{})
		if err != nil {
			t.Fatalf("%s: %v", l.name, err)
		}
		if !p.InPlace || len(p.Patches) != 0 {
			t.Errorf("%s: in place %v, %d patches", l.name, p.InPlace, len(p.Patches))
		}
	}
}

// Where the movie box keeps its size, the edit is in place; where it
// grows, it takes the room of a free box after it, else the file grows
// and, with moov first, the chunk offsets move.
func TestPlanVideoEditLayout(t *testing.T) {
	same := VideoEdit{GPS: &GPS{Latitude: 10, Longitude: 20, Altitude: 34, HasAltitude: true}}
	for _, c := range []struct {
		name      string
		opts      func(*qtbuild.Options)
		edit      VideoEdit
		inPlace   bool
		offsMoved bool
	}{
		// "+52.5200+013.4050+034.000/" → "+10.000000+020.000000+34.000/":
		// longer, so moov grows.
		{"grows, moov last", func(*qtbuild.Options) {}, same, false, false},
		{"grows, moov first", func(o *qtbuild.Options) { o.MoovFirst = true }, same, false, true},
		{"grows into free, moov first", func(o *qtbuild.Options) { o.MoovFirst, o.FreeAfterMoov = true, 256 }, same, true, false},
		{"grows into free, moov last", func(o *qtbuild.Options) { o.FreeAfterMoov = 256 }, same, true, false},
		{"free too small", func(o *qtbuild.Options) { o.MoovFirst, o.FreeAfterMoov = true, 8 }, same, false, true},
		// Removing the place shrinks moov by more than 8 bytes: padded.
		{"shrinks, moov first", func(o *qtbuild.Options) { o.MoovFirst = true }, VideoEdit{RemoveGPS: true}, true, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			o := qtbuild.Default()
			c.opts(&o)
			file, _, _ := qtbuild.Build(o)
			p, err := PlanVideoEdit(bytes.NewReader(file), int64(len(file)), c.edit)
			if err != nil {
				t.Fatal(err)
			}
			if p.InPlace != c.inPlace {
				t.Errorf("in place %v, want %v (size %d → %d)", p.InPlace, c.inPlace, p.Size, p.NewSize)
			}
			got := checkVideoEdit(t, file, c.edit, p)
			moved := chunkOffsets(t, got)[0] != chunkOffsets(t, file)[0]
			if moved != c.offsMoved {
				t.Errorf("chunk offsets moved %v, want %v", moved, c.offsMoved)
			}
		})
	}
}

// Offsets this package doesn't move make a size-changing edit refuse.
func TestPlanVideoEditRefuses(t *testing.T) {
	grow := VideoEdit{Created: &editDate, GPS: &GPS{Latitude: 10, Longitude: 20, Altitude: 34, HasAltitude: true}}
	o := qtbuild.Default()
	o.MoovFirst = true
	file, _, _ := qtbuild.Build(o)
	frag := append(bytes.Clone(file), 0, 0, 0, 16, 'm', 'o', 'o', 'f', 0, 0, 0, 8, 'm', 'f', 'h', 'd')
	if _, err := PlanVideoEdit(bytes.NewReader(frag), int64(len(frag)), grow); !errors.Is(err, ErrUnsupported) {
		t.Errorf("a fragment after moov: %v, want ErrUnsupported", err)
	}
	// An edit in place doesn't move anything, so it's fine.
	if _, err := PlanVideoEdit(bytes.NewReader(frag), int64(len(frag)), VideoEdit{}); err != nil {
		t.Errorf("no edit: %v", err)
	}
	for _, bad := range []VideoEdit{
		{GPS: &GPS{Latitude: 91}},
		{GPS: &GPS{Longitude: math.NaN()}},
		{GPS: editPlace, RemoveGPS: true},
	} {
		if _, err := PlanVideoEdit(bytes.NewReader(file), int64(len(file)), bad); err == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
	jpeg := plainJPEG(t)
	if _, err := PlanVideoEdit(bytes.NewReader(jpeg), int64(len(jpeg)), grow); !errors.Is(err, ErrNotQuickTime) {
		t.Errorf("a JPEG: %v", err)
	}
}

func TestShiftOffsets(t *testing.T) {
	p := []byte{0, 0, 0, 0, 0, 0, 0, 2, 0, 0, 0, 10, 0xFF, 0xFF, 0xFF, 0xF0}
	out, err := shiftOffsets(p, false, 0, 8)
	if err != nil || binary.BigEndian.Uint32(out[8:]) != 18 || binary.BigEndian.Uint32(out[12:]) != 0xFFFFFFF8 {
		t.Errorf("%x, %v", out, err)
	}
	if _, err := shiftOffsets(p, false, 0, 16); !errors.Is(err, ErrUnsupported) {
		t.Errorf("an offset past 32 bits: %v", err)
	}
	if _, err := shiftOffsets(p[:12], false, 0, 1); !errors.Is(err, ErrMalformed) {
		t.Errorf("a short box: %v", err)
	}
	// Offsets before the movie box stay.
	out, _ = shiftOffsets(p, false, 11, 8)
	if binary.BigEndian.Uint32(out[8:]) != 10 {
		t.Errorf("an offset before the movie box moved: %x", out)
	}
}

func FuzzVideoEdit(f *testing.F) {
	movieSeeds(f)
	f.Fuzz(func(t *testing.T, data []byte) {
		withinDeadline(t, func() {
			_, readErr := ReadVideo(bytes.NewReader(data), int64(len(data)))
			for _, e := range videoEdits[3:5] {
				p, err := PlanVideoEdit(bytes.NewReader(data), int64(len(data)), e.edit)
				if err != nil {
					continue
				}
				checkPlan(t, data, p)
				if readErr != nil {
					continue
				}
				var out bytes.Buffer
				if _, err := p.WriteTo(&out); err != nil {
					t.Fatal(err)
				}
				v, err := ReadVideo(bytes.NewReader(out.Bytes()), int64(out.Len()))
				if err != nil {
					t.Fatalf("the written movie doesn't read: %v", err)
				}
				if e.edit.Created != nil && !v.Created.Equal(*e.edit.Created) {
					t.Fatalf("Created %v", v.Created)
				}
				if e.edit.RemoveGPS && v.GPS != nil {
					t.Fatalf("GPS %+v after removing it", v.GPS)
				}
			}
		})
	})
}

// ExifTool and ffprobe read what's written, in both layouts, and ExifTool
// finds no new problem in the file. Runs where they're installed.
func TestVideoEditOtherTools(t *testing.T) {
	_, errET := exec.LookPath("exiftool")
	_, errFF := exec.LookPath("ffprobe")
	if errET != nil && errFF != nil {
		t.Skip("neither exiftool nor ffprobe installed")
	}
	dir := t.TempDir()
	for _, l := range movieLayouts {
		if l.name == "Keys in udta" || l.name == "MP4 meta FullBox" {
			continue // Keys placements ExifTool doesn't read
		}
		o := qtbuild.Default()
		l.edit(&o)
		file, _, _ := qtbuild.Build(o)
		edit := VideoEdit{Created: &editDate, GPS: editPlace}
		var out bytes.Buffer
		if err := EditVideo(bytes.NewReader(file), int64(len(file)), &out, edit); err != nil {
			t.Fatal(err)
		}
		name := strings.ReplaceAll(l.name, " ", "-")
		before, after := filepath.Join(dir, name+"-before.mov"), filepath.Join(dir, name+".mov")
		if err := os.WriteFile(before, file, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(after, out.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
		if errET == nil {
			checkExifToolVideo(t, l.name, before, after)
		}
		if errFF == nil {
			checkFFprobeVideo(t, l.name, after, edit)
		}
	}
}

func checkExifToolVideo(t *testing.T, name, before, after string) {
	t.Helper()
	b, err := exec.Command("exiftool", "-s3", "-n", "-Keys:CreationDate", "-Keys:GPSCoordinates", after).Output()
	if err != nil {
		t.Fatalf("%s: exiftool: %v", name, err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 2 || lines[0] != "2023:05:06 07:08:09-04:00" || !strings.HasPrefix(lines[1], "-33.856784 151.215297 12.5") {
		t.Errorf("%s: ExifTool reads %q", name, lines)
	}
	warnings := func(p string) string {
		b, _ := exec.Command("exiftool", "-validate", "-warning", "-error", "-a", "-s3", p).Output()
		return string(b)
	}
	if w0, w1 := warnings(before), warnings(after); w1 != w0 {
		t.Errorf("%s: ExifTool's validation changed:\nbefore: %s\nafter: %s", name, w0, w1)
	}
}

func checkFFprobeVideo(t *testing.T, name, path string, edit VideoEdit) {
	t.Helper()
	b, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "format_tags", "-of", "default=nw=1", path).Output()
	if err != nil {
		t.Fatalf("%s: ffprobe: %v", name, err)
	}
	s := string(b)
	if !strings.Contains(s, "com.apple.quicktime.location.ISO6709="+iso6709(edit.GPS)) {
		t.Errorf("%s: ffprobe doesn't read the place:\n%s", name, s)
	}
	if !strings.Contains(s, "com.apple.quicktime.creationdate=2023-05-06T07:08:09-0400") {
		t.Errorf("%s: ffprobe doesn't read the Keys date:\n%s", name, s)
	}
	if !strings.Contains(s, "creation_time=2023-05-06T11:08:09") {
		t.Errorf("%s: ffprobe doesn't read the movie header's date:\n%s", name, s)
	}
}

// Real encoded movies, made by FFmpeg with moov first and last, as MOV and
// MP4: after the edit every decoded frame is the same (framemd5), and
// FFmpeg and ExifTool read the values. Runs where ffmpeg is installed.
func TestVideoEditEncoded(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	dir := t.TempDir()
	for _, c := range []struct{ name, ext, flags string }{
		{"moov-last", "mov", ""},
		{"moov-first", "mov", "+faststart"},
		{"mp4-moov-last", "mp4", ""},
		{"mp4-moov-first", "mp4", "+faststart"},
	} {
		t.Run(c.name, func(t *testing.T) {
			src := filepath.Join(dir, c.name+"."+c.ext)
			args := []string{"-v", "error", "-f", "lavfi", "-i", "testsrc=duration=2:size=160x120:rate=10",
				"-f", "lavfi", "-i", "sine=duration=2", "-c:v", "mpeg4", "-c:a", "aac", "-shortest"}
			if c.flags != "" {
				args = append(args, "-movflags", c.flags)
			}
			if out, err := exec.Command("ffmpeg", append(args, src)...).CombinedOutput(); err != nil {
				t.Fatalf("ffmpeg: %v\n%s", err, out)
			}
			file, err := os.ReadFile(src)
			if err != nil {
				t.Fatal(err)
			}
			edit := VideoEdit{Created: &editDate, GPS: editPlace}
			p, err := PlanVideoEdit(bytes.NewReader(file), int64(len(file)), edit)
			if err != nil {
				t.Fatal(err)
			}
			got := checkVideoEdit(t, file, edit, p)
			dst := filepath.Join(dir, c.name+"-edited."+c.ext)
			if err := os.WriteFile(dst, got, 0o600); err != nil {
				t.Fatal(err)
			}
			md5 := func(p string) string {
				out, err := exec.Command("ffmpeg", "-v", "error", "-i", p, "-map", "0", "-f", "framemd5", "-").Output()
				if err != nil {
					t.Fatalf("ffmpeg framemd5 %s: %v", p, err)
				}
				return string(out)
			}
			if a, b := md5(src), md5(dst); a != b || len(a) < 100 {
				t.Errorf("decoded frames differ (or none): %d vs %d bytes of framemd5", len(a), len(b))
			}
			checkFFprobeVideo(t, c.name, dst, edit)
			if _, err := exec.LookPath("exiftool"); err == nil {
				checkExifToolVideo(t, c.name, src, dst)
			}
			t.Logf("%s: %d → %d bytes, in place %v, %d patches", c.name, p.Size, p.NewSize, p.InPlace, len(p.Patches))
		})
	}
}

func BenchmarkPlanVideoEdit(b *testing.B) {
	for _, moovFirst := range []bool{false, true} {
		o := qtbuild.Default()
		o.MoovFirst, o.MediaSize = moovFirst, 1<<20
		file, _, _ := qtbuild.Build(o)
		edit := VideoEdit{Created: &editDate, GPS: editPlace}
		b.Run(map[bool]string{false: "moov-last", true: "moov-first"}[moovFirst], func(b *testing.B) {
			cr := &countingReader{r: bytes.NewReader(file)}
			b.ReportAllocs()
			for range b.N {
				if _, err := PlanVideoEdit(cr, int64(len(file)), edit); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(cr.bytes)/float64(b.N), "read-B/op")
		})
	}
}
