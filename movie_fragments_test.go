package mediameta

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0x20h/go-media-metadata/internal/bmff"
)

// fragmentTargets checks that every absolute offset a fragmented movie
// holds lands where it should: each tfhd base-data-offset and tfra moof
// offset on a moof, each sidx's first reference on a moof or sidx.
func fragmentTargets(t *testing.T, name string, file []byte) (moofs, bases, tfras, sidxs int) {
	t.Helper()
	top, err := bmff.ParseBoxes(file, 0, int64(len(file)))
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	typeAt := func(at int64) string {
		for _, b := range top {
			if b.Offset == at {
				return string(b.Type[:])
			}
		}
		return ""
	}
	for _, b := range top {
		switch b.Type {
		case bmff.T("moof"):
			moofs++
			kids, _ := bmff.ParseBoxes(file, b.Offset+b.HeaderLen, b.End())
			for _, traf := range kids {
				tk, _ := bmff.ParseBoxes(file, traf.Body(), traf.End())
				for _, k := range tk {
					if k.Type == bmff.T("tfhd") && file[k.Body()+3]&1 != 0 {
						bases++
						if v := int64(binary.BigEndian.Uint64(file[k.Body()+8:])); typeAt(v) != "moof" {
							t.Errorf("%s: a base offset %d on %q", name, v, typeAt(v))
						}
					}
				}
			}
		case bmff.T("mfra"):
			kids, _ := bmff.ParseBoxes(file, b.Offset+b.HeaderLen, b.End())
			for _, k := range kids {
				if k.Type != bmff.T("tfra") {
					continue
				}
				p := file[k.Body():k.End()]
				w := 4
				if p[0] == 1 {
					w = 8
				}
				lens := binary.BigEndian.Uint32(p[8:])
				entry := 2*w + int((lens>>4)&3+1+(lens>>2)&3+1+lens&3+1)
				for j := 0; j < int(binary.BigEndian.Uint32(p[12:])); j++ {
					off := 16 + j*entry + w
					v := int64(binary.BigEndian.Uint32(p[off:]))
					if w == 8 {
						v = int64(binary.BigEndian.Uint64(p[off:]))
					}
					tfras++
					if typeAt(v) != "moof" {
						t.Errorf("%s: a tfra moof offset %d on %q", name, v, typeAt(v))
					}
				}
			}
		case bmff.T("sidx"):
			sidxs++
			p := file[b.Offset+b.HeaderLen : b.End()]
			first := int64(binary.BigEndian.Uint32(p[16:]))
			if p[0] == 1 {
				first = int64(binary.BigEndian.Uint64(p[20:]))
			}
			if at := typeAt(b.End() + first); at != "moof" && at != "sidx" {
				t.Errorf("%s: a sidx first reference on %q", name, at)
			}
		}
	}
	return moofs, bases, tfras, sidxs
}

