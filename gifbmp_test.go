package mediameta

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

// testdata/gif-bmp: plain.gif, a two-frame 32×24 animation written by
// image/gif; comment-xmp.gif, the same with a comment and XMP (dc:title,
// dc:subject) added by ExifTool; 24×16 BMPs with the 40-byte info header
// bottom-up and top-down (a negative height), and with the 12-byte core
// header.
func gifBMP(t testing.TB, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "gif-bmp", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestGIFBMPImageSize(t *testing.T) {
	for name, want := range map[string][2]int{
		"plain.gif": {32, 24}, "comment-xmp.gif": {32, 24},
		"bottom-up.bmp": {24, 16}, "top-down.bmp": {24, 16}, "core.bmp": {24, 16},
	} {
		b := gifBMP(t, name)
		s, err := ReadImageSize(bytes.NewReader(b), int64(len(b)))
		if err != nil || s.Width != want[0] || s.Height != want[1] {
			t.Errorf("%s: %d×%d, %v; want %d×%d", name, s.Width, s.Height, err, want[0], want[1])
		}
	}
}

func TestGIFCommentAndXMP(t *testing.T) {
	b := gifBMP(t, "comment-xmp.gif")
	r := bytes.NewReader(b)
	if c, err := ReadComment(r, int64(len(b))); err != nil || c != "A harbour at dusk" {
		t.Errorf("comment %q, %v", c, err)
	}
	packet, err := ReadXMP(r, int64(len(b)))
	if err != nil || packet == nil {
		t.Fatalf("XMP %d bytes, %v", len(packet), err)
	}
	x, err := ParseXMP(packet)
	if err != nil || x.Title != "Harbour" || !slices.Equal(x.Keywords, []string{"boats", "sea"}) {
		t.Errorf("XMP %+v, %v", x, err)
	}

	plain := gifBMP(t, "plain.gif")
	if c, err := ReadComment(bytes.NewReader(plain), int64(len(plain))); err != nil || c != "" {
		t.Errorf("plain.gif comment %q, %v", c, err)
	}
	if p, err := ReadXMP(bytes.NewReader(plain), int64(len(plain))); err != nil || p != nil {
		t.Errorf("plain.gif XMP %q, %v", p, err)
	}
	// A BMP has no comment of its own.
	bmp := gifBMP(t, "core.bmp")
	if _, err := ReadComment(bytes.NewReader(bmp), int64(len(bmp))); !errors.Is(err, ErrUnknownFormat) {
		t.Errorf("BMP comment: %v, want ErrUnknownFormat", err)
	}
}

// withBlocks puts extension blocks before a GIF's trailer.
func withBlocks(gif []byte, blocks ...[]byte) []byte {
	out := slices.Clone(gif[:len(gif)-1])
	for _, b := range blocks {
		out = append(out, b...)
	}
	return append(out, 0x3B)
}

// comment is a comment extension holding text, in sub-blocks of at most
// 255 bytes.
func comment(text string) []byte {
	b := []byte{0x21, 0xFE}
	for len(text) > 0 {
		n := min(len(text), 255)
		b = append(b, byte(n))
		b = append(b, text[:n]...)
		text = text[n:]
	}
	return append(b, 0)
}

func TestGIFComments(t *testing.T) {
	plain := gifBMP(t, "plain.gif")
	long := string(bytes.Repeat([]byte("x"), 600))
	b := withBlocks(plain, comment("first"), []byte{0x21, 0xF9, 4, 0, 0, 0, 0, 0}, comment(long))
	if c, err := ReadComment(bytes.NewReader(b), int64(len(b))); err != nil || c != "first\n"+long {
		t.Errorf("comments %q…, %v", c[:min(len(c), 20)], err)
	}
	// Another application extension (a loop count) is skipped.
	netscape := []byte{0x21, 0xFF, 11, 'N', 'E', 'T', 'S', 'C', 'A', 'P', 'E', '2', '.', '0', 3, 1, 0, 0, 0}
	b = withBlocks(plain, netscape, comment("after"))
	if c, err := ReadComment(bytes.NewReader(b), int64(len(b))); err != nil || c != "after" {
		t.Errorf("comment after NETSCAPE2.0: %q, %v", c, err)
	}
}

// Cut short anywhere, or with a broken block, a GIF is ErrMalformed (or,
// cut before its screen descriptor, not a GIF); never a panic.
func TestGIFMalformed(t *testing.T) {
	b := gifBMP(t, "comment-xmp.gif")
	for n := 0; n < len(b); n++ {
		cut := b[:n]
		_, err := ReadComment(bytes.NewReader(cut), int64(n))
		if n < 13 {
			if !errors.Is(err, ErrUnknownFormat) {
				t.Fatalf("cut at %d: %v, want ErrUnknownFormat", n, err)
			}
			continue
		}
		if !errors.Is(err, ErrMalformed) {
			t.Fatalf("cut at %d: %v, want ErrMalformed", n, err)
		}
	}
	plain := gifBMP(t, "plain.gif")
	for name, bad := range map[string][]byte{
		"an unknown block":         withBlocks(plain, []byte{0x99}),
		"XMP without its trailer":  withBlocks(plain, append([]byte{0x21, 0xFF, 11}, []byte(xmpAppID+"<x/>")...)),
		"a trailer without a zero": withBlocks(plain, append(append([]byte{0x21, 0xFF, 11}, []byte(xmpAppID+"<x/>")...), append(slices.Clone(xmpMagicTrailer), 7)...)),
	} {
		if _, err := ReadXMP(bytes.NewReader(bad), int64(len(bad))); !errors.Is(err, ErrMalformed) {
			t.Errorf("%s: %v, want ErrMalformed", name, err)
		}
	}
}

func TestBMPMalformed(t *testing.T) {
	b := gifBMP(t, "bottom-up.bmp")
	zero := slices.Clone(b)
	binary.LittleEndian.PutUint32(zero[18:], 0)
	if _, err := ReadImageSize(bytes.NewReader(zero), int64(len(zero))); !errors.Is(err, ErrMalformed) {
		t.Errorf("width 0: %v, want ErrMalformed", err)
	}
	odd := slices.Clone(b)
	binary.LittleEndian.PutUint32(odd[14:], 41)
	if _, err := ReadImageSize(bytes.NewReader(odd), int64(len(odd))); !errors.Is(err, ErrUnknownFormat) {
		t.Errorf("a 41-byte header: %v, want ErrUnknownFormat", err)
	}
	if _, err := ReadImageSize(bytes.NewReader(b[:20]), 20); !errors.Is(err, ErrUnknownFormat) {
		t.Errorf("a header cut short: %v, want ErrUnknownFormat", err)
	}
}

// ExifTool reads the test files' size, comment and XMP as this package
// does. Runs where exiftool is installed.
func TestGIFBMPAgreeWithExifTool(t *testing.T) {
	tool, err := exec.LookPath("exiftool")
	if err != nil {
		t.Skip("exiftool not installed")
	}
	out, err := exec.Command(tool, "-j", "-n", "-q", "-ImageWidth", "-ImageHeight", "-Comment", "-XMP:Title", "-XMP:Subject",
		filepath.Join("testdata", "gif-bmp")).Output()
	if err != nil {
		t.Fatalf("exiftool: %v", err)
	}
	var got []struct {
		SourceFile              string
		ImageWidth, ImageHeight int
		Comment, Title          string
		Subject                 any
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 {
		t.Fatalf("exiftool read %d files, want 5", len(got))
	}
	for _, e := range got {
		b, err := os.ReadFile(e.SourceFile)
		if err != nil {
			t.Fatal(err)
		}
		r, n := bytes.NewReader(b), int64(len(b))
		s, err := ReadImageSize(r, n)
		if err != nil || s.Width != e.ImageWidth || s.Height != e.ImageHeight {
			t.Errorf("%s: %d×%d (%v), ExifTool %d×%d", e.SourceFile, s.Width, s.Height, err, e.ImageWidth, e.ImageHeight)
		}
		if filepath.Ext(e.SourceFile) != ".gif" {
			continue
		}
		if c, _ := ReadComment(r, n); c != e.Comment {
			t.Errorf("%s: comment %q, ExifTool %q", e.SourceFile, c, e.Comment)
		}
		var x XMP
		if p, _ := ReadXMP(r, n); p != nil {
			x, _ = ParseXMP(p)
		}
		var subject []string
		if l, ok := e.Subject.([]any); ok {
			for _, s := range l {
				subject = append(subject, s.(string))
			}
		}
		if x.Title != e.Title || !slices.Equal(x.Keywords, subject) {
			t.Errorf("%s: XMP %q %q, ExifTool %q %q", e.SourceFile, x.Title, x.Keywords, e.Title, subject)
		}
	}
}

// FuzzReadGIF walks mutated GIFs: the header, sub-block chains, the XMP
// trailer, cut files. Every read ends without a panic or a hang, and a
// size, when there is one, is positive.
func FuzzReadGIF(f *testing.F) {
	plain := gifBMP(f, "plain.gif")
	f.Add(plain)
	f.Add(gifBMP(f, "comment-xmp.gif"))
	f.Add(withBlocks(plain, comment("a"), []byte{0x21, 0xFF, 11, 'N', 'E', 'T', 'S', 'C', 'A', 'P', 'E', '2', '.', '0', 3, 1, 0, 0, 0}))
	f.Fuzz(func(t *testing.T, data []byte) {
		withinDeadline(t, func() {
			r, n := bytes.NewReader(data), int64(len(data))
			if s, err := ReadImageSize(r, n); err == nil && (s.Width <= 0 || s.Height <= 0) {
				t.Fatalf("size %d×%d", s.Width, s.Height)
			}
			_, _ = ReadComment(r, n)
			_, _ = ReadXMP(r, n)
		})
	})
}

// FuzzReadBMP: any info header gives a positive size or an error.
func FuzzReadBMP(f *testing.F) {
	for _, n := range []string{"bottom-up.bmp", "top-down.bmp", "core.bmp"} {
		f.Add(gifBMP(f, n))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		withinDeadline(t, func() {
			if s, err := ReadImageSize(bytes.NewReader(data), int64(len(data))); err == nil && (s.Width <= 0 || s.Height <= 0) {
				t.Fatalf("size %d×%d", s.Width, s.Height)
			}
		})
	})
}
