package mediameta

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A title and description written by VideoEdit reach every place
// ExifTool (and ReadVideo) reads them from, and removing them leaves none:
// Keys, the item list (added to one the file has), and the user data's
// QuickTime and 3GPP entries.
func TestVideoTextEverywhere(t *testing.T) {
	tool, err := exec.LookPath("exiftool")
	if err != nil {
		t.Skip("exiftool not installed")
	}
	dir := t.TempDir()
	for _, name := range []string{"titles.mp4", "itemlist.mp4", "userdata.mp4", "xmp.mp4", "keys.mov", "udta.mov"} {
		src := readFile(t, filepath.Join("testdata/video", name))
		for _, c := range []struct {
			name        string
			title, desc string
		}{{"set", "New title", "New description"}, {"removed", "", ""}} {
			var buf bytes.Buffer
			if err := SetVideoMetadata(bytes.NewReader(src), int64(len(src)), &buf, VideoEdit{Title: &c.title, Description: &c.desc}); err != nil {
				t.Fatalf("%s %s: %v", name, c.name, err)
			}
			out := buf.Bytes()
			v, err := ReadVideo(bytes.NewReader(out), int64(len(out)))
			if err != nil || v.Title != c.title || v.Description != c.desc {
				t.Errorf("%s %s: ReadVideo %q %q, %v", name, c.name, v.Title, v.Description, err)
			}
			p := filepath.Join(dir, c.name+"-"+name)
			if err := os.WriteFile(p, out, 0o644); err != nil {
				t.Fatal(err)
			}
			res, err := exec.Command(tool, "-j", "-a", "-G1", "-q", "-Title", "-Description", "-DisplayName", p).Output()
			if err != nil {
				t.Fatal(err)
			}
			var got []map[string]any
			if err := json.Unmarshal(res, &got); err != nil || len(got) != 1 {
				t.Fatalf("%v\n%s", err, res)
			}
			found := 0
			for k, val := range got[0] {
				if k == "SourceFile" || strings.HasPrefix(k, "XMP") {
					continue // XMP isn't VideoEdit's
				}
				want := c.title
				if strings.HasSuffix(k, ":Description") {
					want = c.desc
				}
				if s, _ := val.(string); s != want {
					t.Errorf("%s %s: ExifTool %s %q, want %q", name, c.name, k, val, want)
				}
				found++
			}
			if c.title != "" && found < 2 {
				t.Errorf("%s: the title and description in %d places: %v", name, found, got[0])
			}
			if c.title == "" && found > 0 {
				t.Errorf("%s: left after removal: %v", name, got[0])
			}
		}
	}
	// The item list ffmpeg makes gets the title, which players read.
	probe, err := exec.LookPath("ffprobe")
	if err != nil {
		return
	}
	out, err := exec.Command(probe, "-v", "error", "-show_entries", "format_tags=title", "-of", "csv=p=0",
		filepath.Join(dir, "set-xmp.mp4")).Output()
	if err != nil || strings.TrimSpace(string(out)) != "New title" {
		t.Errorf("ffprobe's title %q, %v", out, err)
	}
}
