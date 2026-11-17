package mediameta

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/0x20h/go-media-metadata/internal/bmff"
)

// moovToEnd moves a movie's moov box to the end of the file, with its
// chunk offsets, as a writer that doesn't do fast start leaves it.
func moovToEnd(t *testing.T, file []byte) []byte {
	t.Helper()
	top, err := bmff.ReadBoxes(bytes.NewReader(file), 0, int64(len(file)))
	if err != nil {
		t.Fatal(err)
	}
	mi := -1
	for i, b := range top {
		if b.Type == bmff.T("moov") {
			mi = i
		}
	}
	moovB := top[mi]
	moov, err := parseQTNode(file[moovB.Offset:moovB.End()], 0)
	if err != nil {
		t.Fatal(err)
	}
	newAt := map[int]int64{}
	var out []byte
	for i, b := range top {
		if i != mi {
			newAt[i] = int64(len(out))
			out = append(out, file[b.Offset:b.End()]...)
		}
	}
	tables, err := chunkTables(moov)
	if err != nil {
		t.Fatal(err)
	}
	for _, tb := range tables {
		tb.mapped = make([]int64, len(tb.offsets))
		for j, v := range tb.offsets {
			nv, ok := mapOffset(top, newAt, v)
			if !ok {
				t.Fatalf("offset %d", v)
			}
			tb.mapped[j] = nv
		}
		tb.write()
	}
	return append(out, moov.encode()...)
}

// The real clips: H.264 and HEVC with AAC, MP4 and MOV, tagged by
// ExifTool (Keys, ©xyz, an item list title, XMP, loci, a rotation), with
// moov first and last. The rewrite keeps every stream frame for frame,
// and what ffprobe reads of them; it has moov first; stripped, ExifTool
// finds none of the tags; it agrees with ffmpeg's fast start.
func TestRewriteMovieClips(t *testing.T) {
	tool, err := exec.LookPath("exiftool")
	if err != nil {
		t.Skip("exiftool not installed")
	}
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	probe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe not installed")
	}
	dir := t.TempDir()
	run := func(name string, args ...string) []byte {
		t.Helper()
		out, err := exec.Command(name, args...).Output()
		if err != nil {
			var stderr []byte
			if ee, ok := err.(*exec.ExitError); ok {
				stderr = ee.Stderr
			}
			t.Fatalf("%s %v: %v %s", filepath.Base(name), args, err, stderr)
		}
		return out
	}
	frames := func(p string) string {
		return string(run(ff, "-v", "error", "-i", p, "-map", "0", "-c", "copy", "-f", "framemd5", "-"))
	}
	streams := func(p string) string {
		return string(run(probe, "-v", "error", "-show_entries",
			"stream=index,codec_name,profile,codec_tag_string,width,height,pix_fmt,color_range,color_space,color_transfer,color_primaries,sample_rate,channels,r_frame_rate,time_base,duration_ts,nb_frames:stream_side_data:format=duration",
			"-of", "json", p))
	}
	// tags are ExifTool's tags of these groups.
	tags := func(p string) map[string]any {
		var got []map[string]any
		if err := json.Unmarshal(run(tool, "-j", "-G1", "-a", "-q", p), &got); err != nil || len(got) != 1 {
			t.Fatalf("exiftool %s: %v", p, err)
		}
		out := map[string]any{}
		for k, v := range got[0] {
			g, _, _ := strings.Cut(k, ":")
			if g == "Keys" || g == "ItemList" || g == "UserData" || strings.HasPrefix(g, "XMP") || g == "Composite" && strings.HasSuffix(k, ":Rotation") {
				out[k] = v
			}
		}
		return out
	}
	for _, c := range []struct{ name, args string }{
		{"h264.mp4", "-c:v libx264 -c:a aac"},
		{"hevc.mov", "-c:v libx265 -tag:v hvc1 -x265-params log-level=none -c:a aac"},
		{"h264.mov", "-c:v libx264 -c:a aac"},
		{"hevc.mp4", "-c:v libx265 -tag:v hvc1 -x265-params log-level=none -c:a aac"},
	} {
		base := filepath.Join(dir, c.name)
		run(ff, append(append([]string{"-v", "error", "-y", "-f", "lavfi", "-i", "testsrc=size=64x48:rate=10", "-f", "lavfi", "-i", "sine=frequency=440",
			"-t", "1", "-color_primaries", "bt709", "-color_trc", "bt709", "-colorspace", "bt709"}, strings.Fields(c.args)...), base)...)
		run(tool, "-q", "-q", "-overwrite_original", "-Rotation=90", "-Keys:Make=Maker", "-Keys:Model=Cam", "-ItemList:Title=Item title",
			"-XMP-dc:Title=XMP title", "-UserData:GPSCoordinates=48.1, 11.5, 520", "-Keys:GPSCoordinates=48.1, 11.5, 520",
			"-Keys:Location=+48.1000+011.5000/", base)
		first, err := os.ReadFile(base)
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range []struct {
			name string
			file []byte
		}{{"moov-first", first}, {"moov-last", moovToEnd(t, first)}} {
			name := c.name + "/" + v.name
			src := filepath.Join(dir, strings.ReplaceAll(name, "/", "-"))
			if err := os.WriteFile(src, v.file, 0o644); err != nil {
				t.Fatal(err)
			}
			wantFrames, wantStreams, wantTags := frames(src), streams(src), tags(src)
			if len(wantTags) < 6 {
				t.Fatalf("%s: the source has only %v", name, wantTags)
			}
			faststart := src + ".ffmpeg" + filepath.Ext(c.name)
			run(ff, "-v", "error", "-y", "-i", src, "-map", "0", "-c", "copy", "-map_metadata", "-1", "-movflags", "+faststart", faststart)
			for _, opts := range []RewriteOptions{{}, {StripMetadata: true}, {StripMetadata: true, ZeroTimes: true}} {
				out := src + ".out" + filepath.Ext(c.name)
				if err := os.WriteFile(out, rewritten(t, v.file, opts), 0o644); err != nil {
					t.Fatal(err)
				}
				if got := frames(out); got != wantFrames {
					t.Errorf("%s %+v: frames differ", name, opts)
				}
				if got := streams(out); got != wantStreams {
					t.Errorf("%s %+v: streams\n%s\nwant\n%s", name, opts, got, wantStreams)
				}
				if e := run(ff, "-v", "error", "-i", out, "-f", "null", "-"); len(e) != 0 {
					t.Errorf("%s %+v: decoding: %s", name, opts, e)
				}
				file, _ := os.ReadFile(out)
				if ok, err := IndexFirst(bytes.NewReader(file), int64(len(file))); !ok || err != nil {
					t.Errorf("%s %+v: index first %v %v", name, opts, ok, err)
				}
				got := tags(out)
				if !opts.StripMetadata {
					if !equalJSON(got, wantTags) {
						t.Errorf("%s %+v: tags %v, want %v", name, opts, got, wantTags)
					}
					continue
				}
				if r := got["Composite:Rotation"]; len(got) != 1 || r != wantTags["Composite:Rotation"] {
					t.Errorf("%s %+v: tags left %v (rotation %v)", name, opts, sortedKeys(got), wantTags["Composite:Rotation"])
				}
				// ffmpeg's fast start: the same frames and streams.
				if frames(faststart) != wantFrames || streams(faststart) != wantStreams {
					t.Errorf("%s: ffmpeg's fast start differs from the source", name)
				}
			}
		}
	}
}

func equalJSON(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

func sortedKeys(m map[string]any) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
