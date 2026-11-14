package mediameta

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

// xmpEditFiles are files of every format planXMPEdit writes, with an XMP
// packet and without: the HEIF layouts and sample files, JPEGs, PNGs and
// WebPs.
func xmpEditFiles(t testing.TB) map[string][]byte {
	out := map[string][]byte{}
	add := func(name string, file []byte) {
		out[name] = file
		p, err := PlanXMP(bytes.NewReader(file), int64(len(file)), xmpPacket("Old title", 0))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var b bytes.Buffer
		if _, err := p.WriteTo(&b); err != nil {
			t.Fatal(err)
		}
		out[name+" with xmp"] = b.Bytes()
	}
	for _, f := range planFiles(t) {
		add("heif "+f.name, f.data)
	}
	for name, f := range jpegFiles(t) {
		add("jpeg "+name, f)
	}
	for name, f := range chunkedFiles(t) {
		out[name] = f
	}
	add("png", testPNG(t))
	return out
}

// xmpEdits are the edits run on every file.
var xmpEdits = []struct {
	name string
	edit XMPEdit
}{
	{"all", XMPEdit{Title: ptr("Harbour at dusk"), Description: ptr("Boats & nets"), Keywords: &[]string{"harbour", "boats"}, Rating: ptr(4)}},
	{"title", XMPEdit{Title: ptr("Old title, longer now than it was before")}},
	{"remove", XMPEdit{Title: ptr(""), Keywords: &[]string{}, RemoveRating: true}},
	{"rejected", XMPEdit{Rating: ptr(-1)}},
}

// wantXMP is what reading the edited packet gives: the edit's fields, the
// rest as they were.
func wantXMP(before XMP, e XMPEdit) XMP {
	w := before
	if e.Title != nil {
		w.Title = *e.Title
	}
	if e.Description != nil {
		w.Description = *e.Description
	}
	if e.Keywords != nil {
		w.Keywords = slices.Clone(*e.Keywords)
		if len(w.Keywords) == 0 {
			w.Keywords = nil
		}
	}
	if e.Rating != nil {
		w.Rating, w.HasRating = *e.Rating, true
	}
	if e.RemoveRating {
		w.Rating, w.HasRating = 0, false
	}
	return w
}

func readXMPFields(t *testing.T, file []byte) XMP {
	t.Helper()
	b, err := ReadXMP(bytes.NewReader(file), int64(len(file)))
	if err != nil {
		t.Fatalf("ReadXMP: %v", err)
	}
	if len(b) == 0 {
		return XMP{}
	}
	x, err := ParseXMP(b)
	if err != nil {
		t.Fatalf("ParseXMP: %v", err)
	}
	return x
}

// sameImage checks that an edit left a file's image data as it was: a
// HEIF file's image items, a JPEG's scan, a PNG's or WebP's image chunks.
func sameImage(t *testing.T, before, after []byte) {
	t.Helper()
	r := bytes.NewReader(before)
	switch n := int64(len(before)); {
	case isJPEG(r, n):
		if !bytes.Equal(jpegScan(t, before), jpegScan(t, after)) {
			t.Error("the scan data changed")
		}
	case isPNG(r, n), isWebP(r, n):
		checkChunked(t, before, after)
	default:
		b, a := imageItems(before), imageItems(after)
		if len(b) == 0 {
			t.Fatal("no image items")
		}
		for id, d := range b {
			if !bytes.Equal(a[id], d) {
				t.Errorf("item %d's data changed", id)
			}
		}
	}
}

// Every edit of every file: the packet reads back with the edit and the
// rest as it was, the image data is as it was, the plan's invariants hold,
// and the same edit again changes nothing, in place.
func TestPlanXMPEdit(t *testing.T) {
	for name, file := range xmpEditFiles(t) {
		before := readXMPFields(t, file)
		for _, e := range xmpEdits {
			t.Run(name+"/"+e.name, func(t *testing.T) {
				p, err := planXMPEdit(bytes.NewReader(file), int64(len(file)), e.edit)
				if err != nil {
					t.Fatal(err)
				}
				checkPlan(t, file, p)
				out := written(t, p)
				if got, want := readXMPFields(t, out), wantXMP(before, e.edit); !xmpEqual(got, want) {
					t.Errorf("reads back as %+v, want %+v", got, want)
				}
				sameImage(t, file, out)
				again, err := planXMPEdit(bytes.NewReader(out), int64(len(out)), e.edit)
				if err != nil {
					t.Fatal(err)
				}
				if !again.InPlace || !bytes.Equal(written(t, again), out) {
					t.Error("the same edit again changes the file")
				}
			})
		}
	}
}

func xmpEqual(a, b XMP) bool {
	return a.Title == b.Title && a.Description == b.Description && slices.Equal(a.Keywords, b.Keywords) &&
		a.Rating == b.Rating && a.HasRating == b.HasRating
}

