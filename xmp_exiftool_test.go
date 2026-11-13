package mediameta

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

// The packets EditXMP writes read in ExifTool as ParseXMP reads them, and
// keep the properties it doesn't touch. Runs where exiftool is installed.
func TestXMPAgreesWithExifTool(t *testing.T) {
	tool, err := exec.LookPath("exiftool")
	if err != nil {
		t.Skip("exiftool not installed")
	}
	dir := t.TempDir()
	files := map[string][]byte{
		"source.xmp":  []byte(lightroomXMP),
		"edited.xmp":  editXMP(t, []byte(lightroomXMP), XMPEdit{Title: ptr("Lake <3 & co"), Description: ptr("New"), Keywords: &[]string{"lake", "ice"}, Rating: ptr(5)}),
		"removed.xmp": editXMP(t, []byte(lightroomXMP), XMPEdit{Title: ptr(""), Keywords: &[]string{}, RemoveRating: true}),
		"new.xmp":     editXMP(t, nil, XMPEdit{Title: ptr("T"), Keywords: &[]string{"x", "y"}, Rating: ptr(-1)}),
	}
	for name, b := range files {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out, err := exec.Command(tool, "-j", "-n", "-q", "-struct", "-XMP:Title", "-XMP:Description", "-XMP:Subject",
		"-XMP:Rating", "-XMP:City", "-XMP:Creator", "-XMP-dc:Title-de", dir).Output()
	if err != nil {
		t.Fatalf("exiftool: %v", err)
	}
	var got []map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("exiftool's JSON: %v\n%s", err, out)
	}
	if len(got) != len(files) {
		t.Fatalf("exiftool read %d files, want %d", len(got), len(files))
	}
	str := func(v any) string {
		switch v := v.(type) {
		case string:
			return v
		case float64:
			return json.Number(formatFloat(v)).String()
		}
		return ""
	}
	list := func(v any) []string {
		switch v := v.(type) {
		case []any:
			var out []string
			for _, e := range v {
				out = append(out, str(e))
			}
			return out
		case nil:
			return nil
		}
		return []string{str(v)}
	}
	for _, e := range got {
		name := filepath.Base(e["SourceFile"].(string))
		x := parseXMP(t, files[name])
		if str(e["Title"]) != x.Title || str(e["Description"]) != x.Description || !slices.Equal(list(e["Subject"]), x.Keywords) {
			t.Errorf("%s: ExifTool %v, ParseXMP %+v", name, e, x)
		}
		if _, has := e["Rating"]; has != x.HasRating || has && int(e["Rating"].(float64)) != x.Rating {
			t.Errorf("%s: ExifTool rating %v, ParseXMP %+v", name, e["Rating"], x)
		}
		if name == "new.xmp" {
			continue
		}
		// What EditXMP doesn't touch stays.
		if e["City"] != "Köln" || !slices.Equal(list(e["Creator"]), []string{"Someone"}) {
			t.Errorf("%s: lost City or Creator: %v", name, e)
		}
		if name == "edited.xmp" && e["Title-de"] != "See & Berge" {
			t.Errorf("%s: lost the German title: %v", name, e["Title-de"])
		}
	}
}

func formatFloat(f float64) string {
	b, _ := json.Marshal(f)
	return string(b)
}
