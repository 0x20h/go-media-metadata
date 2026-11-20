package mediameta

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// canonBoxes are the sizes of a Canon movie's uuid box, its CNTH and
// CNTH's CNDA, and where CNTH's padding starts and ends.
func canonBoxes(t *testing.T, file []byte) (uuid, cnth, cnda int, padAt, padEnd int) {
	t.Helper()
	u := bytes.Index(file, append([]byte("uuid"), canonUUID...))
	c := bytes.Index(file, []byte("CNTH"))
	d := bytes.Index(file, []byte("CNDA"))
	if u < 4 || c < 4 || d < 4 {
		t.Fatal("no Canon uuid box")
	}
	size := func(at int) int { return int(binary.BigEndian.Uint32(file[at-4:])) }
	return size(u), size(c), size(d), d - 4 + size(d), c - 4 + size(c)
}

func setCanonDate(t *testing.T, file []byte, d time.Time) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := SetVideoMetadata(bytes.NewReader(file), int64(len(file)), &out, VideoEdit{Created: &d}); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// The thumbnail Exif's dates follow the edit: ExifTool reads the new
// DateTimeOriginal and CreateDate there, the Canon boxes keep their
// sizes (the Exif is edited in place), the frames are the same, and
// ReadVideo reads the date set.
func TestCanonDateFollowsEdit(t *testing.T) {
	src := readFile(t, "testdata/video/canon.mp4")
	d := time.Date(2021, 7, 8, 9, 10, 11, 0, time.FixedZone("", 7200))
	out := setCanonDate(t, src, d)
	u0, c0, d0, _, _ := canonBoxes(t, src)
	if u, c, n, _, _ := canonBoxes(t, out); u != u0 || c != c0 || n != d0 {
		t.Errorf("Canon boxes %d %d %d, were %d %d %d", u, c, n, u0, c0, d0)
	}
	v, err := ReadVideo(bytes.NewReader(out), int64(len(out)))
	if err != nil || !v.Created.Equal(d) {
		t.Errorf("read back %v, %v", v.Created, err)
	}
	thumb, _ := canonThumbExif(t, out)
	wall := time.Date(2021, 7, 8, 9, 10, 11, 0, time.UTC)
	if !thumb.DateTimeOriginal.Equal(wall) || !thumb.DateTimeDigitized.Equal(wall) || thumb.OffsetKnown {
		t.Errorf("thumbnail Exif %v %v (offset %v)", thumb.DateTimeOriginal, thumb.DateTimeDigitized, thumb.OffsetKnown)
	}
	if thumb.Camera.Make != "Canon" {
		t.Errorf("thumbnail make %q", thumb.Camera.Make)
	}

	dir := t.TempDir()
	p := filepath.Join(dir, "canon.mp4")
	if err := os.WriteFile(p, out, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.LookPath("ffmpeg"); err == nil {
		frames := func(f string) string {
			b, err := exec.Command("ffmpeg", "-v", "error", "-i", f, "-map", "0", "-f", "framemd5", "-").Output()
			if err != nil {
				t.Fatal(err)
			}
			return string(b)
		}
		if a, b := frames("testdata/video/canon.mp4"), frames(p); a == "" || a != b {
			t.Error("decoded frames differ (or none)")
		}
	}
	tool, err := exec.LookPath("exiftool")
	if err != nil {
		t.Skip("exiftool not installed")
	}
	b, err := exec.Command(tool, "-j", "-G1", "-ExifIFD:DateTimeOriginal", "-ExifIFD:CreateDate", "-IFD0:Make", p).Output()
	if err != nil {
		t.Fatal(err)
	}
	var got []map[string]any
	if err := json.Unmarshal(b, &got); err != nil || len(got) != 1 {
		t.Fatal(err, string(b))
	}
	for _, tag := range []string{"ExifIFD:DateTimeOriginal", "ExifIFD:CreateDate"} {
		if got[0][tag] != "2021:07:08 09:10:11" {
			t.Errorf("ExifTool's %s %v", tag, got[0][tag])
		}
	}
}

// canonThumbExif reads the Exif of a Canon movie's thumbnail.
func canonThumbExif(t *testing.T, file []byte) (Exif, []byte) {
	t.Helper()
	d := bytes.Index(file, []byte("CNDA"))
	n := int(binary.BigEndian.Uint32(file[d-4:]))
	jpeg := file[d+4 : d-4+n]
	x, err := ReadExif(bytes.NewReader(jpeg), int64(len(jpeg)))
	if err != nil {
		t.Fatal(err)
	}
	return x, jpeg
}

// A JPEG that grows (a fraction of a second adds SubSecTimeOriginal and
// SubSecTimeDigitized) takes its room from CNTH's padding; without
// padding, CNTH and the uuid box grow, and moov with them.
func TestCanonDateGrows(t *testing.T) {
	src := readFile(t, "testdata/video/canon.mp4")
	d := time.Date(2021, 7, 8, 9, 10, 11, 250e6, time.UTC)
	u0, c0, d0, _, _ := canonBoxes(t, src)

	out := setCanonDate(t, src, d)
	u, c, n, padAt, padEnd := canonBoxes(t, out)
	if u != u0 || c != c0 || padEnd-padAt != c0-8-n {
		t.Errorf("with padding: Canon boxes %d %d %d, were %d %d %d", u, c, n, u0, c0, d0)
	}
	if x, _ := canonThumbExif(t, out); !x.DateTimeOriginal.Equal(d) {
		t.Errorf("with padding: thumbnail date %v", x.DateTimeOriginal)
	}
	if v, err := ReadVideo(bytes.NewReader(out), int64(len(out))); err != nil || !v.Created.Equal(d.Truncate(time.Second)) && !v.Created.Equal(d) {
		t.Errorf("with padding: read back %v, %v", v.Created, err)
	}

	// The padding cut away: CNTH ends with CNDA. moov is the last box
	// (canon.go.txt), so no chunk offset moves.
	_, _, _, padAt, padEnd = canonBoxes(t, src)
	cut := padEnd - padAt
	nopad := append(append([]byte(nil), src[:padAt]...), src[padEnd:]...)
	for _, typ := range []string{"moov", "uuid", "CNTH"} {
		at := bytes.Index(nopad, []byte(typ)) - 4
		binary.BigEndian.PutUint32(nopad[at:], binary.BigEndian.Uint32(nopad[at:])-uint32(cut))
	}
	if _, err := ReadVideo(bytes.NewReader(nopad), int64(len(nopad))); err != nil {
		t.Fatal(err)
	}
	out = setCanonDate(t, nopad, d)
	_, c1, n1, _, _ := canonBoxes(t, nopad)
	if _, c, n, _, _ := canonBoxes(t, out); n <= n1 || c-n != c1-n1 {
		t.Errorf("without padding: CNTH %d CNDA %d, were %d %d", c, n, c1, n1)
	}
	if x, _ := canonThumbExif(t, out); !x.DateTimeOriginal.Equal(d) {
		t.Errorf("without padding: thumbnail date %v", x.DateTimeOriginal)
	}
}

// An edit without a date leaves the Canon thumbnail as it is.
func TestCanonDateUntouched(t *testing.T) {
	src := readFile(t, "testdata/video/canon.mp4")
	title := "Lake"
	var out bytes.Buffer
	if err := SetVideoMetadata(bytes.NewReader(src), int64(len(src)), &out, VideoEdit{Title: &title}); err != nil {
		t.Fatal(err)
	}
	_, a := canonThumbExif(t, src)
	if _, b := canonThumbExif(t, out.Bytes()); !bytes.Equal(a, b) {
		t.Error("a title edit changed the thumbnail")
	}
}
