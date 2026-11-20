package mediameta

import (
	"bytes"
	"crypto/sha256"
	"strings"
	"testing"

	"github.com/0x20h/go-media-metadata/internal/bmff"
	"github.com/0x20h/go-media-metadata/internal/bmff/qtbuild"
)

// assembleMovie lays out a built movie's boxes in the order of the tokens
// and points its chunk offsets at their data where it lands:
//
//	ftyp moov free skip   the box (free, skip: 32 bytes)
//	mdat mdat64           all the media data, a 32- or 64-bit header
//	mdatA mdatB           its first and second half, as two mdats
//	mdat0                 all of it, size 0 ("to the end"; last)
//	xmp uuid              an XMP uuid box, another uuid box
func assembleMovie(t *testing.T, co64 bool, tokens ...string) []byte {
	t.Helper()
	o := qtbuild.Default()
	o.MediaSize, o.Co64 = 4096, co64
	src, ms, me := qtbuild.Build(o)
	media := src[ms:me]
	top, err := bmff.ReadBoxes(bytes.NewReader(src), 0, int64(len(src)))
	if err != nil {
		t.Fatal(err)
	}
	ftyp, _ := bmff.Find(top, bmff.T("ftyp"))
	moovB, _ := bmff.Find(top, bmff.T("moov"))
	moov, err := parseQTNode(src[moovB.Offset:moovB.End()], 0)
	if err != nil {
		t.Fatal(err)
	}
	tables, err := chunkTables(moov)
	if err != nil {
		t.Fatal(err)
	}
	half := int64(len(media) / 2)
	// Where each part of the media data starts in the file, by layout.
	var mediaAt [2]int64
	lay := func() []byte {
		var out []byte
		for _, tok := range tokens {
			switch tok {
			case "ftyp":
				out = append(out, src[ftyp.Offset:ftyp.End()]...)
			case "moov":
				out = append(out, moov.encode()...)
			case "free", "skip":
				out = append(bmff.AppendHeader(out, bmff.T(tok), 24, false), make([]byte, 24)...)
			case "mdat", "mdat64", "mdat0":
				switch tok {
				case "mdat":
					out = bmff.AppendHeader(out, bmff.T("mdat"), len(media), false)
				case "mdat64":
					out = bmff.AppendHeader(out, bmff.T("mdat"), len(media), true)
				default:
					out = append(out, 0, 0, 0, 0, 'm', 'd', 'a', 't')
				}
				mediaAt[0] = int64(len(out))
				mediaAt[1] = mediaAt[0] + half
				out = append(out, media...)
			case "mdatA":
				out = bmff.AppendHeader(out, bmff.T("mdat"), int(half), false)
				mediaAt[0] = int64(len(out))
				out = append(out, media[:half]...)
			case "mdatB":
				out = bmff.AppendHeader(out, bmff.T("mdat"), len(media)-int(half), false)
				mediaAt[1] = int64(len(out)) - half
				out = append(out, media[half:]...)
			case "xmp", "uuid":
				id := xmpUUID
				if tok == "uuid" {
					id = bytes.Repeat([]byte{0x42}, 16)
				}
				body := []byte(`<x:xmpmeta xmlns:x="adobe:ns:meta/"/>`)
				out = append(bmff.AppendHeader(out, bmff.T("uuid"), 16+len(body), false), id...)
				out = append(out, body...)
			default:
				t.Fatalf("token %q", tok)
			}
		}
		return out
	}
	// Twice: the first layout finds where the media data goes, the second
	// writes the offsets (same widths, so moov keeps its size).
	lay()
	for _, tb := range tables {
		tb.mapped = make([]int64, len(tb.offsets))
		for i, v := range tb.offsets {
			rel := v - ms
			part := 0
			if rel >= half {
				part = 1
			}
			tb.mapped[i] = mediaAt[part] + rel - int64(part)*half
		}
		tb.write()
	}
	return lay()
}

