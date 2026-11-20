package mediameta

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// aviEditDate is the date the AVI edit tests set: a zone of its own, so
// the clock time written is the date's own.
var aviEditDate = time.Date(2014, 3, 14, 5, 6, 7, 0, time.FixedZone("", 3600))

// planAVIDate plans aviEditDate on file and writes the result.
func planAVIDate(t testing.TB, file []byte) (*Plan, []byte, error) {
	t.Helper()
	p, err := PlanVideoMetadata(bytes.NewReader(file), int64(len(file)), VideoEdit{Created: &aviEditDate})
	if err != nil {
		return nil, nil, err
	}
	var out bytes.Buffer
	if _, err := p.WriteTo(&out); err != nil {
		t.Fatal(err)
	}
	if !p.InPlace || int64(out.Len()) != p.Size {
		t.Fatalf("not in place: %+v", p)
	}
	applied := append([]byte(nil), file...)
	for _, pt := range p.Patches {
		copy(applied[pt.Offset:], pt.Data)
	}
	if !bytes.Equal(applied, out.Bytes()) {
		t.Fatal("the patches and WriteTo differ")
	}
	return p, out.Bytes(), nil
}

// The AVI corpus with dates: after the edit ExifTool reads the new date
// in every field the file had, ReadVideo reads it, and every decoded
// frame is the same.
func TestAVIEditAgreesWithExifTool(t *testing.T) {
	dir := t.TempDir()
	var edited []string
	for _, name := range []string{"idit", "icrd", "nikon"} {
		src := "testdata/avi/" + name + ".avi"
		file := readFile(t, src)
		_, out, err := planAVIDate(t, file)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		v, err := ReadVideo(bytes.NewReader(out), int64(len(out)))
		want := time.Date(2014, 3, 14, 5, 6, 7, 0, time.UTC)
		if name == "icrd" {
			want = time.Date(2014, 3, 14, 0, 0, 0, 0, time.UTC)
		}
		if err != nil || !v.Created.Equal(want) || v.OffsetKnown {
			t.Errorf("%s: read back %v (%v), want %v", name, v.Created, err, want)
		}
		before, _ := ReadVideo(bytes.NewReader(file), int64(len(file)))
		before.Created, v.Created = time.Time{}, time.Time{}
		if !jsonEqual(before, v) {
			t.Errorf("%s: more than the date changed:\n%+v\n%+v", name, before, v)
		}
		p := filepath.Join(dir, name+".avi")
		if err := os.WriteFile(p, out, 0o644); err != nil {
			t.Fatal(err)
		}
		edited = append(edited, p)
		if _, err := exec.LookPath("ffmpeg"); err == nil {
			a := aviFrameMD5(t, src)
			if b := aviFrameMD5(t, p); a == "" || a != b {
				t.Errorf("%s: decoded frames differ (or none)", name)
			}
		}
	}
	tool, err := exec.LookPath("exiftool")
	if err != nil {
		t.Skip("exiftool not installed")
	}
	out, err := exec.Command(tool, append([]string{"-j", "-q", "-G1", "-time:all", "--System:all"}, edited...)...).Output()
	if err != nil {
		t.Fatal(err)
	}
	var got []map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	wantTags := map[string][]string{
		"idit.avi":  {"RIFF:DateTimeOriginal"},
		"icrd.avi":  {"RIFF:DateCreated"},
		"nikon.avi": {"Nikon:DateTimeOriginal", "Nikon:CreateDate"},
	}
	for _, e := range got {
		name := filepath.Base(e["SourceFile"].(string))
		tags := wantTags[name]
		if len(e) != len(tags)+1 {
			t.Errorf("%s: ExifTool's dates %v, want only %v", name, e, tags)
		}
		for _, tag := range tags {
			want := "2014:03:14 05:06:07"
			if name == "icrd.avi" {
				want = "2014:03:14"
			}
			if e[tag] != want {
				t.Errorf("%s: ExifTool's %s %v, want %s", name, tag, e[tag], want)
			}
		}
	}
}

func aviFrameMD5(t *testing.T, p string) string {
	t.Helper()
	out, err := exec.Command("ffmpeg", "-v", "error", "-i", p, "-map", "0", "-f", "framemd5", "-").Output()
	if err != nil {
		t.Fatalf("ffmpeg framemd5 %s: %v", p, err)
	}
	return string(out)
}