// Fragmented clips as ffmpeg writes them: each rewritten keeps its frames
// and streams as ffprobe reads them, its fragments' offsets land on their
// boxes, and stripped, ExifTool finds no tags.
func TestRewriteFragmentedClips(t *testing.T) {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	probe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe not installed")
	}
	tool, _ := exec.LookPath("exiftool")
	dir := t.TempDir()
	run := func(name string, args ...string) string {
		t.Helper()
		out, err := exec.Command(name, args...).Output()
		if err != nil {
			t.Fatalf("%s %v: %v", filepath.Base(name), args, err)
		}
		return string(out)
	}
	for _, c := range []struct {
		name, flags string
		bases, sidx bool
	}{
		{"empty-moov", "frag_keyframe+empty_moov", true, false},
		{"base-moof", "frag_keyframe+empty_moov+default_base_moof", false, false},
		{"samples-in-moov", "frag_keyframe", true, false},
		{"sidx", "frag_keyframe+empty_moov+default_base_moof+global_sidx", false, true},
		{"no-mfra", "frag_keyframe+empty_moov+skip_trailer", true, false},
	} {
		src := filepath.Join(dir, c.name+".mp4")
		run(ff, "-v", "error", "-y", "-f", "lavfi", "-i", "testsrc=duration=2:size=64x48:rate=10", "-f", "lavfi", "-i", "sine=duration=2",
			"-c:v", "libx264", "-preset", "veryfast", "-g", "5", "-c:a", "aac", "-b:a", "16k", "-shortest",
			"-metadata", "title=Lake", "-metadata", "comment=Walk", "-movflags", c.flags, src)
		file, err := os.ReadFile(src)
		if err != nil {
			t.Fatal(err)
		}
		moofs, bases, _, sidxs := fragmentTargets(t, c.name, file)
		if moofs < 2 || (bases > 0) != c.bases || (sidxs > 0) != c.sidx {
			t.Fatalf("%s: the source has %d moofs, %d base offsets, %d sidx", c.name, moofs, bases, sidxs)
		}
		frames := run(ff, "-v", "quiet", "-i", src, "-map", "0", "-c", "copy", "-f", "framemd5", "-")
		streams := run(probe, "-v", "quiet", "-show_entries", "stream=index,codec_name,width,height,sample_rate,nb_frames,duration_ts", "-of", "json", src)
		for _, opts := range []RewriteOptions{{}, {StripMetadata: true, ZeroTimes: true}} {
			out := rewritten(t, file, opts)
			p := filepath.Join(dir, c.name+".out.mp4")
			if err := os.WriteFile(p, out, 0o644); err != nil {
				t.Fatal(err)
			}
			if got := run(ff, "-v", "quiet", "-i", p, "-map", "0", "-c", "copy", "-f", "framemd5", "-"); got != frames {
				t.Errorf("%s %+v: frames differ", c.name, opts)
			}
			if got := run(probe, "-v", "quiet", "-show_entries", "stream=index,codec_name,width,height,sample_rate,nb_frames,duration_ts", "-of", "json", p); got != streams {
				t.Errorf("%s %+v: streams\n%s\nwant\n%s", c.name, opts, got, streams)
			}
			m, b, tf, s := fragmentTargets(t, c.name, out)
			m0, b0, tf0, s0 := fragmentTargets(t, c.name, file)
			if m != m0 || b != b0 || tf != tf0 || s != s0 {
				t.Errorf("%s %+v: fragments %d %d %d %d, were %d %d %d %d", c.name, opts, m, b, tf, s, m0, b0, tf0, s0)
			}
			if opts.StripMetadata {
				if len(out) >= len(file) {
					t.Errorf("%s: stripped, %d bytes, were %d", c.name, len(out), len(file))
				}
				if tool != "" {
					if got := run(tool, "-s3", "-Title", "-Comment", p); strings.TrimSpace(got) != "" {
						t.Errorf("%s: tags left: %q", c.name, got)
					}
				}
			}
			again, err := PlanRewriteMovie(bytes.NewReader(out), int64(len(out)), opts)
			if err != nil || !again.InPlace || len(again.Patches) != 0 {
				t.Errorf("%s %+v: again %+v, %v", c.name, opts, again, err)
			}
		}
	}
}

// Fragments or an index before the movie box are refused, as is a
// segment index over boxes the rewrite would move apart.
func TestRewriteFragmentedRefused(t *testing.T) {
	file := readFile(t, "testdata/video/frag.mp4")
	top, err := bmff.ParseBoxes(file, 0, int64(len(file)))
	if err != nil || top[1].Type != bmff.T("moov") || top[2].Type != bmff.T("moof") {
		t.Fatalf("frag.mp4: %v %v", err, top)
	}
	// moof and its mdat first, then moov.
	moov, frag := top[1], file[top[2].Offset:top[3].End()]
	swapped := append(append(append(append([]byte(nil), file[:moov.Offset]...), frag...), file[moov.Offset:moov.End()]...), file[top[3].End():]...)
	if _, err := PlanRewriteMovie(bytes.NewReader(swapped), int64(len(swapped)), RewriteOptions{}); !errors.Is(err, ErrUnsupported) {
		t.Errorf("a fragment before moov: %v, want ErrUnsupported", err)
	}
	// A segment index before moov.
	sidx := make([]byte, 44)
	binary.BigEndian.PutUint32(sidx, 44)
	copy(sidx[4:], "sidx")
	binary.BigEndian.PutUint16(sidx[8+22:], 1)
	withSidx := append(append(append([]byte(nil), file[:moov.Offset]...), sidx...), file[moov.Offset:]...)
	if _, err := PlanRewriteMovie(bytes.NewReader(withSidx), int64(len(withSidx)), RewriteOptions{}); !errors.Is(err, ErrUnsupported) {
		t.Errorf("a sidx before moov: %v, want ErrUnsupported", err)
	}
}

