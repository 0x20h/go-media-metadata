package mediameta

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/0x20h/go-media-metadata/internal/bmff/testbuild"
)

// A plan either leaves the image data as HashImageData takes it, or is
// refused: never a write that changes it.
func checkGuarded(t *testing.T, name string, data []byte, plan func(src *bytes.Reader, n int64) (*Plan, error), wantRefused bool) {
	t.Helper()
	// The hash of no bytes is "": a chunk past the end has none yet.
	before, hashErr := imageDataMD5(t, data)
	if hashErr != nil {
		t.Fatalf("%s: no image data hash (%v)", name, hashErr)
	}
	p, err := plan(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		// Refused as unsupported, or, where the format's own checks find
		// it first, as malformed.
		if !errors.Is(err, ErrUnsupported) && !errors.Is(err, ErrMalformed) {
			t.Fatalf("%s: %v, want ErrUnsupported or ErrMalformed", name, err)
		}
		if !wantRefused {
			t.Fatalf("%s: refused: %v", name, err)
		}
		return
	}
	if wantRefused {
		t.Errorf("%s: planned, want it refused", name)
	}
	if after, err := imageDataMD5(t, written(t, p)); after != before {
		t.Fatalf("%s: image data hash %q became %q (%v)", name, before, after, err)
	}
}

// setFirstChunk points a movie's first stco entry at off.
func setFirstChunk(t *testing.T, file []byte, off uint32) []byte {
	t.Helper()
	out := bytes.Clone(file)
	i := bytes.Index(out, []byte("stco"))
	if i < 0 || i+16 > len(out) {
		t.Fatal("no stco")
	}
	binary.BigEndian.PutUint32(out[i+12:], off)
	return out
}

var guardVideoPlans = []struct {
	name string
	plan func(src *bytes.Reader, n int64) (*Plan, error)
}{
	{"date", func(src *bytes.Reader, n int64) (*Plan, error) {
		return PlanVideoMetadata(src, n, VideoEdit{Created: &editDate})
	}},
	{"long title", func(src *bytes.Reader, n int64) (*Plan, error) {
		return PlanVideoMetadata(src, n, VideoEdit{Title: ptr(string(bytes.Repeat([]byte("t"), 3000)))})
	}},
	{"rotation", func(src *bytes.Reader, n int64) (*Plan, error) { return PlanRotation(src, n, 90) }},
}

// A movie's chunk declared in its own moov box, or past the end of the
// file: a metadata write or a rotation would change the media data.
func TestImageDataGuardMovie(t *testing.T) {
	front := readFile(t, "testdata/video/keys.mov")
	end := moovToEnd(t, front)
	moovAt := func(file []byte) uint32 { return uint32(bytes.Index(file, []byte("moov")) - 4) }
	for _, c := range []struct {
		name    string
		file    []byte
		refused []bool // per guardVideoPlans
	}{
		{"moov first", front, []bool{false, false, false}},
		{"moov last", end, []bool{false, false, false}},
		{"chunk in moov, moov first", setFirstChunk(t, front, moovAt(front)+8), []bool{true, true, true}},
		{"chunk in moov, moov last", setFirstChunk(t, end, moovAt(end)+8), []bool{true, true, true}},
		// Past the end: the date and the rotation keep the size; the long
		// title grows the file, which would fill the chunk.
		{"chunk past the end, moov last", setFirstChunk(t, end, uint32(len(end))+16), []bool{false, true, false}},
	} {
		for i, p := range guardVideoPlans {
			checkGuarded(t, c.name+": "+p.name, c.file, p.plan, c.refused[i])
		}
	}
}

