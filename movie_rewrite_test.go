package mediameta

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/0x20h/go-media-metadata/internal/bmff"
	"github.com/0x20h/go-media-metadata/internal/bmff/qtbuild"
)

func topTypes(t *testing.T, file []byte) []string {
	t.Helper()
	top, err := bmff.ReadBoxes(bytes.NewReader(file), 0, int64(len(file)))
	if err != nil {
		t.Fatalf("top-level boxes: %v", err)
	}
	var out []string
	for _, b := range top {
		out = append(out, string(b.Type[:]))
	}
	return out
}

func rewritten(t *testing.T, src []byte, opts RewriteOptions) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := RewriteMovie(bytes.NewReader(src), int64(len(src)), &buf, opts); err != nil {
		t.Fatalf("RewriteMovie: %v", err)
	}
	return buf.Bytes()
}

func TestRewriteMovie(t *testing.T) {
	for _, first := range []bool{false, true} {
		for _, opts := range []RewriteOptions{{}, {StripMetadata: true}, {StripMetadata: true, ZeroTimes: true}} {
			o := qtbuild.Default()
			o.MoovFirst, o.FreeAfterMoov = first, 64
			src, _, _ := qtbuild.Build(o)
			before, err := ReadVideo(bytes.NewReader(src), int64(len(src)))
			if err != nil {
				t.Fatal(err)
			}
			out := rewritten(t, src, opts)
			types := topTypes(t, out)
			if len(types) < 3 || types[0] != "ftyp" || types[1] != "moov" {
				t.Errorf("moov first %v %+v: boxes %v", first, opts, types)
			}
			for _, ty := range types {
				if ty == "free" {
					t.Errorf("%+v: a free box is left", opts)
				}
			}
			if ok, err := IndexFirst(bytes.NewReader(out), int64(len(out))); !ok || err != nil {
				t.Errorf("IndexFirst %v, %v", ok, err)
			}
			if movieHash(t, out) != movieHash(t, src) {
				t.Errorf("moov first %v %+v: the media data changed", first, opts)
			}
			after, err := ReadVideo(bytes.NewReader(out), int64(len(out)))
			if err != nil {
				t.Fatal(err)
			}
			if after.Duration != before.Duration || after.Width != before.Width || after.Orientation != before.Orientation {
				t.Errorf("%+v: %+v, before %+v", opts, after, before)
			}
			switch {
			case opts.StripMetadata:
				if after.Make != "" || after.Model != "" || after.GPS != nil || after.ContentIdentifier != "" {
					t.Errorf("%+v: metadata left: %+v", opts, after)
				}
				if opts.ZeroTimes && !after.MovieCreated.IsZero() {
					t.Errorf("%+v: movie time %v", opts, after.MovieCreated)
				}
			default:
				if after.Make != before.Make || !after.Created.Equal(before.Created) || after.GPS == nil {
					t.Errorf("%+v: metadata changed: %+v, before %+v", opts, after, before)
				}
			}
			// Rewriting again changes nothing.
			p, err := PlanRewriteMovie(bytes.NewReader(out), int64(len(out)), opts)
			if err != nil || !p.InPlace || len(p.Patches) != 0 || p.NewSize != p.Size {
				t.Errorf("%+v again: %+v, %v", opts, p, err)
			}
		}
	}
	if ok, _ := IndexFirst(bytes.NewReader(movieLast(t)), int64(len(movieLast(t)))); ok {
		t.Error("IndexFirst of a movie with moov last")
	}
}

func movieLast(t *testing.T) []byte {
	o := qtbuild.Default()
	src, _, _ := qtbuild.Build(o)
	return src
}

func TestRewriteMovieRefusals(t *testing.T) {
	if _, err := PlanRewriteMovie(bytes.NewReader(plainJPEG(t)), int64(len(plainJPEG(t))), RewriteOptions{}); !errors.Is(err, ErrUnknownFormat) {
		t.Errorf("a JPEG: %v", err)
	}
	src := movieLast(t)
	// A fragment before the movie (fragments after it are rewritten:
	// see movie_fragments_test.go).
	top, err := bmff.ParseBoxes(src, 0, int64(len(src)))
	if err != nil || top[0].Type != bmff.T("ftyp") {
		t.Fatal(err)
	}
	at := top[0].End()
	frag := append(append(append([]byte(nil), src[:at]...), bmff.AppendHeader(nil, bmff.T("moof"), 0, false)...), src[at:]...)
	if _, err := PlanRewriteMovie(bytes.NewReader(frag), int64(len(frag)), RewriteOptions{}); !errors.Is(err, ErrUnsupported) {
		t.Errorf("a fragment before the movie: %v", err)
	}
}

// sparseFile is a file of a prefix, then n zero bytes, then a suffix,
// without the zeros in memory.
type sparseFile struct {
	prefix, suffix []byte
	zeros          int64
}

func (s *sparseFile) size() int64 { return int64(len(s.prefix)) + s.zeros + int64(len(s.suffix)) }