func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

// rebuildAVIHdrlTail is idit.avi with the end of its header list, from
// its JUNK chunk on, replaced by tail, which must be as long.
func rebuildAVIHdrlTail(t *testing.T, tail []byte) []byte {
	t.Helper()
	file := append([]byte(nil), readFile(t, "testdata/avi/idit.avi")...)
	r := bytes.NewReader(file)
	top, err := aviChunks(r, 12, int64(len(file)))
	if err != nil || top[0].id != "hdrl" {
		t.Fatalf("idit.avi: %v %+v", err, top)
	}
	kids, _ := aviChunks(r, top[0].at, top[0].at+top[0].n)
	var from int64 = -1
	for _, k := range kids {
		if k.id == "JUNK" {
			from = k.at - 8
		}
	}
	end := top[0].at + top[0].n
	if from < 0 || end-from != int64(len(tail)) {
		t.Fatalf("idit.avi: hdrl tail of %d bytes, want %d", end-from, len(tail))
	}
	copy(file[from:], tail)
	return file
}

func aviTestChunk(id string, data []byte, size int) []byte {
	b := binary.LittleEndian.AppendUint32([]byte(id), uint32(size))
	b = append(b, data...)
	return append(b, make([]byte, size+size&1-len(data))...)
}

// A date that grows (an asctime date's day of one digit, unpadded, to
// two) takes its bytes (one, and a pad byte) from the JUNK chunk right
// after it; without one,
// the edit is refused with ErrNoRoom.
func TestAVIEditGrowsIntoJUNK(t *testing.T) {
	idit := aviTestChunk("IDIT", []byte("Tue Jun 7 08:09:10 2005\n"), 24) // 32 bytes, no NUL
	junk := aviTestChunk("JUNK", nil, 262)                                // 270 bytes
	file := rebuildAVIHdrlTail(t, append(append([]byte(nil), idit...), junk...))
	if v, err := ReadVideo(bytes.NewReader(file), int64(len(file))); err != nil || v.Created != time.Date(2005, 6, 7, 8, 9, 10, 0, time.UTC) {
		t.Fatalf("the test file reads %v, %v", v.Created, err)
	}
	p, out, err := planAVIDate(t, file)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Patches) != 1 {
		t.Errorf("%d patches", len(p.Patches))
	}
	at := bytes.Index(out, []byte("IDIT"))
	if want := aviTestChunk("IDIT", []byte("Fri Mar 14 05:06:07 2014\n\x00"), 26); !bytes.Equal(out[at:at+len(want)], want) {
		t.Errorf("IDIT %q", out[at:at+len(want)])
	}
	if j := out[at+34 : at+42]; string(j[:4]) != "JUNK" || binary.LittleEndian.Uint32(j[4:]) != 260 {
		t.Errorf("JUNK header %q", j)
	}
	if v, err := ReadVideo(bytes.NewReader(out), int64(len(out))); err != nil || v.Created != time.Date(2014, 3, 14, 5, 6, 7, 0, time.UTC) {
		t.Errorf("read back %v, %v", v.Created, err)
	}

	// The JUNK chunk before the date: no room.
	file = rebuildAVIHdrlTail(t, append(append([]byte(nil), junk...), idit...))
	if _, _, err := planAVIDate(t, file); !errors.Is(err, ErrNoRoom) || !errors.Is(err, ErrUnsupported) {
		t.Errorf("no room: %v, want ErrNoRoom", err)
	}
}

