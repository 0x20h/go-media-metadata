package mediameta

import (
	"bytes"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0x20h/go-media-metadata/internal/bmff"
)

// trackBoxes is each track's box of type t in the movie (nil where it
// has none), in track order, and the track's composition span.
func trackBoxes(t *testing.T, file []byte, typ string) (boxes [][]byte, spans [][2]int64) {
	t.Helper()
	top, err := bmff.ParseBoxes(file, 0, int64(len(file)))
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range top {
		if b.Type != bmff.T("moov") {
			continue
		}
		moov, err := parseQTNode(file[b.Offset:b.End()], 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, trak := range moov.kids {
			if trak.typ != bmff.T("trak") {
				continue
			}
			var box []byte
			if n := trak.child(bmff.T(typ)); n != nil {
				box = n.encode()
			}
			boxes = append(boxes, box)
			var span [2]int64
			if stbl := path(trak, "mdia", "minf", "stbl"); stbl != nil {
				if stts, ctts := stbl.child(bmff.T("stts")), stbl.child(bmff.T("ctts")); stts != nil && ctts != nil {
					if first, end, ok, _ := compositionSpan(stts.payload(), ctts.payload()); ok {
						span = [2]int64{first, end}
					}
				}
			}
			spans = append(spans, span)
		}
	}
	return boxes, spans
}

// A movie with B-frames and no edit list gets the edit list ffmpeg's
// muxer writes for it: the same video edts as ffmpeg's own file of the
// same streams, none for the audio, every frame the same, and both
// streams starting at 0 as ffprobe (a player) presents them.
func TestStartAtFirstFrame(t *testing.T) {
	src := readFile(t, "testdata/video/bframes-noedts.mp4")
	ref := readFile(t, "testdata/video/bframes.mp4")
	out := rewritten(t, src, RewriteOptions{StartAtFirstFrame: true})

	got, spans := trackBoxes(t, out, "edts")
	want, _ := trackBoxes(t, ref, "edts")
	if len(got) != 2 || got[0] == nil || !bytes.Equal(got[0], want[0]) {
		t.Errorf("video edts %x, ffmpeg's %x", got[0], want[0])
	}
	if got[1] != nil {
		t.Errorf("audio edts %x, want none", got[1])
	}
	// The first frame's presentation time, the edit list applied: its
	// composition time less the entry's media time.
	if e := got[0]; len(e) >= 32 { // edts and elst headers, version, count, segment duration
		mediaTime := int64(int32(binary.BigEndian.Uint32(e[28:])))
		if spans[0][0] == 0 || spans[0][0]-mediaTime != 0 {
			t.Errorf("first frame at %d, media time %d: not presented at 0", spans[0][0], mediaTime)
		}
	}
	tk, _ := trackBoxes(t, out, "tkhd")
	tkRef, _ := trackBoxes(t, ref, "tkhd")
	if !bytes.Equal(tk[0], tkRef[0]) {
		t.Errorf("video tkhd %x, ffmpeg's %x", tk[0], tkRef[0])
	}
	if _, err := ReadVideo(bytes.NewReader(out), int64(len(out))); err != nil {
		t.Errorf("ReadVideo: %v", err)
	}
	// Again: nothing more to do.
	if p, err := PlanRewriteMovie(bytes.NewReader(out), int64(len(out)), RewriteOptions{StartAtFirstFrame: true}); err != nil || !p.InPlace || len(p.Patches) != 0 {
		t.Errorf("again: %+v, %v", p, err)
	}

	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	dir := t.TempDir()
	in, res := filepath.Join(dir, "in.mp4"), filepath.Join(dir, "out.mp4")
	if err := os.WriteFile(in, src, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(res, out, 0o644); err != nil {
		t.Fatal(err)
	}
	starts := func(p string) string {
		b, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "stream=codec_type,start_time", "-of", "csv=p=0", p).Output()
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(b))
	}
	if s := starts(res); s != "video,0.000000\naudio,0.000000" {
		t.Errorf("ffprobe start times %q", s)
	}
	if s := starts(in); s == starts(res) {
		t.Errorf("the source already starts at 0: %q", s)
	}
	// The decoded frames, their hashes alone: the same frames, now
	// presented two frames earlier.
	hashes := func(p, stream string) string {
		b, err := exec.Command("ffmpeg", "-v", "error", "-i", p, "-map", "0:"+stream, "-f", "framemd5", "-").Output()
		if err != nil {
			t.Fatal(err)
		}
		var h []string
		for _, l := range strings.Split(string(b), "\n") {
			if f := strings.Split(l, ","); !strings.HasPrefix(l, "#") && len(f) == 6 {
				h = append(h, strings.TrimSpace(f[5]))
			}
		}
		return strings.Join(h, " ")
	}
	for _, s := range []string{"v", "a"} {
		if a, b := hashes(in, s), hashes(res, s); a == "" || a != b {
			t.Errorf("stream %s: decoded frames differ", s)
		}
	}
}

