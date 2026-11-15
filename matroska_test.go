package mediameta

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// testdata/matroska (see its make.sh): title-date.mkv, H.264 with a
// title, a recording time and a 16:9 display size; vp9.webm with a title;
// streamed.webm, VP8 written to a pipe, so its Segment's size is unknown.
func mkvFile(t testing.TB, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "matroska", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestReadMatroskaVideo(t *testing.T) {
	for _, c := range []struct {
		file, title, created string
	}{
		{"title-date.mkv", "Harbour at dusk", "2025-05-04T11:14:15.25Z"},
		{"vp9.webm", "Boats", ""},
		{"streamed.webm", "", ""},
	} {
		b := mkvFile(t, c.file)
		r, n := bytes.NewReader(b), int64(len(b))
		v, err := ReadVideo(r, n)
		if err != nil {
			t.Fatalf("%s: %v", c.file, err)
		}
		if v.Title != c.title || v.Width != 70 || v.Height != 50 || v.Orientation != 1 || v.Duration != time.Second {
			t.Errorf("%s: %q %d×%d, orientation %d, %v", c.file, v.Title, v.Width, v.Height, v.Orientation, v.Duration)
		}
		if c.created != "" {
			want, _ := time.Parse(time.RFC3339Nano, c.created)
			if !v.Created.Equal(want) || !v.OffsetKnown {
				t.Errorf("%s: created %v, want %v", c.file, v.Created, want)
			}
		} else if !v.Created.IsZero() {
			t.Errorf("%s: created %v", c.file, v.Created)
		}
		if s, err := ReadImageSize(r, n); err != nil || s.Width != 70 || s.Height != 50 {
			t.Errorf("%s: ReadImageSize %+v, %v", c.file, s, err)
		}
		if o, err := ReadOrientation(r, n); err != nil || o != 1 {
			t.Errorf("%s: ReadOrientation %v, %v", c.file, o, err)
		}
		for name, err := range map[string]error{
			"PlanOrientation":   second(PlanOrientation(r, n, 6)),
			"PlanRotation":      second(PlanRotation(r, n, 90)),
			"PlanVideoMetadata": second(PlanVideoMetadata(r, n, VideoEdit{RemoveGPS: true})),
			"HashImageData":     second(HashImageData(r, n, io.Discard)),
		} {
			if !errors.Is(err, ErrUnsupported) {
				t.Errorf("%s: %s: %v, want ErrUnsupported", c.file, name, err)
			}
		}
	}
}

// ebml builds an element: its ID's bytes, an 8-byte size, the body.
func ebml(id uint32, body ...[]byte) []byte {
	var idb []byte
	for s := 24; s >= 0; s -= 8 {
		if c := byte(id >> s); c != 0 || len(idb) > 0 {
			idb = append(idb, c)
		}
	}
	data := bytes.Join(body, nil)
	size := make([]byte, 8)
	binary.BigEndian.PutUint64(size, uint64(len(data)))
	size[0] = 0x01
	return append(append(idb, size...), data...)
}

func ebmlU(id uint32, v uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, v)
	return ebml(id, b)
}

func ebmlF(id uint32, v float64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, math.Float64bits(v))
	return ebml(id, b)
}

// mkv is a Matroska file of these Segment children.
func mkv(children ...[]byte) []byte {
	head := ebml(ebmlHeader, ebml(ebmlDocType, []byte("matroska")))
	return append(head, ebml(mkvSegment, children...)...)
}

func mkvVideoTrack(w, h uint64, video ...[]byte) []byte {
	return ebml(mkvTracks, ebml(mkvTrackEntry, ebmlU(mkvTrackType, 1),
		ebml(mkvVideo, append([][]byte{ebmlU(mkvPixelWidth, w), ebmlU(mkvPixelHeight, h)}, video...)...)))
}

func cluster() []byte { return ebml(mkvCluster, ebmlU(0xE7, 0)) }