// A HEIF image item's extent of length 0 runs to the end of the file, and
// one past the end has bytes only once the file grows: an appended item
// would land in either.
func TestImageDataGuardHEIF(t *testing.T) {
	file, _ := testbuild.Build(testbuild.Default())
	f, err := parseHEIF(bytes.NewReader(file), int64(len(file)))
	if err != nil {
		t.Fatal(err)
	}
	// The last image item's extent, in iloc: offset then length, 4 bytes
	// each.
	var off, n uint64
	for _, it := range f.m.Loc.Items {
		if isHEIFImageType(f, it.ID) && len(it.Extents) == 1 && it.Extents[0].Offset > off {
			off, n = it.Extents[0].Offset, it.Extents[0].Length
		}
	}
	pair := binary.BigEndian.AppendUint32(binary.BigEndian.AppendUint32(nil, uint32(off)), uint32(n))
	at := bytes.Index(file, pair)
	if at < 0 || bytes.Count(file, pair) != 1 {
		t.Fatal("the extent isn't in iloc once")
	}
	extent := func(o, l uint32) []byte {
		out := bytes.Clone(file)
		binary.BigEndian.PutUint32(out[at:], o)
		binary.BigEndian.PutUint32(out[at+4:], l)
		return out
	}
	xmp := xmpPacket("Harbour at dusk", 3000)
	plans := []struct {
		name string
		plan func(src *bytes.Reader, n int64) (*Plan, error)
	}{
		{"xmp", func(src *bytes.Reader, n int64) (*Plan, error) { return PlanXMP(src, n, xmp) }},
		{"exif", func(src *bytes.Reader, n int64) (*Plan, error) {
			return PlanExif(src, n, photoTIFF(binary.LittleEndian))
		}},
	}
	for _, c := range []struct {
		name    string
		file    []byte
		refused bool
	}{
		{"as built", file, false},
		{"extent to the end", extent(uint32(off), 0), true},
		{"extent past the end", extent(uint32(len(file))+64, 16), true},
	} {
		for _, p := range plans {
			checkGuarded(t, c.name+": "+p.name, c.file, p.plan, c.refused)
		}
	}
}

// A TIFF whose strip runs past the end of the file: a new IFD0 would
// land in it (the CI input that found the orientation case).
func TestImageDataGuardTIFFPastEnd(t *testing.T) {
	for _, name := range []string{"e39d26566199d188", "44cbb801abe38402"} {
		data := fuzzSeed(t, "FuzzOrientationAnyFormat", name)
		for _, o := range []Orientation{1, 6} {
			checkGuarded(t, name, data, func(src *bytes.Reader, n int64) (*Plan, error) { return PlanOrientation(src, n, o) }, true)
		}
	}
}

// fuzzSeed is a corpus file's bytes: after the "go test fuzz v1" line, a
// []byte literal.
func fuzzSeed(t *testing.T, target, name string) []byte {
	t.Helper()
	file := readFile(t, "testdata/fuzz/"+target+"/"+name)
	lines := strings.SplitN(string(file), "\n", 2)
	if len(lines) != 2 {
		t.Fatalf("%s: not a corpus file", name)
	}
	lit := strings.TrimSuffix(strings.TrimSpace(lines[1]), ")")
	data, err := strconv.Unquote(strings.TrimPrefix(lit, "[]byte("))
	if err != nil {
		t.Fatal(err)
	}
	return []byte(data)
}

// fuzzImageHash is a fuzz input's image data hash, for checking the writes
// planned from it.
type fuzzImageHash struct {
	sum string
	ok  bool
}

func fuzzImageHashOf(t *testing.T, data []byte) fuzzImageHash {
	sum, err := imageDataMD5(t, data)
	return fuzzImageHash{sum, err == nil}
}

// check fails t where out's image data isn't the input's. An input whose
// image data doesn't hash isn't checked.
func (h fuzzImageHash) check(t *testing.T, name string, out []byte) {
	t.Helper()
	if !h.ok {
		return
	}
	if after, err := imageDataMD5(t, out); after != h.sum {
		t.Fatalf("%s: image data hash %q became %q (%v)", name, h.sum, after, err)
	}
}

// The guard's movie walk finds the chunks the image data hash reads: the
// same bytes, joined where they touch.
func TestMovieImageRangesMatchHash(t *testing.T) {
	paths, _ := filepath.Glob("testdata/video/*.mp4")
	more, _ := filepath.Glob("testdata/video/*.mov")
	for _, p := range append(paths, more...) {
		file := readFile(t, p)
		n := int64(len(file))
		var hashed [][2]int64
		h := &rangeWriter{r: bytes.NewReader(file), size: n, w: io.Discard, record: &hashed}
		if err := h.movie(); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		got := mergeRanges(movieImageRanges(bytes.NewReader(file), n))
		// A fragmented movie's samples are in its fragments, not in moov's
		// tables: neither finds any.
		fragmented := strings.HasSuffix(p, "/frag.mp4")
		if want := mergeRanges(hashed); !slices.Equal(got, want) || len(want) == 0 && !fragmented {
			t.Errorf("%s: guard %v, hash %v", p, got, want)
		}
	}
}