func (s *sparseFile) ReadAt(p []byte, off int64) (int, error) {
	n := 0
	for n < len(p) {
		at := off + int64(n)
		switch {
		case at < int64(len(s.prefix)):
			n += copy(p[n:], s.prefix[at:])
		case at < int64(len(s.prefix))+s.zeros:
			k := min(int64(len(p)-n), int64(len(s.prefix))+s.zeros-at)
			clear(p[n : int64(n)+k])
			n += int(k)
		case at < s.size():
			n += copy(p[n:], s.suffix[at-int64(len(s.prefix))-s.zeros:])
		default:
			return n, io.EOF
		}
	}
	return n, nil
}

// headWriter keeps the first bytes written and counts the rest.
type headWriter struct {
	head []byte
	keep int
	n    int64
}

func (w *headWriter) Write(p []byte) (int, error) {
	if room := w.keep - len(w.head); room > 0 {
		w.head = append(w.head, p[:min(room, len(p))]...)
	}
	w.n += int64(len(p))
	return len(p), nil
}

// A chunk offset that moving moov pushes past 32 bits: its stco becomes
// a co64 (which grows moov, so the offsets are mapped again), and the
// offset points where its chunk went. The 4 GB of media data aren't in
// memory.
func TestRewriteMoviePromotesToCo64(t *testing.T) {
	o := qtbuild.Default()
	o.Keys, o.Udta = nil, nil
	small, _, _ := qtbuild.Build(o) // moov last
	top, err := bmff.ReadBoxes(bytes.NewReader(small), 0, int64(len(small)))
	if err != nil {
		t.Fatal(err)
	}
	ftypB, _ := bmff.Find(top, bmff.T("ftyp"))
	moovB, _ := bmff.Find(top, bmff.T("moov"))
	moov, err := parseQTNode(small[moovB.Offset:moovB.End()], 0)
	if err != nil {
		t.Fatal(err)
	}
	const media = int64(1<<32) + 4096
	mdatAt := ftypB.End()
	chunk := int64(1<<32) - 64 // inside the media data, 32-bit before the move
	tables, err := chunkTables(moov)
	if err != nil || len(tables) == 0 {
		t.Fatalf("%d tables, %v", len(tables), err)
	}
	for _, tb := range tables {
		tb.mapped = make([]int64, len(tb.offsets))
		for i := range tb.mapped {
			tb.mapped[i] = chunk
		}
		tb.write()
	}
	prefix := append(append([]byte(nil), small[:ftypB.End()]...), bmff.AppendHeader(nil, bmff.T("mdat"), int(media), true)...)
	f := &sparseFile{prefix: prefix, zeros: media, suffix: moov.encode()}
	w := &headWriter{keep: 1 << 16}
	if err := RewriteMovie(f, f.size(), w, RewriteOptions{}); err != nil {
		t.Fatal(err)
	}
	head := w.head
	out, err := bmff.ReadBoxes(bytes.NewReader(head), 0, int64(len(head)))
	if err != nil && len(out) < 2 {
		// The mdat runs past the head kept: read the boxes before it.
		out, _ = bmff.ReadBoxesLenient(bytes.NewReader(head), 0, int64(len(head)))
	}
	if len(out) < 3 || out[1].Type != bmff.T("moov") || out[2].Type != bmff.T("mdat") {
		t.Fatalf("boxes %v", out)
	}
	newMoov, err := parseQTNode(head[out[1].Offset:out[1].End()], 0)
	if err != nil {
		t.Fatal(err)
	}
	got, err := chunkTables(newMoov)
	if err != nil || len(got) != len(tables) {
		t.Fatalf("%d tables, %v", len(got), err)
	}
	want := out[2].Offset + (chunk - mdatAt)
	for _, tb := range got {
		if !tb.wide || tb.node.typ != bmff.T("co64") {
			t.Errorf("table %v not promoted", tb.node.typ)
		}
		for _, v := range tb.offsets {
			if v != want {
				t.Errorf("offset %d, want %d", v, want)
			}
		}
	}
	if w.n != f.size()+int64(len(newMoov.encode()))-int64(len(f.suffix)) {
		t.Errorf("wrote %d bytes, the file has %d (moov %d → %d)", w.n, f.size(), len(f.suffix), len(newMoov.encode()))
	}
}

// ZeroTimes zeroes the creation and modification times of the movie
// header and of every track's and media's header (#216).
func TestZeroTimesEveryHeader(t *testing.T) {
	src := readFile(t, "testdata/video/keys-mvhd.mov")
	headerTimes := func(file []byte) (nonzero []string) {
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
			check := func(name string, n *qtNode) {
				if n == nil {
					t.Fatalf("no %s", name)
				}
				p := n.payload()
				end := 12
				if p[0] == 1 {
					end = 20
				}
				if !bytes.Equal(p[4:end], make([]byte, end-4)) {
					nonzero = append(nonzero, name)
				}
			}
			check("mvhd", moov.child(bmff.T("mvhd")))
			for _, k := range moov.kids {
				if k.typ == bmff.T("trak") {
					check("tkhd", k.child(bmff.T("tkhd")))
					check("mdhd", path(k, "mdia").child(bmff.T("mdhd")))
				}
			}
		}
		return nonzero
	}
	if got := headerTimes(src); len(got) == 0 {
		t.Fatal("the source's times are all 0 already")
	}
	if got := headerTimes(rewritten(t, src, RewriteOptions{ZeroTimes: true})); len(got) != 0 {
		t.Errorf("times left in %v", got)
	}
}
