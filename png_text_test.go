package mediameta

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A PNG's Title and Description text chunks and tIME read as ExifTool
// reads them (its PNG group), and ReadAll falls back to them.
func TestPNGTextAgreesWithExifTool(t *testing.T) {
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
	cases := map[string][]string{
		"text.png":       {"-PNG:Title=Lake Constance", "-PNG:Description=Café ★ by the water", "-PNG:ModifyDate=2021:07:08 09:10:11Z"},
		"compressed.png": {"-z", "-PNG:Title=Compressed title", "-PNG:Description=Compressed description"},
		"none.png":       nil,
	}
	for name, args := range cases {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, readFile(t, base), 0o644); err != nil {
			t.Fatal(err)
		}
		if args != nil {
			if out, err := exec.Command(tool, append(append([]string{"-q", "-overwrite_original"}, args...), p)...).CombinedOutput(); err != nil {
				t.Fatalf("%s: %v %s", name, err, out)
			}
		}
		file := readFile(t, p)
		pt, err := ReadPNGText(bytes.NewReader(file), int64(len(file)))
		if err != nil {
			t.Fatal(err)
		}
		m, err := ReadAll(bytes.NewReader(file), int64(len(file)))
		if err != nil || m.PNG != pt || m.Title != pt.Title || m.Description != pt.Description {
			t.Errorf("%s: ReadAll %+v / %q %q, ReadPNGText %+v (%v)", name, m.PNG, m.Title, m.Description, pt, err)
		}
		cmd := exec.Command(tool, "-j", "-q", "-d", "%Y-%m-%dT%H:%M:%SZ", "-PNG:Title", "-PNG:Description", "-PNG:ModifyDate", p)
		cmd.Env = append(os.Environ(), "TZ=UTC")
		out, err := cmd.Output()
		var got []map[string]any
		if err != nil || json.Unmarshal(out, &got) != nil || len(got) != 1 {
			t.Fatalf("%v %s", err, out)
		}
		e := got[0]
		str := func(k string) string { s, _ := e[k].(string); return s }
		var mod string
		if !pt.Modified.IsZero() {
			mod = pt.Modified.Format(time.RFC3339)
		}
		if name == "text.png" && !pt.Modified.Equal(time.Date(2021, 7, 8, 9, 10, 11, 0, time.UTC)) {
			t.Errorf("text.png: tIME %v", pt.Modified)
		}
		if pt.Title != str("Title") || pt.Description != str("Description") || mod != strings.TrimSpace(str("ModifyDate")) {
			t.Errorf("%s: %+v, ExifTool %v", name, pt, e)
		}
	}
	if _, err := ReadPNGText(bytes.NewReader([]byte("not a png")), 9); err != ErrUnknownFormat {
		t.Errorf("not a PNG: %v", err)
	}
}