// The refusals: a file without a date field (none is added), a field
// other than the date, and a Nikon date record too short for the date.
func TestAVIEditRefusals(t *testing.T) {
	plain := readFile(t, "testdata/avi/plain.avi")
	if _, _, err := planAVIDate(t, plain); !errors.Is(err, ErrNoDateField) || !errors.Is(err, ErrUnsupported) {
		t.Errorf("plain.avi: %v, want ErrNoDateField", err)
	}
	idit := readFile(t, "testdata/avi/idit.avi")
	title := "Lake"
	for _, e := range []VideoEdit{{Title: &title}, {GPS: &GPS{Latitude: 1, Longitude: 2}}, {Created: &aviEditDate, XMP: &XMPEdit{}}} {
		if _, err := PlanVideoMetadata(bytes.NewReader(idit), int64(len(idit)), e); !errors.Is(err, ErrUnsupported) {
			t.Errorf("%+v: %v, want ErrUnsupported", e, err)
		}
	}
	// No change: an empty plan.
	if p, err := PlanVideoMetadata(bytes.NewReader(idit), int64(len(idit)), VideoEdit{}); err != nil || !p.InPlace || len(p.Patches) != 0 {
		t.Errorf("empty edit: %+v, %v", p, err)
	}
	// Nikon's DateTimeOriginal record cut to 10 bytes (its size, and a
	// record of 10 bytes after it keeping the chunk filled).
	nikon := append([]byte(nil), readFile(t, "testdata/avi/nikon.avi")...)
	at := bytes.Index(nikon, []byte("\x13\x00\x14\x002013:05:06 07:08:09"))
	if at < 0 {
		t.Fatal("nikon.avi: no DateTimeOriginal record")
	}
	copy(nikon[at:], []byte("\x13\x00\x0a\x002013:05:06\x55\x00\x06\x00\x00\x00\x00\x00\x00\x00"))
	if _, _, err := planAVIDate(t, nikon); !errors.Is(err, ErrNoRoom) {
		t.Errorf("short Nikon date: %v, want ErrNoRoom", err)
	}
}

// An OpenDML layout: the stream's super index (indx) where ffmpeg leaves
// its JUNK. The edit doesn't touch it, nor take room from it.
func TestAVIEditOpenDML(t *testing.T) {
	file := append([]byte(nil), readFile(t, "testdata/avi/idit.avi")...)
	at := bytes.Index(file, []byte("JUNK\x18\x10\x00\x00")) // the strl JUNK, 4120 bytes
	if at < 0 {
		t.Fatal("idit.avi: no strl JUNK")
	}
	copy(file[at:], "indx")
	// wLongsPerEntry 4, an index of chunks (bIndexType 0), one entry
	// pointing at the movi list, absolute.
	binary.LittleEndian.PutUint16(file[at+8:], 4)
	binary.LittleEndian.PutUint32(file[at+12:], 1)
	copy(file[at+16:], "00dc")
	movi := bytes.Index(file, []byte("movi"))
	binary.LittleEndian.PutUint64(file[at+32:], uint64(movi-8))
	p, _, err := planAVIDate(t, file)
	if err != nil {
		t.Fatal(err)
	}
	for _, pt := range p.Patches {
		if pt.Offset < int64(at)+8+4120 && pt.Offset+int64(len(pt.Data)) > int64(at) {
			t.Errorf("a patch at %d over the indx chunk", pt.Offset)
		}
	}
}

func FuzzAVIEdit(f *testing.F) {
	paths, _ := filepath.Glob("testdata/avi/*.avi")
	for _, p := range paths {
		f.Add(readFile(f, p))
	}
	f.Fuzz(func(t *testing.T, file []byte) {
		before, _ := ReadVideo(bytes.NewReader(file), int64(len(file)))
		p, out, err := planAVIDate(t, file)
		if err != nil {
			if !errors.Is(err, ErrUnsupported) && !errors.Is(err, ErrMalformed) && !errors.Is(err, ErrUnknownFormat) {
				t.Fatalf("an error of no kind: %v", err)
			}
			return
		}
		if len(p.Patches) == 0 {
			return
		}
		v, err := ReadVideo(bytes.NewReader(out), int64(len(out)))
		if err != nil {
			t.Fatalf("unreadable after the edit: %v", err)
		}
		if (!before.Created.IsZero() || !v.Created.IsZero()) &&
			!v.Created.Equal(time.Date(2014, 3, 14, 5, 6, 7, 0, time.UTC)) && !v.Created.Equal(time.Date(2014, 3, 14, 0, 0, 0, 0, time.UTC)) {
			t.Fatalf("read back %v", v.Created)
		}
	})
}

func BenchmarkPlanVideoEditAVI(b *testing.B) {
	file := readFile(b, "testdata/avi/nikon.avi")
	cr := &countingReader{r: bytes.NewReader(file)}
	edit := VideoEdit{Created: &aviEditDate}
	b.ReportAllocs()
	for range b.N {
		if _, err := PlanVideoMetadata(cr, int64(len(file)), edit); err != nil {
			b.Fatal(err)
		}
	}
	reportReads(b, cr)
}
