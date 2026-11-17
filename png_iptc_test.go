package mediameta

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A PNG's IPTC (a raw profile zTXt, as ExifTool writes it) reads as
// ExifTool reads it, and an IPTC edit writes it as ExifTool would.
func TestPNGIPTC(t *testing.T) {
	tool, err := exec.LookPath("exiftool")
	if err != nil {
		t.Skip("exiftool not installed")
	}
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	dir := t.TempDir()
	base := filepath.Join(dir, "base.png")
	if err := exec.Command(ff, "-v", "error", "-y", "-f", "lavfi", "-i", "testsrc=size=32x24", "-frames:v", "1", base).Run(); err != nil {
		t.Fatal(err)
	}
	withIPTC := filepath.Join(dir, "iptc-src.png")
	if err := os.WriteFile(withIPTC, readFile(t, base), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(tool, "-q", "-q", "-overwrite_original", "-IPTC:ObjectName=Old title", "-IPTC:Caption-Abstract=Old caption",
		"-IPTC:Keywords=old", "-IPTC:By-line=Kept author", withIPTC).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	type iptcTags struct {
		ObjectName, CaptionAbstract, ByLine string
		Keywords                            any
	}
	read := func(p string) iptcTags {
		out, err := exec.Command(tool, "-j", "-q", "-IPTC:ObjectName", "-IPTC:Caption-Abstract", "-IPTC:Keywords", "-IPTC:By-line", p).Output()
		var got []map[string]any
		if err != nil || json.Unmarshal(out, &got) != nil || len(got) != 1 {
			t.Fatalf("%v %s", err, out)
		}
		s := func(k string) string { v, _ := got[0][k].(string); return v }
		return iptcTags{s("ObjectName"), s("Caption-Abstract"), s("By-line"), got[0]["Keywords"]}
	}
	src := readFile(t, withIPTC)
	iim, err := ReadIPTC(bytes.NewReader(src), int64(len(src)))
	x, perr := ParseIPTC(iim)
	if err != nil || perr != nil || x.Title != "Old title" || x.Description != "Old caption" || strings.Join(x.Keywords, ",") != "old" {
		t.Fatalf("ReadIPTC: %+v, %v %v", x, err, perr)
	}
	for _, name := range []string{"iptc-src.png", "base.png"} {
		file := readFile(t, filepath.Join(dir, name))
		for _, c := range []struct {
			kind string
			edit IPTCEdit
		}{
			{"set", IPTCEdit{Title: ptr("Lake"), Description: ptr("Calm water"), Keywords: &[]string{"lake", "summer"}}},
			{"removed", IPTCEdit{Title: ptr(""), Description: ptr(""), Keywords: &[]string{}}},
		} {
			p, err := PlanMetadata(bytes.NewReader(file), int64(len(file)), MetadataEdit{IPTC: &c.edit})
			if err != nil {
				t.Fatalf("%s %s: %v", name, c.kind, err)
			}
			var buf bytes.Buffer
			if _, err := p.WriteTo(&buf); err != nil {
				t.Fatal(err)
			}
			out := buf.Bytes()
			checkPNGCRCs(t, name+" "+c.kind, out)
			path := filepath.Join(dir, c.kind+"-"+name)
			if err := os.WriteFile(path, out, 0o644); err != nil {
				t.Fatal(err)
			}
			got := read(path)
			m, err := ReadAll(bytes.NewReader(out), int64(len(out)))
			if err != nil {
				t.Fatal(err)
			}
			// The author ExifTool wrote stays; the edited fields are the
			// edit's.
			wantAuthor := ""
			if name == "iptc-src.png" {
				wantAuthor = "Kept author"
			}
			switch c.kind {
			case "set":
				if got.ObjectName != "Lake" || got.CaptionAbstract != "Calm water" || fmt.Sprint(got.Keywords) != "[lake summer]" || got.ByLine != wantAuthor {
					t.Errorf("%s set: ExifTool %+v", name, got)
				}
				if m.IPTC.Title != "Lake" || strings.Join(m.IPTC.Keywords, ",") != "lake,summer" {
					t.Errorf("%s set: ReadAll IPTC %+v", name, m.IPTC)
				}
				if iim, _ := ReadIPTC(bytes.NewReader(out), int64(len(out))); bytes.Contains(iim, []byte("Old")) {
					t.Errorf("%s set: an old value is still in the IIM data", name)
				}
			case "removed":
				if got.ObjectName != "" || got.CaptionAbstract != "" || got.Keywords != nil || got.ByLine != wantAuthor {
					t.Errorf("%s removed: ExifTool %+v", name, got)
				}
			}
		}
	}
}
