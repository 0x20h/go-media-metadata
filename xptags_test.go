package mediameta

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"testing"
)

// The XP tags read as ExifTool reads them (IFD0, UTF-16LE), on every
// ExifTool corpus file; xp-tags.jpg has each, non-ASCII text among them.
func TestXPTagsAgreeWithExifTool(t *testing.T) {
	tool, err := exec.LookPath("exiftool")
	if err != nil {
		t.Skip("exiftool not installed")
	}
	paths, _ := filepath.Glob("testdata/exiftool/*.jpg")
	out, err := exec.Command(tool, append([]string{"-j", "-q", "-IFD0:XPTitle", "-IFD0:XPComment", "-IFD0:XPAuthor",
		"-IFD0:XPKeywords", "-IFD0:XPSubject"}, paths...)...).Output()
	if err != nil {
		t.Fatal(err)
	}
	var got []struct{ SourceFile, XPTitle, XPComment, XPAuthor, XPKeywords, XPSubject string }
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	withXP := 0
	for _, e := range got {
		file := readFile(t, e.SourceFile)
		x, err := ReadExif(bytes.NewReader(file), int64(len(file)))
		if err != nil {
			t.Fatalf("%s: %v", e.SourceFile, err)
		}
		if x.XPTitle != e.XPTitle || x.XPComment != e.XPComment || x.XPAuthor != e.XPAuthor ||
			x.XPKeywords != e.XPKeywords || x.XPSubject != e.XPSubject {
			t.Errorf("%s: %q %q %q %q %q, ExifTool %+v", e.SourceFile, x.XPTitle, x.XPComment, x.XPAuthor, x.XPKeywords, x.XPSubject, e)
		}
		if e.XPKeywords != "" {
			withXP++
		}
	}
	if withXP == 0 {
		t.Error("no corpus file with XP tags")
	}
}
