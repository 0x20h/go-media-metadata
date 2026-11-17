package mediameta

import (
	"bytes"
	"errors"
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
	// A fragment after the movie.
	frag := append(append([]byte(nil), src...), bmff.AppendHeader(nil, bmff.T("moof"), 0, false)...)
	if _, err := PlanRewriteMovie(bytes.NewReader(frag), int64(len(frag)), RewriteOptions{}); !errors.Is(err, ErrUnsupported) {
		t.Errorf("a fragmented movie: %v", err)
	}
}