// A rating out of range, or a packet this package can't parse, is refused
// and nothing is planned.
func TestPlanXMPEditErrors(t *testing.T) {
	file := testPNG(t)
	if _, err := planXMPEdit(bytes.NewReader(file), int64(len(file)), XMPEdit{Rating: ptr(6)}); err == nil {
		t.Error("a rating of 6 was planned")
	}
	broken := pngWithChunks(t, pngChunkBytes("iTXt", xmpITXt([]byte(`<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF`))))
	if _, err := planXMPEdit(bytes.NewReader(broken), int64(len(broken)), XMPEdit{Title: ptr("x")}); err == nil {
		t.Error("a packet that doesn't parse was replaced")
	}
}

// ExifTool reads the edit back from every format, with no warning the
// source didn't have.
func TestPlanXMPEditWithExifTool(t *testing.T) {
	tool, err := exec.LookPath("exiftool")
	if err != nil {
		t.Skip("exiftool not installed")
	}
	dir := t.TempDir()
	edit := xmpEdits[0].edit
	names := map[string]string{}
	warned := map[string]string{}
	for name, file := range xmpEditFiles(t) {
		p, err := planXMPEdit(bytes.NewReader(file), int64(len(file)), edit)
		if err != nil {
			t.Fatal(err)
		}
		base := fmt.Sprintf("%03d", len(names))
		names[base] = name
		ext := ".heic"
		switch r, n := bytes.NewReader(file), int64(len(file)); {
		case isJPEG(r, n):
			ext = ".jpg"
		case isPNG(r, n):
			ext = ".png"
		case isWebP(r, n):
			ext = ".webp"
		}
		for _, f := range []struct {
			suffix string
			data   []byte
		}{{"-src" + ext, file}, {ext, written(t, p)}} {
			if err := os.WriteFile(filepath.Join(dir, base+f.suffix), f.data, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	got, err := exec.Command(tool, "-j", "-q", "-validate", "-warning", "-error", "-a",
		"-XMP-dc:Title", "-XMP-dc:Subject", "-XMP-xmp:Rating", "-n", dir).Output()
	if err != nil {
		t.Fatalf("exiftool: %v", err)
	}
	var files []map[string]any
	if err := json.Unmarshal(got, &files); err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		base := filepath.Base(fmt.Sprint(f["SourceFile"]))
		if len(base) > 7 && base[3:7] == "-src" {
			warned[base[:3]] = fmt.Sprint(f["Validate"])
		}
	}
	checked := 0
	for _, f := range files {
		base := filepath.Base(fmt.Sprint(f["SourceFile"]))
		if len(base) > 7 && base[3:7] == "-src" {
			continue
		}
		checked++
		name := names[base[:3]]
		if f["Title"] != "Harbour at dusk" || fmt.Sprint(f["Subject"]) != "[harbour boats]" || fmt.Sprint(f["Rating"]) != "4" {
			t.Errorf("%s: ExifTool reads %v / %v / %v", name, f["Title"], f["Subject"], f["Rating"])
		}
		// Validate, with -n: errors, warnings, minor warnings. The edit may
		// take one away (a stale second packet), never add one.
		var e, w, m, se, sw, sm int
		_, _ = fmt.Sscan(fmt.Sprint(f["Validate"]), &e, &w, &m)
		_, _ = fmt.Sscan(warned[base[:3]], &se, &sw, &sm)
		if e > se || w > sw || m > sm {
			t.Errorf("%s: ExifTool validates it as %v (%v %v), the source as %s", name, f["Validate"], f["Warning"], f["Error"], warned[base[:3]])
		}
	}
	if checked != len(names) {
		t.Errorf("ExifTool read %d edited files, want %d", checked, len(names))
	}
}

// Every XMP edit that succeeds keeps the plan's invariants, reads back
// with the edit, leaves the image data as it was where the file is one of
// the formats, and is in place when made again.
func FuzzPlanXMPEdit(f *testing.F) {
	seeds(f)
	jpegSeeds(f)
	for _, file := range chunkedFiles(f) {
		f.Add(file)
	}
	edit := xmpEdits[0].edit
	f.Fuzz(func(t *testing.T, data []byte) {
		withinDeadline(t, func() {
			p, err := planXMPEdit(bytes.NewReader(data), int64(len(data)), edit)
			if err != nil {
				return
			}
			checkPlan(t, data, p)
			var buf bytes.Buffer
			if _, err := p.WriteTo(&buf); err != nil {
				t.Fatal(err)
			}
			out := buf.Bytes()
			b, err := ReadXMP(bytes.NewReader(out), int64(len(out)))
			if err != nil {
				t.Fatalf("the edited file's XMP doesn't read: %v", err)
			}
			x, err := ParseXMP(b)
			if err != nil || x.Title != *edit.Title || x.Rating != 4 || !slices.Equal(x.Keywords, *edit.Keywords) {
				t.Fatalf("the edit reads back as %+v, %v", x, err)
			}
			if before := imageItems(data); before != nil {
				after := imageItems(out)
				for id, d := range before {
					if !bytes.Equal(after[id], d) {
						t.Errorf("item %d's data changed", id)
					}
				}
			}
			again, err := planXMPEdit(bytes.NewReader(out), int64(len(out)), edit)
			if err != nil {
				t.Fatalf("the edited file can't be edited again: %v", err)
			}
			if !again.InPlace {
				t.Error("the same edit again isn't in place")
			}
		})
	})
}