func TestRewriteMovieLayouts(t *testing.T) {
	for _, l := range []struct {
		tokens string
		co64   bool
	}{
		{"ftyp moov mdat", false},
		{"ftyp mdat moov", false},
		{"ftyp mdat moov", true},
		{"ftyp mdat64 moov", false},
		{"ftyp free mdat skip moov free", false},
		{"ftyp mdatA mdatB moov", false},
		{"ftyp mdatA moov mdatB", false},
		{"ftyp xmp mdat uuid moov", false},
		{"ftyp uuid mdat xmp moov", true},
		{"ftyp moov free mdat0", false},
		{"ftyp free moov mdat0", false},
		{"moov mdat", false},
	} {
		src := assembleMovie(t, l.co64, strings.Fields(l.tokens)...)
		want := movieHash(t, src)
		before, err := ReadVideo(bytes.NewReader(src), int64(len(src)))
		if err != nil {
			t.Fatalf("%s: %v", l.tokens, err)
		}
		for _, opts := range []RewriteOptions{{}, {StripMetadata: true}} {
			out := rewritten(t, src, opts)
			types := strings.Join(topTypes(t, out), " ")
			if ok, err := IndexFirst(bytes.NewReader(out), int64(len(out))); !ok || err != nil {
				t.Errorf("%s %+v: %s, IndexFirst %v %v", l.tokens, opts, types, ok, err)
			}
			if strings.Contains(types, "free") || strings.Contains(types, "skip") {
				t.Errorf("%s %+v: %s", l.tokens, opts, types)
			}
			if n := strings.Count(types, "uuid"); opts.StripMetadata && n != strings.Count(l.tokens, "uuid") ||
				!opts.StripMetadata && n != strings.Count(l.tokens, "uuid")+strings.Count(l.tokens, "xmp") {
				t.Errorf("%s %+v: %s", l.tokens, opts, types)
			}
			if got := movieHash(t, out); got != want {
				t.Errorf("%s %+v: media data hash changed (%s)", l.tokens, opts, types)
			}
			after, err := ReadVideo(bytes.NewReader(out), int64(len(out)))
			if err != nil || after.Duration != before.Duration || after.Orientation != before.Orientation {
				t.Errorf("%s %+v: %+v, %v", l.tokens, opts, after, err)
			}
			p, err := PlanRewriteMovie(bytes.NewReader(out), int64(len(out)), opts)
			if err != nil || !p.InPlace || len(p.Patches) != 0 {
				t.Errorf("%s %+v again: %+v, %v", l.tokens, opts, p, err)
			}
		}
	}
}

func FuzzRewriteMovie(f *testing.F) {
	movieSeeds(f)
	for _, l := range []string{"ftyp mdat moov", "ftyp mdatA moov mdatB", "ftyp xmp mdat uuid moov free", "ftyp free moov mdat0"} {
		f.Add(assembleMovie(&testing.T{}, false, strings.Fields(l)...))
	}
	f.Add(readFile(f, "testdata/video/bframes-noedts.mp4"))
	f.Add(readFile(f, "testdata/video/frag.mp4")) // fragmented
	f.Fuzz(func(t *testing.T, data []byte) {
		withinDeadline(t, func() {
			r := bytes.NewReader(data)
			h := sha256.New()
			_, hashErr := HashImageData(r, int64(len(data)), h)
			want := h.Sum(nil)
			_, readErr := ReadVideo(r, int64(len(data)))
			for _, opts := range []RewriteOptions{{}, {StripMetadata: true, ZeroTimes: true}, {StartAtFirstFrame: true}} {
				p, err := PlanRewriteMovie(r, int64(len(data)), opts)
				if err != nil {
					continue
				}
				checkPlan(t, data, p)
				var out bytes.Buffer
				if _, err := p.WriteTo(&out); err != nil {
					t.Fatal(err)
				}
				b := out.Bytes()
				if ok, err := IndexFirst(bytes.NewReader(b), int64(len(b))); !ok || err != nil {
					t.Fatalf("%+v: index first %v, %v", opts, ok, err)
				}
				if hashErr == nil {
					h.Reset()
					if _, err := HashImageData(bytes.NewReader(b), int64(len(b)), h); err != nil || !bytes.Equal(h.Sum(nil), want) {
						t.Fatalf("%+v: media data hash changed (%v)", opts, err)
					}
				}
				if readErr == nil {
					if _, err := ReadVideo(bytes.NewReader(b), int64(len(b))); err != nil {
						t.Fatalf("%+v: the rewritten movie doesn't read: %v", opts, err)
					}
				}
				again, err := PlanRewriteMovie(bytes.NewReader(b), int64(len(b)), opts)
				if err != nil || !again.InPlace || len(again.Patches) != 0 {
					t.Fatalf("%+v: rewriting again: %+v, %v", opts, again, err)
				}
			}
		})
	})
}
