package mediameta

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/0x20h/go-media-metadata/internal/bmff"
)

// refBox is a box of type typ holding parts.
func refBox(typ string, parts ...[]byte) []byte {
	body := bytes.Join(parts, nil)
	return append(binary.BigEndian.AppendUint32(nil, uint32(8+len(body))), append([]byte(typ), body...)...)
}

func be32(v uint32) []byte { return binary.BigEndian.AppendUint32(nil, v) }

// refTrack is a reference movie's track: its data references (true: this
// file, the self-contained flag; false: an alias to another file), its
// sample descriptions (each naming a data reference index), its stsc
// runs (first chunk, description) and its chunk offsets.
type refTrack struct {
	handler string
	drefs   []bool
	descs   []uint16
	runs    [][2]uint32
	chunks  []uint32
	noStsc  bool
}

func (tr refTrack) encode(id uint32) []byte {
	var entries []byte
	for _, self := range tr.drefs {
		if self {
			entries = append(entries, refBox("url ", be32(1))...)
		} else {
			// An alias record's bytes (made up): a file elsewhere.
			entries = append(entries, refBox("alis", be32(0), []byte("Macintosh HD:Media:clip.mov"))...)
		}
	}
	dref := refBox("dref", be32(0), be32(uint32(len(tr.drefs))), entries)
	var descs []byte
	for _, r := range tr.descs {
		d := append(make([]byte, 6), byte(r>>8), byte(r))
		descs = append(descs, refBox("mp4v", d, make([]byte, 70))...)
	}
	stsd := refBox("stsd", be32(0), be32(uint32(len(tr.descs))), descs)
	var runs []byte
	for _, r := range tr.runs {
		runs = append(runs, be32(r[0])...)
		runs = append(runs, be32(1)...)
		runs = append(runs, be32(r[1])...)
	}
	stsc := refBox("stsc", be32(0), be32(uint32(len(tr.runs))), runs)
	if tr.noStsc {
		stsc = nil
	}
	var offs []byte
	for _, c := range tr.chunks {
		offs = append(offs, be32(c)...)
	}
	stco := refBox("stco", be32(0), be32(uint32(len(tr.chunks))), offs)
	tkhd := refBox("tkhd", []byte{0, 0, 0, 7}, make([]byte, 8), be32(id), make([]byte, 60), be32(64<<16), be32(48<<16))
	mdhd := refBox("mdhd", be32(0), be32(0), be32(0), be32(600), be32(600), make([]byte, 4))
	hdlr := refBox("hdlr", be32(0), []byte("mhlr"), []byte(tr.handler), make([]byte, 13))
	minf := refBox("minf", refBox("dinf", dref), refBox("stbl", stsd, stsc, stco))
	return refBox("trak", tkhd, refBox("mdia", mdhd, hdlr, minf))
}

// refMovie is a QuickTime movie of the tracks, moov after the media data
// (when media is given) and a title in its user data. Internal chunk
// offsets are given relative to the media data's start (mdat's payload).
func refMovie(media []byte, tracks ...refTrack) []byte {
	ftyp := refBox("ftyp", []byte("qt  "), be32(0), []byte("qt  "))
	start := uint32(len(ftyp) + 8)
	var traks []byte
	for i, tr := range tracks {
		tr.chunks = append([]uint32(nil), tr.chunks...)
		for j := range tr.chunks {
			if tr.chunkInFile(j) {
				tr.chunks[j] += start
			}
		}
		traks = append(traks, tr.encode(uint32(i+1))...)
	}
	mvhd := refBox("mvhd", be32(0), be32(0), be32(0), be32(600), be32(600), make([]byte, 80))
	udta := refBox("udta", refBox("\xa9nam", []byte{0, 4, 0x55, 0xc4}, []byte("Clip")))
	moov := refBox("moov", mvhd, traks, udta)
	if media == nil {
		return append(ftyp, moov...)
	}
	return append(append(ftyp, refBox("mdat", media)...), moov...)
}

