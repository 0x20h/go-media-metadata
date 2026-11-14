package mediameta

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const hierarchicalXMP = `<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">
 <rdf:Description rdf:about="" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:lr="http://ns.adobe.com/lightroom/1.0/">
  <dc:subject><rdf:Bag><rdf:li>Berlin</rdf:li><rdf:li>Ada</rdf:li></rdf:Bag></dc:subject>
  <lr:hierarchicalSubject><rdf:Bag><rdf:li>Places|Europe|Berlin</rdf:li><rdf:li>People|Ada</rdf:li></rdf:Bag></lr:hierarchicalSubject>
 </rdf:Description>
</rdf:RDF></x:xmpmeta>`

func TestReadHierarchicalKeywords(t *testing.T) {
	x := parseXMP(t, []byte(hierarchicalXMP))
	if want := []string{"Places|Europe|Berlin", "People|Ada"}; !slices.Equal(x.HierarchicalKeywords, want) {
		t.Errorf("hierarchical keywords %q, want %q", x.HierarchicalKeywords, want)
	}
	if want := []string{"Berlin", "Ada"}; !slices.Equal(x.Keywords, want) {
		t.Errorf("keywords %q, want %q", x.Keywords, want)
	}
	if x := parseXMP(t, []byte(lightroomXMP)); x.HierarchicalKeywords != nil {
		t.Errorf("a packet without them: %q", x.HierarchicalKeywords)
	}
}

func TestEditHierarchicalKeywords(t *testing.T) {
	set := []string{"Places|Europe|Köln", "Events|Rock & Roll <live>"}
	// Added to a packet without the namespace, which gets declared.
	out := editXMP(t, []byte(lightroomXMP), XMPEdit{HierarchicalKeywords: &set})
	x := parseXMP(t, out)
	if !slices.Equal(x.HierarchicalKeywords, set) {
		t.Errorf("added: %q", x.HierarchicalKeywords)
	}
	if want := []string{"lake", "snow"}; !slices.Equal(x.Keywords, want) {
		t.Errorf("dc:subject changed with it: %q", x.Keywords)
	}
	if !bytes.Contains(out, []byte(`xmlns:lr="http://ns.adobe.com/lightroom/1.0/"`)) {
		t.Errorf("the lr namespace isn't declared:\n%s", out)
	}
	// Replaced, in order.
	again := []string{"People|Ada"}
	if x := parseXMP(t, editXMP(t, []byte(hierarchicalXMP), XMPEdit{HierarchicalKeywords: &again})); !slices.Equal(x.HierarchicalKeywords, again) {
		t.Errorf("replaced: %q", x.HierarchicalKeywords)
	}
	// The same list again changes nothing.
	same := []string{"Places|Europe|Berlin", "People|Ada"}
	if out := editXMP(t, []byte(hierarchicalXMP), XMPEdit{HierarchicalKeywords: &same}); !bytes.Equal(out, []byte(hierarchicalXMP)) {
		t.Errorf("an edit to the same list changed the packet")
	}
	// An empty list removes the property, and only it.
	none := []string{}
	out = editXMP(t, []byte(hierarchicalXMP), XMPEdit{HierarchicalKeywords: &none})
	if x := parseXMP(t, out); x.HierarchicalKeywords != nil || len(x.Keywords) != 2 {
		t.Errorf("removed: %q, keywords %q", x.HierarchicalKeywords, x.Keywords)
	}
	if bytes.Contains(out, []byte("hierarchicalSubject")) {
		t.Errorf("the property is still in the packet:\n%s", out)
	}
	bad := []string{"Places|\xff"}
	if _, err := EditXMP([]byte(hierarchicalXMP), XMPEdit{HierarchicalKeywords: &bad}); err == nil {
		t.Error("a keyword that isn't UTF-8 was accepted")
	}
}

// Through a file: PlanMetadata's XMP part writes them, ReadXMP reads them
// back.
func TestPlanHierarchicalKeywords(t *testing.T) {
	set := []string{"Places|Europe|Berlin"}
	for name, file := range map[string][]byte{"JPEG": plainJPEG(t), "PNG": testPNG(t), "HEIF": planFiles(t)[0].data} {
		p, err := PlanMetadata(bytes.NewReader(file), int64(len(file)), MetadataEdit{XMP: &XMPEdit{HierarchicalKeywords: &set}})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		checkPlan(t, file, p)
		out := written(t, p)
		packet, err := ReadXMP(bytes.NewReader(out), int64(len(out)))
		if err != nil {
			t.Fatal(err)
		}
		if x := parseXMP(t, packet); !slices.Equal(x.HierarchicalKeywords, set) {
			t.Errorf("%s: %q", name, x.HierarchicalKeywords)
		}
	}
}

// ExifTool reads what this package writes, and this package reads what
// ExifTool writes. Runs where exiftool is installed.
func TestHierarchicalKeywordsExifTool(t *testing.T) {
	tool, err := exec.LookPath("exiftool")
	if err != nil {
		t.Skip("exiftool not installed")
	}
	dir := t.TempDir()
	set := []string{"Places|Europe|Köln", "People|Ada"}
	file := plainJPEG(t)
	p, err := PlanMetadata(bytes.NewReader(file), int64(len(file)), MetadataEdit{XMP: &XMPEdit{HierarchicalKeywords: &set}})
	if err != nil {
		t.Fatal(err)
	}
	ours := filepath.Join(dir, "ours.jpg")
	if err := os.WriteFile(ours, written(t, p), 0o600); err != nil {
		t.Fatal(err)
	}
	// "##" between items: they contain "|", and ExifTool prints a newline
	// separator as ".".
	out, err := exec.Command(tool, "-s3", "-sep", "##", "-XMP-lr:HierarchicalSubject", ours).Output()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Split(strings.TrimSpace(string(out)), "##"); !slices.Equal(got, set) {
		t.Errorf("ExifTool reads %q, want %q", got, set)
	}

	theirs := filepath.Join(dir, "theirs.jpg")
	if err := os.WriteFile(theirs, file, 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{"-q", "-overwrite_original", "-charset", "UTF8"}
	for _, k := range set {
		args = append(args, "-XMP-lr:HierarchicalSubject+="+k)
	}
	if err := exec.Command(tool, append(args, theirs)...).Run(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(theirs)
	if err != nil {
		t.Fatal(err)
	}
	packet, err := ReadXMP(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	if x := parseXMP(t, packet); !slices.Equal(x.HierarchicalKeywords, set) {
		t.Errorf("read from ExifTool's packet: %q, want %q", x.HierarchicalKeywords, set)
	}
}