// What the option leaves as it is: a movie with an edit list, a movie
// without composition offsets, and (the option off) every movie.
func TestStartAtFirstFrameUnchanged(t *testing.T) {
	for _, p := range []string{"testdata/video/bframes.mp4", "testdata/video/xmp.mp4", "testdata/video/keys.mov"} {
		src := readFile(t, p)
		with, without := rewritten(t, src, RewriteOptions{StartAtFirstFrame: true}), rewritten(t, src, RewriteOptions{})
		if !bytes.Equal(with, without) {
			t.Errorf("%s: the option changed the rewrite", p)
		}
	}
	src := readFile(t, "testdata/video/bframes-noedts.mp4")
	if e, _ := trackBoxes(t, rewritten(t, src, RewriteOptions{}), "edts"); e[0] != nil || e[1] != nil {
		t.Error("an edit list without the option")
	}
}

func TestCompositionSpan(t *testing.T) {
	table := func(runs ...uint32) []byte {
		b := binary.BigEndian.AppendUint32(make([]byte, 4), uint32(len(runs)/2))
		for _, r := range runs {
			b = binary.BigEndian.AppendUint32(b, r)
		}
		return b
	}
	for _, c := range []struct {
		name        string
		stts, ctts  []byte
		first, end  int64
		ok, wantErr bool
	}{
		// Decoding times 0, 100, 200, 300; offsets 200, 300, 100, 100:
		// composition times 200, 400, 300, 400, the last ending at 500.
		{"reordered", table(4, 100), table(1, 200, 1, 300, 2, 100), 200, 500, true, false},
		{"one run", table(3, 10), table(3, 20), 20, 50, true, false},
		{"negative offset", table(2, 10), table(1, 0xFFFFFFF6, 1, 0), -10, 20, true, false},
		{"different counts", table(3, 10), table(2, 20), 0, 0, false, false},
		{"no samples", table(), table(), 0, 0, false, false},
		{"table past its box", table(2, 10)[:12], table(2, 10), 0, 0, false, true},
	} {
		first, end, ok, err := compositionSpan(c.stts, c.ctts)
		if (err != nil) != c.wantErr || ok != c.ok || ok && (first != c.first || end != c.end) {
			t.Errorf("%s: %d %d %v %v", c.name, first, end, ok, err)
		}
	}
}

func BenchmarkRewriteMovieFirstFrame(b *testing.B) {
	src := readFile(b, "testdata/video/bframes-noedts.mp4")
	cr := &countingReader{r: bytes.NewReader(src)}
	b.ReportAllocs()
	for range b.N {
		if _, err := PlanRewriteMovie(cr, int64(len(src)), RewriteOptions{StartAtFirstFrame: true}); err != nil {
			b.Fatal(err)
		}
	}
	reportReads(b, cr)
}
