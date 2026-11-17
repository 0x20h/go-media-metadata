package mediameta

import (
	"bytes"
	"errors"
	"github.com/0x20h/go-media-metadata/internal/bmff/qtbuild"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// readAllFiles are every corpus file of every format, and the RAW samples
// with MEDIAMETA_RAW_DIR set.
func readAllFiles(t testing.TB) []string {
	t.Helper()
	pats := []string{"testdata/exiftool/*.jpg", imageIOCorpus + "/*.jpg", imageIOCorpus + "/*.heic", "testdata/imageio/*.heic",
		"testdata/iphone/*.heic", "testdata/webp/*.webp", "testdata/video/*.mov", "testdata/video/*.mp4", "testdata/video/*.3gp",
		"testdata/m2ts/*.m2ts", "testdata/m2ts/*.ts", "testdata/gif-bmp/*.gif", "testdata/gif-bmp/*.bmp",
		"testdata/matroska/*.mkv", "testdata/matroska/*.webm", "testdata/avi/*.avi", "testdata/mpegps/*.mpg"}
	if dir := os.Getenv("MEDIAMETA_RAW_DIR"); dir != "" {
		for _, ext := range []string{"dng", "cr2", "cr3", "nef", "arw", "orf", "rw2", "raf"} {
			pats = append(pats, filepath.Join(dir, "*."+ext))
		}
	}
	var paths []string
	for _, pat := range pats {
		p, _ := filepath.Glob(pat)
		paths = append(paths, p...)
	}
	if len(paths) < 40 {
		t.Fatalf("%d corpus files", len(paths))
	}
	return paths
}

// ReadAll gives what the separate functions give, on every corpus file:
// they are checked against ExifTool, so it is too. (The summary's own
// choices, which part wins, are TestReadAllSummary's.)
func TestReadAllEqualsSeparateCalls(t *testing.T) {
	for _, path := range readAllFiles(t) {
		file := readFile(t, path)
		r, n := bytes.NewReader(file), int64(len(file))
		m, err := ReadAll(r, n)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if err := m.Errors.Err(); err != nil {
			t.Errorf("%s: %v", path, err)
		}
		if m.Video != nil {
			v, err := ReadVideo(r, n)
			if err != nil || !reflect.DeepEqual(*m.Video, v) {
				t.Errorf("%s: video %+v, ReadVideo %+v (%v)", path, *m.Video, v, err)
			}
			if o, _ := ReadOrientation(r, n); m.Orientation != o {
				t.Errorf("%s: orientation %d, ReadOrientation %d", path, m.Orientation, o)
			}
			continue
		}
		if x, err := ReadExif(r, n); err == nil && !reflect.DeepEqual(m.Exif, x) {
			t.Errorf("%s: Exif %+v, ReadExif %+v", path, m.Exif, x)
		}
		var xmp XMP
		if packet, err := ReadXMP(r, n); err == nil && packet != nil {
			xmp, _ = ParseXMP(packet)
		}
		if !reflect.DeepEqual(m.XMP, xmp) {
			t.Errorf("%s: XMP %+v, ReadXMP %+v", path, m.XMP, xmp)
		}
		var iptc IPTC
		if isJPEG(r, n) {
			if iim, err := ReadIPTC(r, n); err == nil && iim != nil {
				iptc, _ = ParseIPTC(iim)
			}
		}
		if !reflect.DeepEqual(m.IPTC, iptc) {
			t.Errorf("%s: IPTC %+v, ReadIPTC %+v", path, m.IPTC, iptc)
		}
		if s, err := ReadImageSize(r, n); err == nil && m.Size != s {
			t.Errorf("%s: size %+v, ReadImageSize %+v", path, m.Size, s)
		}
		if o, err := ReadOrientation(r, n); err == nil && m.Orientation != o {
			t.Errorf("%s: orientation %d, ReadOrientation %d", path, m.Orientation, o)
		}
		if got, want := m.Taken, TakenFrom(m.Exif, m.XMP, m.IPTC); !sameXMPDate(&got.Date, &want.Date) || got.Source != want.Source {
			t.Errorf("%s: taken %+v, TakenFrom %+v", path, got, want)
		}
	}
}

// The summary takes each field from the first part that has it: XMP,
// then IPTC, then Exif (and a GIF's comment last for the description).
func TestReadAllSummary(t *testing.T) {
	m := Metadata{
		Exif: Exif{XPTitle: "xp title", ImageDescription: "exif description", XPKeywords: "a; b;;c",
			GPS: &GPS{Latitude: 1, Longitude: 2}},
		IPTC: IPTC{Title: "iptc title", Keywords: []string{"iptc"}},
		XMP:  XMP{Description: "xmp description", GPS: &GPS{Latitude: 3, Longitude: 4}},
	}
	m.summarize()
	if m.Title != "iptc title" || m.Description != "xmp description" || !reflect.DeepEqual(m.Keywords, []string{"iptc"}) ||
		m.GPS.Latitude != 1 {
		t.Errorf("got %q, %q, %v, %+v", m.Title, m.Description, m.Keywords, *m.GPS)
	}
	m = Metadata{Exif: Exif{XPTitle: "xp title", XPComment: "xp comment", XPKeywords: "a; b;;c"}, XMP: XMP{GPS: &GPS{Latitude: 3}}}
	m.summarize()
	if m.Title != "xp title" || m.Description != "xp comment" || !reflect.DeepEqual(m.Keywords, []string{"a", "b", "c"}) ||
		m.GPS == nil || m.GPS.Latitude != 3 {
		t.Errorf("got %q, %q, %v, %+v", m.Title, m.Description, m.Keywords, m.GPS)
	}
	// A GIF's comment, read before the summary, is the last resort.
	m = Metadata{Description: "gif comment"}
	m.summarize()
	if m.Description != "gif comment" {
		t.Errorf("description %q", m.Description)
	}
}

// A part that can't be read has its error, and the others are read: a JPEG
// whose XMP is broken keeps its Exif, IPTC and size.
func TestReadAllBrokenPart(t *testing.T) {
	file := readFile(t, "testdata/exiftool/all.jpg")
	packet, err := ReadXMP(bytes.NewReader(file), int64(len(file)))
	if err != nil || packet == nil {
		t.Fatal(err)
	}
	// Unbalance the packet's XML in place: same length, so the file's
	// layout stays.
	at := bytes.Index(file, []byte("</rdf:RDF>"))
	if at < 0 {
		t.Fatal("no rdf:RDF end")
	}
	broken := bytes.Clone(file)
	copy(broken[at:], "</rdf:XXX>")
	m, err := ReadAll(bytes.NewReader(broken), int64(len(broken)))
	if err != nil {
		t.Fatal(err)
	}
	if m.Errors.XMP == nil || !errors.Is(m.Errors.Err(), ErrMalformed) {
		t.Errorf("XMP error %v, want ErrMalformed", m.Errors.XMP)
	}
	if m.Exif.DateTimeOriginal.IsZero() || m.IPTC.Title == "" || m.Size.Width == 0 || m.Taken.Source != SourceExifDateTimeOriginal {
		t.Errorf("the other parts lost: %+v", m)
	}
	if _, err := ReadAll(strings.NewReader("plain text, not media"), 21); !errors.Is(err, ErrUnknownFormat) {
		t.Errorf("text: %v, want ErrUnknownFormat", err)
	}
}

// ReadAll against the separate calls an importer would make, on the
// benchmark files: the same answers from fewer reads and allocations.
func BenchmarkReadAll(b *testing.B) {
	for _, f := range benchFiles(b) {
		b.Run(f.name+"/ReadAll", func(b *testing.B) {
			cr := &countingReader{r: bytes.NewReader(f.data)}
			b.ReportAllocs()
			for range b.N {
				if _, err := ReadAll(cr, int64(len(f.data))); err != nil {
					b.Fatal(err)
				}
			}
			reportReads(b, cr)
		})
		b.Run(f.name+"/separate", func(b *testing.B) {
			cr := &countingReader{r: bytes.NewReader(f.data)}
			n := int64(len(f.data))
			b.ReportAllocs()
			for range b.N {
				x, _ := ReadExif(cr, n)
				var xmp XMP
				if packet, _ := ReadXMP(cr, n); packet != nil {
					xmp, _ = ParseXMP(packet)
				}
				_, _ = ReadImageSize(cr, n)
				_, _ = ReadOrientation(cr, n)
				_ = TakenFrom(x, xmp, IPTC{})
			}
			reportReads(b, cr)
		})
	}
}

// ReadAll on any input: no panic, its call error and every part's a
// sentinel, and the parts what the separate functions give.
func FuzzReadAll(f *testing.F) {
	for _, p := range readAllFiles(f) {
		if b, err := os.ReadFile(p); err == nil && len(b) <= 64<<10 {
			f.Add(b)
		}
	}
	seeds(f)
	jpegSeeds(f)
	sentinel := func(err error) bool {
		return err == nil || errors.Is(err, ErrUnknownFormat) || errors.Is(err, ErrMalformed) || errors.Is(err, ErrUnsupported)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		withinDeadline(t, func() {
			r, n := bytes.NewReader(data), int64(len(data))
			m, err := ReadAll(r, n)
			if !sentinel(err) {
				t.Fatalf("ReadAll: %v", err)
			}
			if err != nil {
				return
			}
			e := m.Errors
			for _, pe := range []error{e.Exif, e.XMP, e.IPTC, e.Size, e.Orientation, e.Video} {
				if !sentinel(pe) {
					t.Fatalf("a part's error that is no sentinel: %v", pe)
				}
			}
			if m.Size.Width < 0 || m.Size.Height < 0 || m.Orientation != 0 && !m.Orientation.Valid() {
				t.Fatalf("size %+v, orientation %d", m.Size, m.Orientation)
			}
			if g := m.GPS; g != nil && (g.Latitude < -90 || g.Latitude > 90 || g.Longitude < -180 || g.Longitude > 180) {
				t.Fatalf("GPS %+v", *g)
			}
			if m.Video == nil && e.Exif == nil {
				if x, err := ReadExif(r, n); err == nil && !reflect.DeepEqual(m.Exif, x) {
					t.Fatalf("Exif %+v, ReadExif %+v", m.Exif, x)
				}
			}
			if e.Orientation == nil && m.Video == nil {
				if o, err := ReadOrientation(r, n); err == nil && o != m.Orientation {
					t.Fatalf("orientation %d, ReadOrientation %d", m.Orientation, o)
				}
			}
		})
	})
}

// ReadAll of movies: the I/O and allocations ReadAll spends on a movie's
// boxes, moov first and last, and a corpus movie with Keys.
func BenchmarkReadAllVideo(b *testing.B) {
	files := map[string][]byte{"keys.mov": readFile(b, "testdata/video/keys.mov")}
	for _, first := range []bool{true, false} {
		o := qtbuild.Default()
		o.MoovFirst, o.MediaSize = first, 1<<20
		f, _, _ := qtbuild.Build(o)
		files[map[bool]string{true: "qtbuild-moov-first", false: "qtbuild-moov-last"}[first]] = f
	}
	for _, name := range []string{"keys.mov", "qtbuild-moov-first", "qtbuild-moov-last"} {
		data := files[name]
		b.Run(name, func(b *testing.B) {
			cr := &countingReader{r: bytes.NewReader(data)}
			b.ReportAllocs()
			for range b.N {
				if _, err := ReadAll(cr, int64(len(data))); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(cr.bytes)/float64(b.N), "read-B/op")
			b.ReportMetric(float64(cr.calls)/float64(b.N), "readcalls/op")
		})
	}
}
