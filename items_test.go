package mediameta

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/0x20h/go-media-metadata/internal/bmff"
	"github.com/0x20h/go-media-metadata/internal/bmff/testbuild"
)

// xmpPacket is an XMP packet with a title, of at least n bytes (padded
// with whitespace, as XMP packets are).
func xmpPacket(title string, n int) []byte {
	p := []byte(`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?><x:xmpmeta xmlns:x="adobe:ns:meta/">` +
		`<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#"><rdf:Description rdf:about="" ` +
		`xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title><rdf:Alt><rdf:li xml:lang="x-default">` + title +
		`</rdf:li></rdf:Alt></dc:title></rdf:Description></rdf:RDF></x:xmpmeta>`)
	for len(p) < n-len(`<?xpacket end="w"?>`) {
		p = append(p, ' ')
	}
	return append(p, `<?xpacket end="w"?>`...)
}

// paddedTIFF is photoTIFF followed by n bytes nothing points to: a longer
// value for the same fields.
func paddedTIFF(n int) []byte {
	return append(photoTIFF(binary.LittleEndian), make([]byte, n)...)
}

// metadataEdits are the edits run on every layout: Exif and XMP, short
// enough to fit an existing item and too long to.
var metadataEdits = []struct {
	name string
	exif []byte
	xmp  []byte
}{
	{name: "exif", exif: photoTIFF(binary.LittleEndian)},
	{name: "exif big-endian", exif: photoTIFF(binary.BigEndian)},
	{name: "exif long", exif: paddedTIFF(4000)},
	{name: "xmp", xmp: xmpPacket("Harbour at dusk", 0)},
	{name: "xmp long", xmp: xmpPacket("Harbour at dawn", 6000)},
}