func TestMatroskaStructures(t *testing.T) {
	info := ebml(mkvInfo, ebmlU(mkvScale, 1_000_000), ebmlF(mkvDuration, 2500))
	movieTitle := ebml(mkvTags, ebml(mkvTag, ebml(mkvTargets, ebmlU(mkvTargetType, 50)),
		ebml(mkvSimpleTag, ebml(mkvTagName, []byte("TITLE")), ebml(mkvTagString, []byte("From the tags")))))
	trackTitle := ebml(mkvTags, ebml(mkvTag, ebml(mkvTargets, ebmlU(mkvTagTrackUID, 7)),
		ebml(mkvSimpleTag, ebml(mkvTagName, []byte("TITLE")), ebml(mkvTagString, []byte("A track")))))
	read := func(t *testing.T, b []byte) Video {
		t.Helper()
		v, err := ReadVideo(bytes.NewReader(b), int64(len(b)))
		if err != nil {
			t.Fatal(err)
		}
		return v
	}

	t.Run("tags after the clusters, through the seek head", func(t *testing.T) {
		// The seek head's positions count from the Segment's body; the
		// elements here are laid out after a 40-byte seek head.
		build := func(pos uint64) []byte {
			seek := ebml(mkvSeekHead, ebml(mkvSeek, ebml(mkvSeekID, []byte{0x12, 0x54, 0xC3, 0x67}), ebmlU(mkvSeekPos, pos)))
			return mkv(seek, info, mkvVideoTrack(64, 48), cluster(), movieTitle)
		}
		seekLen := len(ebml(mkvSeekHead, ebml(mkvSeek, ebml(mkvSeekID, []byte{0x12, 0x54, 0xC3, 0x67}), ebmlU(mkvSeekPos, 0))))
		pos := seekLen + len(info) + len(mkvVideoTrack(64, 48)) + len(cluster())
		v := read(t, build(uint64(pos)))
		if v.Title != "From the tags" || v.Duration != 2500*time.Millisecond || v.Width != 64 {
			t.Errorf("%+v", v)
		}
		// A stale seek entry is skipped.
		if v := read(t, build(uint64(pos-1))); v.Title != "" {
			t.Errorf("stale seek: title %q", v.Title)
		}
	})
	t.Run("Info's title wins; a track's TITLE isn't the movie's", func(t *testing.T) {
		withTitle := ebml(mkvInfo, ebml(mkvTitle, []byte("From Info\x00\x00")))
		if v := read(t, mkv(withTitle, movieTitle)); v.Title != "From Info" {
			t.Errorf("title %q", v.Title)
		}
		if v := read(t, mkv(info, trackTitle)); v.Title != "" {
			t.Errorf("track title taken: %q", v.Title)
		}
	})
	t.Run("rotation from the projection's roll", func(t *testing.T) {
		for roll, want := range map[float64]Orientation{0: 1, 90: 8, -90: 6, 180: 3, 270: 6, 45: 0} {
			v := read(t, mkv(info, mkvVideoTrack(64, 48, ebml(mkvProjection, ebmlF(mkvPoseRoll, roll)))))
			if v.Orientation != want {
				t.Errorf("roll %v: orientation %d, want %d", roll, v.Orientation, want)
			}
		}
		// A 360° picture isn't one of the eight.
		v := read(t, mkv(info, mkvVideoTrack(64, 48, ebml(mkvProjection, ebmlU(mkvProjType, 1)))))
		if v.Orientation != 0 {
			t.Errorf("equirectangular: orientation %d", v.Orientation)
		}
	})
	t.Run("an audio-only file has no size", func(t *testing.T) {
		audio := ebml(mkvTracks, ebml(mkvTrackEntry, ebmlU(mkvTrackType, 2)))
		b := mkv(info, audio)
		if v := read(t, b); v.Width != 0 || v.Orientation != 0 {
			t.Errorf("%+v", v)
		}
		if _, err := ReadImageSize(bytes.NewReader(b), int64(len(b))); !errors.Is(err, ErrUnsupported) {
			t.Errorf("ReadImageSize: %v, want ErrUnsupported", err)
		}
	})
	t.Run("a DateUTC before 2001", func(t *testing.T) {
		dayBefore := -int64(24 * time.Hour)
		v := read(t, mkv(ebml(mkvInfo, ebmlU(mkvDateUTC, uint64(dayBefore)))))
		if want := time.Date(2000, 12, 31, 0, 0, 0, 0, time.UTC); !v.Created.Equal(want) {
			t.Errorf("created %v", v.Created)
		}
	})
}