// frag.mp4 (testdata/video/make.sh), stripped: moov shrinks, every
// fragment moves, and the offsets follow.
func TestRewriteFragmentedFixture(t *testing.T) {
	file := readFile(t, "testdata/video/frag.mp4")
	out := rewritten(t, file, RewriteOptions{StripMetadata: true})
	if len(out) >= len(file) {
		t.Fatalf("stripped: %d bytes, were %d", len(out), len(file))
	}
	m, b, tf, _ := fragmentTargets(t, "frag.mp4", out)
	if m < 2 || b < 2 || tf < 1 {
		t.Errorf("%d moofs, %d base offsets, %d tfra entries", m, b, tf)
	}
	before, err := ReadVideo(bytes.NewReader(file), int64(len(file)))
	if err != nil {
		t.Fatal(err)
	}
	after, err := ReadVideo(bytes.NewReader(out), int64(len(out)))
	if err != nil || after.Width != before.Width || after.Duration != before.Duration || after.Title != "" {
		t.Errorf("read back %+v (%v), before %+v", after, err, before)
	}
}

// A free box between segment indexes and what they index: the rewrite
// leaves it out, so each sidx's first offset shrinks by its size.
func TestRewriteSidxFirstOffset(t *testing.T) {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	src := filepath.Join(t.TempDir(), "sidx.mp4")
	if out, err := exec.Command(ff, "-v", "error", "-y", "-f", "lavfi", "-i", "testsrc=duration=2:size=64x48:rate=10",
		"-c:v", "libx264", "-preset", "veryfast", "-g", "5",
		// No absolute offsets (default_base_moof, no mfra): the sidx is
		// what the free box shifts.
		"-movflags", "frag_keyframe+empty_moov+default_base_moof+global_sidx+skip_trailer", src).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v %s", err, out)
	}
	file := readFile(t, src)
	top, err := bmff.ParseBoxes(file, 0, int64(len(file)))
	if err != nil {
		t.Fatal(err)
	}
	last := -1
	for i, b := range top {
		if b.Type == bmff.T("sidx") {
			last = i
		}
	}
	if last < 0 {
		t.Fatal("no sidx")
	}
	// Insert a free box after the last sidx; every sidx's span starts 8
	// bytes later.
	at := top[last].End()
	free := bmff.AppendHeader(nil, bmff.T("free"), 0, false)
	padded := append(append(append([]byte(nil), file[:at]...), free...), file[at:]...)
	for _, b := range top[:last+1] {
		if b.Type != bmff.T("sidx") {
			continue
		}
		p := padded[b.Offset+b.HeaderLen:]
		if p[0] == 1 {
			binary.BigEndian.PutUint64(p[20:], binary.BigEndian.Uint64(p[20:])+8)
		} else {
			binary.BigEndian.PutUint32(p[16:], binary.BigEndian.Uint32(p[16:])+8)
		}
	}
	fragmentTargets(t, "padded", padded)
	out := rewritten(t, padded, RewriteOptions{})
	if !bytes.Equal(out, file) {
		t.Errorf("rewritten: %d bytes, want the file without its free box (%d)", len(out), len(file))
	}
	fragmentTargets(t, "rewritten", out)
}