func planMetadata(t *testing.T, file []byte, exif, xmp []byte) *Plan {
	t.Helper()
	var p *Plan
	var err error
	if exif != nil {
		p, err = PlanExif(bytes.NewReader(file), int64(len(file)), exif)
	} else {
		p, err = PlanXMP(bytes.NewReader(file), int64(len(file)), xmp)
	}
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func written(t *testing.T, p *Plan) []byte {
	t.Helper()
	var out bytes.Buffer
	if _, err := p.WriteTo(&out); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// On every layout and sample file: the item reads back as written, the
// image items' data and the orientation are what they were, the plan's
// invariants hold, and writing the same value again is in place.
func TestSetMetadataItems(t *testing.T) {
	for _, f := range planFiles(t) {
		for _, e := range metadataEdits {
			t.Run(f.name+"/"+e.name, func(t *testing.T) {
				p := planMetadata(t, f.data, e.exif, e.xmp)
				checkPlan(t, f.data, p)
				out := written(t, p)
				if e.exif != nil {
					x, err := ReadExif(bytes.NewReader(out), int64(len(out)))
					if err != nil {
						t.Fatal(err)
					}
					if x.DateTimeOriginal.IsZero() || x.GPS == nil {
						t.Errorf("the Exif written doesn't read back: %+v", x)
					}
				} else {
					got, err := ReadXMP(bytes.NewReader(out), int64(len(out)))
					if err != nil {
						t.Fatal(err)
					}
					if !bytes.Equal(got, e.xmp) {
						t.Errorf("XMP reads back as %d bytes, wrote %d", len(got), len(e.xmp))
					}
				}
				if got, want := orientationOfFile(t, out), orientationOfFile(t, f.data); got != want {
					t.Errorf("orientation %d, was %d", got, want)
				}
				before := itemData(t, f.data)
				after := itemData(t, out)
				fb, _ := parseHEIF(bytes.NewReader(f.data), int64(len(f.data)))
				for id, d := range before {
					if typ := fb.m.ItemTypes[id]; typ == [4]byte{'E', 'x', 'i', 'f'} || typ == [4]byte{'m', 'i', 'm', 'e'} {
						continue
					}
					if !bytes.Equal(after[id], d) {
						t.Errorf("item %d's data changed", id)
					}
				}
				again := planMetadata(t, out, e.exif, e.xmp)
				if !again.InPlace {
					t.Errorf("the same value again isn't in place (%d → %d bytes)", again.Size, again.NewSize)
				}
				checkPlan(t, out, again)
			})
		}
	}
}

// A value that fits the item is a patch: the data and the item's length.
// One that doesn't goes in a media data box at the end.
func TestSetMetadataInPlace(t *testing.T) {
	file, _ := testbuild.Build(testbuild.Default())
	long := planMetadata(t, file, paddedTIFF(4000), nil)
	if long.InPlace || long.NewSize <= long.Size {
		t.Fatalf("a long Exif: InPlace %v, %d → %d bytes", long.InPlace, long.Size, long.NewSize)
	}
	grown := written(t, long)
	short := planMetadata(t, grown, photoTIFF(binary.BigEndian), nil)
	if !short.InPlace {
		t.Fatal("a shorter Exif isn't in place")
	}
	n := 0
	for _, pt := range short.Patches {
		n += len(pt.Data)
	}
	// The whole old extent: the new value, then zeros over the rest.
	if want := 4 + len(paddedTIFF(4000)); n < want || n > want+patchMergeGap {
		t.Errorf("%d bytes patched, want the old extent's %d", n, want)
	}
	// Adding an XMP item grows the meta box: offsets move, not in place.
	add := planMetadata(t, file, nil, xmpPacket("x", 0))
	if add.InPlace {
		t.Error("adding an XMP item is in place")
	}
	checkPlan(t, file, add)
}

func TestSetMetadataErrors(t *testing.T) {
	file, _ := testbuild.Build(testbuild.Default())
	r := bytes.NewReader(file)
	if _, err := PlanExif(r, int64(len(file)), []byte("Exif\x00\x00II*\x00")); err == nil {
		t.Error("Exif without a TIFF header first accepted")
	}
	if _, err := PlanXMP(r, int64(len(file)), []byte("<html/>")); err == nil {
		t.Error("non-XMP accepted")
	}
	if _, err := PlanXMP(r, int64(len(file)), []byte("rdf:RDF \xff")); err == nil {
		t.Error("invalid UTF-8 accepted")
	}
	// A JPEG takes XMP too (jpeg_xmp_test.go), but not XMP that doesn't
	// parse.
	jpeg := plainJPEG(t)
	if _, err := PlanXMP(bytes.NewReader(jpeg), int64(len(jpeg)), []byte("<x:xmpmeta>")); !errors.Is(err, ErrMalformed) {
		t.Errorf("a JPEG with broken XMP: %v", err)
	}
}

// ExifTool reads what was written: the Exif fields on the sample files
// and the XMP title. Runs where exiftool is installed.
func TestMetadataItemsAgreeWithExifTool(t *testing.T) {
	tool, err := exec.LookPath("exiftool")
	if err != nil {
		t.Skip("exiftool not installed")
	}
	dir := t.TempDir()
	var names []string
	for _, f := range planFiles(t) {
		out := written(t, planMetadata(t, f.data, paddedTIFF(100), nil))
		out = written(t, planMetadata(t, out, nil, xmpPacket("Harbour at dusk", 0)))
		name := fmt.Sprintf("%02d.heic", len(names))
		names = append(names, f.name)
		if err := os.WriteFile(filepath.Join(dir, name), out, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := exec.Command(tool, "-j", "-n", "-q", "-EXIF:DateTimeOriginal", "-Composite:GPSLatitude", "-XMP-dc:Title", dir).Output()
	if err != nil {
		t.Fatalf("exiftool: %v", err)
	}
	var files []struct {
		SourceFile       string
		DateTimeOriginal string
		GPSLatitude      *float64
		Title            string
	}
	if err := json.Unmarshal(got, &files); err != nil {
		t.Fatal(err)
	}
	if len(files) != len(names) {
		t.Fatalf("exiftool read %d files, want %d", len(files), len(names))
	}
	for _, f := range files {
		if f.DateTimeOriginal != "2025:05:04 13:14:15" || f.GPSLatitude == nil || f.Title != "Harbour at dusk" {
			t.Errorf("%s: ExifTool reads %+v", f.SourceFile, f)
		}
	}
}

// A file without an iref box gets one, after iinf, with the new item's
// cdsc reference to the primary image.
func TestAddMetadataItemCreatesIref(t *testing.T) {
	o := testbuild.Default()
	o.Grid, o.Thumbnail, o.Irot, o.ExifOrientation = false, false, -1, -1
	file, _ := testbuild.Build(o)
	f, err := parseHEIF(bytes.NewReader(file), int64(len(file)))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.m.Refs) != 0 {
		t.Skip("the fixture has references")
	}
	out := written(t, planMetadata(t, file, nil, xmpPacket("x", 0)))
	g, err := parseHEIF(bytes.NewReader(out), int64(len(out)))
	if err != nil {
		t.Fatal(err)
	}
	id, found, err := g.metadataItem(kindXMP)
	if err != nil || !found {
		t.Fatalf("no XMP item: %v", err)
	}
	if len(g.m.Refs) != 1 || g.m.Refs[0].Type != [4]byte{'c', 'd', 's', 'c'} || g.m.Refs[0].From != id || g.m.Refs[0].To[0] != g.m.Primary {
		t.Errorf("references %+v", g.m.Refs)
	}
}

// secret marks old metadata that must not survive an edit.
const secret = "GPS-51.5007N-0.1246W-SECRET"

// Old metadata isn't left in the file: whether the new value is shorter
// (in place) or longer (moved to the end), no copy of the old value's
// distinctive bytes remains anywhere, on every layout, in mdat or idat.
func TestOldMetadataIsZeroed(t *testing.T) {
	oldExif := append(paddedTIFF(200), secret...)
	oldXMP := xmpPacket(secret, 600)
	for _, f := range planFiles(t) {
		for _, c := range []struct {
			name     string
			old, new [2][]byte // exif, xmp
		}{
			{"exif shorter", [2][]byte{oldExif, nil}, [2][]byte{photoTIFF(binary.LittleEndian), nil}},
			{"exif longer", [2][]byte{oldExif, nil}, [2][]byte{paddedTIFF(5000), nil}},
			{"xmp shorter", [2][]byte{nil, oldXMP}, [2][]byte{nil, xmpPacket("x", 0)}},
			{"xmp longer", [2][]byte{nil, oldXMP}, [2][]byte{nil, xmpPacket("x", 6000)}},
		} {
			t.Run(f.name+"/"+c.name, func(t *testing.T) {
				with := written(t, planMetadata(t, f.data, c.old[0], c.old[1]))
				if !bytes.Contains(with, []byte(secret)) {
					t.Fatal("the old value isn't in the file to begin with")
				}
				p := planMetadata(t, with, c.new[0], c.new[1])
				checkPlan(t, with, p)
				out := written(t, p)
				if bytes.Contains(out, []byte(secret)) {
					t.Errorf("the old value is still in the file (InPlace %v)", p.InPlace)
				}
			})
		}
	}
}

// imageItems is the data of a HEIF file's items other than its Exif and
// mime (XMP and the like) items, by id; nil for a file that isn't HEIF.
func imageItems(data []byte) map[uint32][]byte {
	f, err := parseHEIF(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil
	}
	out := map[uint32][]byte{}
	for _, it := range f.m.Loc.Items {
		if t := f.m.ItemTypes[it.ID]; t == bmff.T("Exif") || t == bmff.T("mime") {
			continue
		}
		if b, err := f.itemBytes(bytes.NewReader(data), int64(len(data)), it.ID); err == nil {
			out[it.ID] = b
		}
	}
	return out
}

// itemSeeds adds the sample layouts with Exif and XMP items already in
// them, fitting and not, so the fuzzer starts from files to replace in.
func itemSeeds(f *testing.F) {
	for _, file := range planFiles(f) {
		for _, e := range metadataEdits {
			var p *Plan
			var err error
			if e.exif != nil {
				p, err = PlanExif(bytes.NewReader(file.data), int64(len(file.data)), e.exif)
			} else {
				p, err = PlanXMP(bytes.NewReader(file.data), int64(len(file.data)), e.xmp)
			}
			if err != nil {
				f.Fatal(err)
			}
			var out bytes.Buffer
			if _, err := p.WriteTo(&out); err != nil {
				f.Fatal(err)
			}
			f.Add(out.Bytes())
		}
	}
}

// Every Exif and XMP item write that succeeds keeps the plan's invariants,
// writes a file that reads back with the new value and the same
// orientation, leaves the other items' data as it was, and is in place
// when written again.
func FuzzPlanItems(f *testing.F) {
	seeds(f)
	itemSeeds(f)
	imageIOSeeds(f)
	exif, xmp, long := photoTIFF(binary.LittleEndian), xmpPacket("Harbour at dusk", 0), xmpPacket("Harbour at dawn", 3000)
	f.Fuzz(func(t *testing.T, data []byte) {
		withinDeadline(t, func() {
			if _, err := parseHEIF(bytes.NewReader(data), int64(len(data))); err != nil {
				return // the JPEG, PNG and WebP writes have fuzz targets of their own
			}
			before := imageItems(data)
			o, oerr := ReadOrientation(bytes.NewReader(data), int64(len(data)))
			for _, c := range []struct {
				name string
				plan func(r *bytes.Reader, n int64) (*Plan, error)
				back func(t *testing.T, out []byte)
			}{
				{"exif", func(r *bytes.Reader, n int64) (*Plan, error) { return PlanExif(r, n, exif) }, func(t *testing.T, out []byte) {
					// The HEIF item, as read: ReadExif tries the other
					// formats first, and a mutated file may pass for one.
					if x, err := readHEIFExif(bytes.NewReader(out), int64(len(out))); err != nil || x.DateTimeOriginal.IsZero() || x.GPS == nil {
						t.Errorf("the Exif written reads back as %+v, %v", x, err)
					}
				}},
				{"exif edit", func(r *bytes.Reader, n int64) (*Plan, error) { return planExifEdit(r, n, ExifEdit{RemoveGPS: true}) }, func(t *testing.T, out []byte) {
					if x, err := readHEIFExif(bytes.NewReader(out), int64(len(out))); err != nil || x.GPS != nil {
						t.Errorf("the GPS position removed reads back as %+v, %v", x, err)
					}
				}},
				{"xmp", func(r *bytes.Reader, n int64) (*Plan, error) { return PlanXMP(r, n, xmp) }, xmpBack(xmp)},
				{"xmp long", func(r *bytes.Reader, n int64) (*Plan, error) { return PlanXMP(r, n, long) }, xmpBack(long)},
			} {
				p, err := c.plan(bytes.NewReader(data), int64(len(data)))
				if err != nil {
					continue
				}
				checkPlan(t, data, p)
				var buf bytes.Buffer
				if _, err := p.WriteTo(&buf); err != nil {
					t.Fatalf("%s: %v", c.name, err)
				}
				out := buf.Bytes()
				c.back(t, out)
				after := imageItems(out)
				for id, b := range before {
					if !bytes.Equal(after[id], b) {
						t.Errorf("%s: item %d's data changed", c.name, id)
					}
				}
				if oerr == nil {
					if got, err := ReadOrientation(bytes.NewReader(out), int64(len(out))); err != nil || got != o {
						t.Errorf("%s: orientation %d, %v; was %d", c.name, got, err, o)
					}
				}
				again, err := c.plan(bytes.NewReader(out), int64(len(out)))
				if err != nil {
					t.Errorf("%s: the written file can't be written again: %v", c.name, err)
				} else if !again.InPlace {
					t.Errorf("%s: writing the same value again isn't in place", c.name)
				}
			}
		})
	})
}

func xmpBack(want []byte) func(t *testing.T, out []byte) {
	return func(t *testing.T, out []byte) {
		if got, err := ReadXMP(bytes.NewReader(out), int64(len(out))); err != nil || !bytes.Equal(got, want) {
			t.Errorf("the XMP written reads back as %d bytes, %v", len(got), err)
		}
	}
}

// ExifTool validates every file the item writes make with no warning its
// source didn't have: an item added to a file without one, replaced in
// place, and moved to the end, Exif and XMP, on every layout and sample
// file. (Two synthetic layouts list their items out of order; that
// warning comes with them.)
func TestMetadataItemsValidateWithExifTool(t *testing.T) {
	tool, err := exec.LookPath("exiftool")
	if err != nil {
		t.Skip("exiftool not installed")
	}
	dir := t.TempDir()
	type writeOf struct{ source, name string }
	files := map[string]writeOf{}
	add := func(source, name string, b []byte) {
		file := fmt.Sprintf("%03d.heic", len(files))
		files[file] = writeOf{source, name}
		if err := os.WriteFile(filepath.Join(dir, file), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range planFiles(t) {
		add(f.name, "", f.data)
		added := written(t, planMetadata(t, f.data, photoTIFF(binary.LittleEndian), nil))
		added = written(t, planMetadata(t, added, nil, xmpPacket("Harbour at dusk", 0)))
		add(f.name, "added", added)
		add(f.name, "in place", written(t, planMetadata(t, written(t, planMetadata(t, added, photoTIFF(binary.BigEndian), nil)), nil, xmpPacket("Harbour", 0))))
		add(f.name, "moved", written(t, planMetadata(t, written(t, planMetadata(t, added, paddedTIFF(4000), nil)), nil, xmpPacket("Harbour at dawn", 6000))))
	}
	got, err := exec.Command(tool, "-j", "-q", "-validate", "-warning", "-error", "-a", dir).Output()
	if err != nil {
		t.Fatalf("exiftool: %v", err)
	}
	var results []map[string]any
	if err := json.Unmarshal(got, &results); err != nil {
		t.Fatal(err)
	}
	if len(results) != len(files) {
		t.Fatalf("exiftool read %d files, want %d", len(results), len(files))
	}
	// What ExifTool says, by source and write ("" the source itself).
	said := map[writeOf]string{}
	for _, r := range results {
		w := files[filepath.Base(fmt.Sprint(r["SourceFile"]))]
		said[w] = fmt.Sprint(r["Validate"], r["Warning"], r["Error"])
	}
	for w, s := range said {
		if w.name != "" && s != said[writeOf{w.source, ""}] {
			t.Errorf("%s/%s: ExifTool says %q, of the source %q", w.source, w.name, s, said[writeOf{w.source, ""}])
		}
	}
}

// A file whose last box runs to the end of the file (size 0 in its header)
// gets that box's size written when an item's data is appended after it:
// the new mdat is a box of its own, the item reads back, and writing the
// same value again is in place.
func TestItemAfterOpenEndedBox(t *testing.T) {
	for _, f := range planFiles(t) {
		boxes, err := bmff.ReadBoxes(bytes.NewReader(f.data), 0, int64(len(f.data)))
		if err != nil {
			t.Fatal(err)
		}
		last := boxes[len(boxes)-1]
		if last.Type == bmff.T("meta") || last.HeaderLen != 8 {
			continue
		}
		file := bytes.Clone(f.data)
		binary.BigEndian.PutUint32(file[last.Offset:], 0)
		t.Run(f.name, func(t *testing.T) {
			packet := xmpPacket("Harbour at dawn", 6000)
			p := planMetadata(t, file, nil, packet)
			checkPlan(t, file, p)
			out := written(t, p)
			got, err := bmff.ReadBoxes(bytes.NewReader(out), 0, int64(len(out)))
			if err != nil {
				t.Fatal(err)
			}
			if n := len(got); n != len(boxes)+1 || got[n-1].Type != bmff.T("mdat") || got[n-2].Size != last.Size {
				t.Fatalf("boxes after the write: %+v", got)
			}
			if x, err := ReadXMP(bytes.NewReader(out), int64(len(out))); err != nil || !bytes.Equal(x, packet) {
				t.Errorf("the XMP reads back as %d bytes, %v", len(x), err)
			}
			if again := planMetadata(t, out, nil, packet); !again.InPlace {
				t.Error("writing the same value again isn't in place")
			}
		})
	}
}

// An item write that appends data is refused where another item's data
// would take it in: an extent that runs to the end of the file, or past
// it.
func TestItemAppendRefusedPastOpenEndedItem(t *testing.T) {
	o := testbuild.Default()
	src, _ := testbuild.Build(o)
	for _, c := range []struct {
		name   string
		length func(size uint64, e bmff.Extent) uint64
		want   error
	}{
		{"to the end", func(uint64, bmff.Extent) uint64 { return 0 }, ErrUnsupported},
		{"past the end", func(size uint64, e bmff.Extent) uint64 { return size }, ErrMalformed},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, err := parseHEIF(bytes.NewReader(src), int64(len(src)))
			if err != nil {
				t.Fatal(err)
			}
			exif, _, _ := f.metadataItem(kindExif)
			changed := false
			for i := range f.m.Loc.Items {
				it := &f.m.Loc.Items[i]
				if it.ID != exif && it.ConstructionMethod == 0 && len(it.Extents) == 1 && !changed {
					it.Extents[0].Length = c.length(uint64(len(src)), it.Extents[0])
					changed = true
				}
			}
			meta, err := f.m.Encode()
			if err != nil || int64(len(meta)) != f.meta.Size {
				t.Fatalf("re-encoding the meta box: %d bytes, %v", len(meta), err)
			}
			file := append(append(bytes.Clone(src[:f.meta.Offset]), meta...), src[f.meta.End():]...)
			long := xmpPacket("Harbour at dawn", 6000)
			if _, err := PlanXMP(bytes.NewReader(file), int64(len(file)), long); !errors.Is(err, c.want) {
				t.Errorf("an appending write: %v, want %v", err, c.want)
			}
		})
	}
}