// chunkInFile is the fixture's own reading of chunk j's file.
func (tr refTrack) chunkInFile(j int) bool {
	desc := uint32(0)
	for _, r := range tr.runs {
		if r[0] <= uint32(j+1) {
			desc = r[1]
		}
	}
	if desc == 0 || int(desc) > len(tr.descs) {
		return true
	}
	if r := int(tr.descs[desc-1]); r < 1 || r > len(tr.drefs) {
		return true
	}
	return tr.drefs[tr.descs[desc-1]-1]
}

// stcoOf is each track's chunk offsets in file.
func stcoOf(t *testing.T, file []byte) [][]uint32 {
	t.Helper()
	top, err := bmff.ParseBoxes(file, 0, int64(len(file)))
	if err != nil {
		t.Fatal(err)
	}
	var out [][]uint32
	for _, b := range top {
		if b.Type != bmff.T("moov") {
			continue
		}
		moov, err := parseQTNode(file[b.Offset:b.End()], 0)
		if err != nil {
			t.Fatal(err)
		}
		tables, err := chunkTables(moov)
		if err != nil {
			t.Fatal(err)
		}
		for _, tb := range tables {
			var o []uint32
			for _, v := range tb.offsets {
				o = append(o, uint32(v))
			}
			out = append(out, o)
		}
	}
	return out
}

// A reference movie rewritten: chunks in this file still point at the
// same bytes, chunks in other files keep their offsets byte for byte,
// and the title reads as before.
func TestRewriteReferenceMovies(t *testing.T) {
	media := make([]byte, 600)
	for i := range media {
		media[i] = byte(i * 7)
	}
	external := refTrack{handler: "vide", drefs: []bool{false}, descs: []uint16{1}, runs: [][2]uint32{{1, 1}}, chunks: []uint32{1000, 90000}}
	internal := refTrack{handler: "soun", drefs: []bool{true}, descs: []uint16{1}, runs: [][2]uint32{{1, 1}}, chunks: []uint32{0, 300}}
	mixed := refTrack{handler: "vide", drefs: []bool{true, false}, descs: []uint16{1, 2},
		runs: [][2]uint32{{1, 1}, {2, 2}, {3, 1}}, chunks: []uint32{100, 777777, 400}}
	for _, c := range []struct {
		name   string
		media  []byte
		tracks []refTrack
	}{
		{"external and internal", media, []refTrack{external, internal}},
		{"mixed", media, []refTrack{mixed}},
		{"external only", nil, []refTrack{external}},
	} {
		src := refMovie(c.media, c.tracks...)
		before, err := ReadVideo(bytes.NewReader(src), int64(len(src)))
		if err != nil || before.Title != "Clip" {
			t.Fatalf("%s: the source reads %+v, %v", c.name, before, err)
		}
		for _, opts := range []RewriteOptions{{}, {StripMetadata: true, ZeroTimes: true}} {
			out := rewritten(t, src, opts)
			if ok, err := IndexFirst(bytes.NewReader(out), int64(len(out))); c.media != nil && (!ok || err != nil) {
				t.Errorf("%s %+v: index first %v %v", c.name, opts, ok, err)
			}
			got, want := stcoOf(t, out), stcoOf(t, src)
			for i, tr := range c.tracks {
				for j := range tr.chunks {
					if !tr.chunkInFile(j) {
						if got[i][j] != want[i][j] {
							t.Errorf("%s %+v: track %d chunk %d in another file moved: %d, was %d", c.name, opts, i, j, got[i][j], want[i][j])
						}
						continue
					}
					a, b := src[want[i][j]:want[i][j]+16], out[got[i][j]:got[i][j]+16]
					if !bytes.Equal(a, b) {
						t.Errorf("%s %+v: track %d chunk %d at %d points elsewhere", c.name, opts, i, j, got[i][j])
					}
				}
			}
			after, err := ReadVideo(bytes.NewReader(out), int64(len(out)))
			if err != nil || !opts.StripMetadata && after.Title != "Clip" || opts.StripMetadata && after.Title != "" {
				t.Errorf("%s %+v: reads %+v, %v", c.name, opts, after, err)
			}
			again, err := PlanRewriteMovie(bytes.NewReader(out), int64(len(out)), opts)
			if err != nil || !again.InPlace || len(again.Patches) != 0 {
				t.Errorf("%s %+v: again %+v, %v", c.name, opts, again, err)
			}
		}
	}
}

