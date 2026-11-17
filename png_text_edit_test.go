package mediameta

import (
	"bytes"
	"encoding/json"
	"hash/crc32"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// checkPNGCRCs fails on a chunk whose CRC doesn't match.
func checkPNGCRCs(t *testing.T, name string, b []byte) {
	t.Helper()
	chunks, err := pngChunks(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	for _, c := range chunks {
		body := b[c.at+4 : c.end()-4]
		if crc32.ChecksumIEEE(body) != uint32(b[c.end()-4])<<24|uint32(b[c.end()-3])<<16|uint32(b[c.end()-2])<<8|uint32(b[c.end()-1]) {
			t.Errorf("%s: %s chunk at %d: bad CRC", name, c.typ[:], c.at)
		}
	}
}

// An XMP title or description edit of a PNG also sets the PNG's own Title
// and Description text chunks (tEXt, zTXt, iTXt) where it has them, and
// removing one removes them: ExifTool's PNG:Title then agrees with the XMP.
func TestPNGTextFollowsXMP(t *testing.T) {
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
	mk := func(name string, args ...string) []byte {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, readFile(t, base), 0o644); err != nil {
			t.Fatal(err)
		}
		if len(args) > 0 {
			if out, err := exec.Command(tool, append([]string{"-q", "-overwrite_original"}, append(args, p)...)...).CombinedOutput(); err != nil {
				t.Fatalf("%s: %v %s", name, err, out)
			}
		}
		return readFile(t, p)
	}
	inputs := map[string][]byte{
		"text.png":       mk("text-src.png", "-PNG:Title=Old title", "-PNG:Description=Ancienne déscription ★"),
		"compressed.png": mk("z-src.png", "-z", "-PNG:Title=Old title", "-PNG:Description=Old description"),
		"none.png":       mk("none-src.png"),
	}
	for name, src := range inputs {
		for _, c := range []struct {
			name        string
			title, desc string
		}{{"latin1", "Lake Constance", "Café at the lake"}, {"unicode", "湖", "Ein See ★"}, {"removed", "", ""}} {
			p, err := PlanMetadata(bytes.NewReader(src), int64(len(src)), MetadataEdit{XMP: &XMPEdit{Title: &c.title, Description: &c.desc}})
			if err != nil {
				t.Fatalf("%s %s: %v", name, c.name, err)
			}
			var buf bytes.Buffer
			if _, err := p.WriteTo(&buf); err != nil {
				t.Fatal(err)
			}
			out := buf.Bytes()
			checkPNGCRCs(t, name+" "+c.name, out)
			for _, old := range []string{"Old title", "Old description", "Ancienne"} {
				if bytes.Contains(out, []byte(old)) {
					t.Errorf("%s %s: %q is still in the file", name, c.name, old)
				}
			}
			path := filepath.Join(dir, c.name+"-"+name)
			if err := os.WriteFile(path, out, 0o644); err != nil {
				t.Fatal(err)
			}
			res, err := exec.Command(tool, "-j", "-a", "-G1", "-q", "-validate", "-warning", "-PNG:Title", "-PNG:Description",
				"-XMP-dc:Title", "-XMP-dc:Description", path).Output()
			if err != nil {
				t.Fatal(err)
			}
			var got []map[string]any
			if err := json.Unmarshal(res, &got); err != nil || len(got) != 1 {
				t.Fatalf("%v\n%s", err, res)
			}
			e := got[0]
			if w, ok := e["ExifTool:Warning"]; ok {
				t.Errorf("%s %s: ExifTool warns %v", name, c.name, w)
			}
			for k, v := range e {
				if k == "SourceFile" || strings.HasPrefix(k, "ExifTool:") {
					continue
				}
				want := c.title
				if strings.HasSuffix(k, "Description") {
					want = c.desc
				}
				if s, _ := v.(string); s != want {
					t.Errorf("%s %s: ExifTool %s %q, want %q", name, c.name, k, v, want)
				}
			}
			// The PNG's own chunks are kept (not added) when the file has them.
			_, hasPNG := e["PNG:Title"]
			if want := name != "none.png" && c.title != ""; hasPNG != want {
				t.Errorf("%s %s: PNG:Title present %v, want %v", name, c.name, hasPNG, want)
			}
		}
	}
}

func TestPNGTextEncoding(t *testing.T) {
	for _, c := range []struct {
		typ, s, wantTyp string
	}{
		{"tEXt", "Café", "tEXt"},
		{"tEXt", "湖", "iTXt"},
		{"zTXt", "plain", "tEXt"},
		{"tEXt", "tab\there", "iTXt"},
		{"iTXt", "plain", "iTXt"},
	} {
		old := []byte("Title\x00x")
		if c.typ == "iTXt" {
			old = []byte("Title\x00\x00\x00de\x00Titel\x00alt")
		}
		data, typ := pngText(c.typ, old, "Title", c.s)
		if typ != c.wantTyp {
			t.Errorf("%s %q: %s, want %s", c.typ, c.s, typ, c.wantTyp)
		}
		if typ == "iTXt" {
			it, ok := parseITXt(data)
			if !ok || string(it.text) != c.s || it.keyword != "Title" || c.typ == "iTXt" && !bytes.Contains(it.head, []byte("de\x00Titel\x00")) {
				t.Errorf("%s %q: %q", c.typ, c.s, data)
			}
		}
	}
}