func TestMatroskaMalformed(t *testing.T) {
	b := mkvFile(t, "title-date.mkv")
	// Cut anywhere: ErrMalformed, or not Matroska before the header is
	// whole, or (cut in a Cluster) everything read. Never a panic.
	for n := 0; n < len(b); n++ {
		_, err := ReadVideo(bytes.NewReader(b[:n]), int64(n))
		if err != nil && !errors.Is(err, ErrMalformed) && !errors.Is(err, ErrUnknownFormat) {
			t.Fatalf("cut at %d: %v", n, err)
		}
	}
	for name, file := range map[string][]byte{
		"no Info":            mkv(mkvVideoTrack(64, 48)),
		"Info of 9-byte int": mkv(ebml(mkvInfo, ebml(mkvScale, make([]byte, 9)))),
		"a 3-byte float":     mkv(ebml(mkvInfo, ebml(mkvDuration, make([]byte, 3)))),
		"something else first": append(ebml(ebmlHeader, ebml(ebmlDocType, []byte("webm"))),
			ebml(mkvInfo)...),
	} {
		if _, err := ReadVideo(bytes.NewReader(file), int64(len(file))); !errors.Is(err, ErrMalformed) {
			t.Errorf("%s: %v, want ErrMalformed", name, err)
		}
	}
	// Another EBML document isn't Matroska.
	other := append(ebml(ebmlHeader, ebml(ebmlDocType, []byte("other"))), ebml(mkvSegment)...)
	if _, err := ReadVideo(bytes.NewReader(other), int64(len(other))); !errors.Is(err, ErrUnknownFormat) {
		t.Errorf("DocType other: %v, want ErrUnknownFormat", err)
	}
}

// ExifTool reads the duration, date, title and size as this package does,
// where it is installed.
func TestMatroskaAgreesWithExifTool(t *testing.T) {
	tool, err := exec.LookPath("exiftool")
	if err != nil {
		t.Skip("exiftool not installed")
	}
	out, err := exec.Command(tool, "-j", "-n", "-q", "-Duration", "-DateTimeOriginal", "-Title", "-ImageWidth", "-ImageHeight", "-ext", "mkv", "-ext", "webm",
		filepath.Join("testdata", "matroska")).Output()
	if err != nil {
		t.Fatalf("exiftool: %v", err)
	}
	var got []struct {
		SourceFile              string
		Duration                float64
		DateTimeOriginal, Title string
		ImageWidth, ImageHeight int
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("exiftool read %d files, want 3", len(got))
	}
	for _, e := range got {
		b, err := os.ReadFile(e.SourceFile)
		if err != nil {
			t.Fatal(err)
		}
		v, err := ReadVideo(bytes.NewReader(b), int64(len(b)))
		if err != nil {
			t.Fatalf("%s: %v", e.SourceFile, err)
		}
		var date string
		if !v.Created.IsZero() {
			// ExifTool: "2025:05:04 11:14:15.25Z".
			date = v.Created.Format("2006:01:02 15:04:05.999999999Z")
		}
		if v.Duration.Seconds() != e.Duration || date != e.DateTimeOriginal || v.Title != e.Title ||
			v.Width != e.ImageWidth || v.Height != e.ImageHeight {
			t.Errorf("%s: %v %q %q %d×%d; ExifTool %v %q %q %d×%d", e.SourceFile, v.Duration, date, v.Title, v.Width, v.Height,
				e.Duration, e.DateTimeOriginal, e.Title, e.ImageWidth, e.ImageHeight)
		}
	}
}

// FuzzReadMatroska walks mutated Matroska and WebM files: varint lengths,
// unknown sizes, seek heads pointing anywhere, nested tags. Every read
// ends without a panic or a hang.
func FuzzReadMatroska(f *testing.F) {
	for _, n := range []string{"title-date.mkv", "vp9.webm", "streamed.webm"} {
		f.Add(mkvFile(f, n))
	}
	info := ebml(mkvInfo, ebmlF(mkvDuration, 10))
	f.Add(mkv(ebml(mkvSeekHead, ebml(mkvSeek, ebml(mkvSeekID, []byte{0x15, 0x49, 0xA9, 0x66}), ebmlU(mkvSeekPos, 0))), cluster(), info))
	f.Fuzz(func(t *testing.T, data []byte) {
		withinDeadline(t, func() {
			r, n := bytes.NewReader(data), int64(len(data))
			if v, err := ReadVideo(r, n); err == nil && (v.Duration < 0 || v.Width < 0 || v.Height < 0) {
				t.Fatalf("%+v", v)
			}
			_, _ = ReadImageSize(r, n)
			_, _ = ReadOrientation(r, n)
		})
	})
}