// A video edit that grows a moov before the media data shifts the chunks
// in this file only.
func TestVideoEditReferenceMovie(t *testing.T) {
	media := make([]byte, 600)
	mixed := refTrack{handler: "vide", drefs: []bool{true, false}, descs: []uint16{1, 2},
		runs: [][2]uint32{{1, 1}, {2, 2}}, chunks: []uint32{100, 5000}}
	// moov first: rewrite the fixture so.
	src := rewritten(t, refMovie(media, mixed), RewriteOptions{})
	want := stcoOf(t, src)
	title := "A much longer title, so that moov grows"
	var out bytes.Buffer
	if err := SetVideoMetadata(bytes.NewReader(src), int64(len(src)), &out, VideoEdit{Title: &title}); err != nil {
		t.Fatal(err)
	}
	got := stcoOf(t, out.Bytes())
	if got[0][1] != want[0][1] {
		t.Errorf("the chunk in another file moved: %d, was %d", got[0][1], want[0][1])
	}
	if got[0][0] == want[0][0] || !bytes.Equal(src[want[0][0]:want[0][0]+16], out.Bytes()[got[0][0]:got[0][0]+16]) {
		t.Errorf("the chunk in this file: %d, was %d", got[0][0], want[0][0])
	}
}

// What can't be told apart for certain is refused.
func TestRewriteReferenceMovieRefused(t *testing.T) {
	media := make([]byte, 600)
	for _, c := range []struct {
		name string
		tr   refTrack
	}{
		{"no stsc", refTrack{handler: "vide", drefs: []bool{false}, descs: []uint16{1}, runs: [][2]uint32{{1, 1}}, chunks: []uint32{9}, noStsc: true}},
		{"a data reference out of range", refTrack{handler: "vide", drefs: []bool{false}, descs: []uint16{2}, runs: [][2]uint32{{1, 1}}, chunks: []uint32{9}}},
		{"a sample description out of range", refTrack{handler: "vide", drefs: []bool{false}, descs: []uint16{1}, runs: [][2]uint32{{1, 3}}, chunks: []uint32{9}}},
		{"stsc not from chunk 1", refTrack{handler: "vide", drefs: []bool{false}, descs: []uint16{1}, runs: [][2]uint32{{2, 1}}, chunks: []uint32{9, 10}}},
		{"stsc out of order", refTrack{handler: "vide", drefs: []bool{true, false}, descs: []uint16{1, 2}, runs: [][2]uint32{{1, 1}, {1, 2}}, chunks: []uint32{9}}},
	} {
		src := refMovie(media, c.tr)
		if _, err := PlanRewriteMovie(bytes.NewReader(src), int64(len(src)), RewriteOptions{}); !errors.Is(err, ErrUnsupported) {
			t.Errorf("%s: %v, want ErrUnsupported", c.name, err)
		}
	}
}

// referenceMovieSeeds are reference movies for the fuzz targets.
func referenceMovieSeeds() [][]byte {
	media := make([]byte, 64)
	return [][]byte{
		refMovie(media, refTrack{handler: "vide", drefs: []bool{false}, descs: []uint16{1}, runs: [][2]uint32{{1, 1}}, chunks: []uint32{1000}},
			refTrack{handler: "soun", drefs: []bool{true}, descs: []uint16{1}, runs: [][2]uint32{{1, 1}}, chunks: []uint32{0, 32}}),
		refMovie(media, refTrack{handler: "vide", drefs: []bool{true, false}, descs: []uint16{1, 2},
			runs: [][2]uint32{{1, 1}, {2, 2}}, chunks: []uint32{8, 5000}}),
		refMovie(nil, refTrack{handler: "vide", drefs: []bool{false}, descs: []uint16{1}, runs: [][2]uint32{{1, 1}}, chunks: []uint32{1000}}),
	}
}
