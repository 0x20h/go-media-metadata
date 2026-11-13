package mediameta

import (
	"bytes"
	"crypto/md5"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func setJPEGXMP(t *testing.T, file, packet []byte) ([]byte, *Plan) {
	t.Helper()
	p, err := PlanXMP(bytes.NewReader(file), int64(len(file)), packet)
	if err != nil {
		t.Fatalf("PlanXMP: %v", err)
	}
	checkPlan(t, file, p)
	var out bytes.Buffer
	if err := SetXMP(bytes.NewReader(file), int64(len(file)), &out, packet); err != nil {
		t.Fatalf("SetXMP: %v", err)
	}
	checkJPEGEdit(t, file, out.Bytes())
	return out.Bytes(), p
}

func readJPEGXMPOf(t *testing.T, file []byte) []byte {
	t.Helper()
	b, err := ReadXMP(bytes.NewReader(file), int64(len(file)))
	if err != nil {
		t.Fatalf("ReadXMP: %v", err)
	}
	return b
}

// xmpSegmentsOf are the APP1 bodies of a file's XMP segments, standard
// ones and extended ones.
func xmpSegmentsOf(t *testing.T, file []byte) (standard, extended [][]byte) {
	t.Helper()
	f, err := parseJPEG(bytes.NewReader(file), int64(len(file)))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range f.segs {
		body := file[s.body : s.body+s.bodyLen]
		switch {
		case s.marker == markerAPP1 && bytes.HasPrefix(body, xmpHeader):
			standard = append(standard, body)
		case s.marker == markerAPP1 && bytes.HasPrefix(body, xmpExtHeader):
			extended = append(extended, body)
		}
	}
	return standard, extended
}

// A packet into a JPEG without XMP: one segment after the Exif one, read
// back as written.
func TestSetXMPJPEG(t *testing.T) {
	src := withSegments(plainJPEG(t), exifSegment(photoTIFF(binaryBE)))
	packet := editXMP(t, nil, XMPEdit{Title: ptr("Lake"), Keywords: &[]string{"a", "b"}, Rating: ptr(4)})
	out, plan := setJPEGXMP(t, src, packet)
	if plan.InPlace {
		t.Error("adding a segment is in place")
	}
	if got := readJPEGXMPOf(t, out); !bytes.Equal(got, packet) {
		t.Fatalf("read back %q", got)
	}
	exif := bytes.Index(out, exifHeader)
	xmp := bytes.Index(out, xmpHeader)
	if exif < 0 || xmp < exif {
		t.Errorf("XMP at %d, Exif at %d: want XMP after Exif", xmp, exif)
	}
	if x := readExif(t, out); x.Orientation != 6 {
		t.Error("the Exif changed")
	}
}

// An edit that keeps the packet's length (EditXMP keeps it where the
// padding allows), or shortens it, is in place: the segment's bytes only.
func TestSetXMPJPEGInPlace(t *testing.T) {
	first, _ := setJPEGXMP(t, plainJPEG(t), editXMP(t, nil, XMPEdit{Title: ptr("Lake")}))
	old := readJPEGXMPOf(t, first)
	for name, packet := range map[string][]byte{
		"same length": editXMP(t, old, XMPEdit{Title: ptr("Snow"), Rating: ptr(2)}),
		"shorter":     []byte(`<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#"/></x:xmpmeta>`),
	} {
		out, plan := setJPEGXMP(t, first, packet)
		if !plan.InPlace || len(out) != len(first) {
			t.Fatalf("%s: in place %v, %d → %d bytes", name, plan.InPlace, len(first), len(out))
		}
		std, _ := xmpSegmentsOf(t, first)
		start := int64(bytes.Index(first, std[0]))
		for _, p := range plan.Patches {
			if p.Offset < start || p.Offset+int64(len(p.Data)) > start+int64(len(std[0])) {
				t.Errorf("%s: patch at %d outside the XMP segment", name, p.Offset)
			}
		}
		got, err := ParseXMP(readJPEGXMPOf(t, out))
		want, _ := ParseXMP(packet)
		if err != nil || got.Title != want.Title || got.Rating != want.Rating {
			t.Errorf("%s: read %+v (%v), want %+v", name, got, err, want)
		}
	}
	// Longer than the segment: replaced, and nothing of the old packet
	// stays in the file.
	long := editXMP(t, old, XMPEdit{Description: ptr(strings.Repeat("long ", 2000))})
	out, plan := setJPEGXMP(t, first, long)
	if plan.InPlace || bytes.Contains(out, []byte(">Lake<")) && !bytes.Contains(long, []byte(">Lake<")) {
		t.Errorf("in place %v", plan.InPlace)
	}
	if std, _ := xmpSegmentsOf(t, out); len(std) != 1 {
		t.Errorf("%d standard XMP segments, want 1", len(std))
	}
}

// A packet past one segment is split: the standard part names the
// extended part by its MD5, which follows in chunks; read back, the two
// are one packet again, and ExifTool reads it the same.
func TestSetXMPJPEGExtended(t *testing.T) {
	big := strings.Repeat("A long description. ", 9000) // 180 KB
	packet := editXMP(t, nil, XMPEdit{Title: ptr("Big"), Description: &big, Keywords: &[]string{"k"}, Rating: ptr(5)})
	out, _ := setJPEGXMP(t, plainJPEG(t), packet)
	std, ext := xmpSegmentsOf(t, out)
	if len(std) != 1 || len(ext) < 3 {
		t.Fatalf("%d standard and %d extended segments", len(std), len(ext))
	}
	sd, err := parseXMPDoc(std[0][len(xmpHeader):])
	if err != nil {
		t.Fatal(err)
	}
	guid := sd.extendedGUID()
	var whole []byte
	for _, b := range ext {
		h := len(xmpExtHeader)
		if string(b[h:h+32]) != guid {
			t.Fatalf("chunk GUID %q, want %q", b[h:h+32], guid)
		}
		if off := binary.BigEndian.Uint32(b[h+36:]); int(off) != len(whole) {
			t.Fatalf("chunk at %d, want %d", off, len(whole))
		}
		whole = append(whole, b[xmpExtHead:]...)
	}
	if sum := md5.Sum(whole); strings.ToUpper(hex.EncodeToString(sum[:])) != guid {
		t.Error("the GUID isn't the extended part's MD5")
	}
	x, err := ParseXMP(readJPEGXMPOf(t, out))
	if err != nil || x.Title != "Big" || x.Description != big || x.Rating != 5 || len(x.Keywords) != 1 {
		t.Fatalf("read back %v: title %q, %d-byte description, rating %d", err, x.Title, len(x.Description), x.Rating)
	}
	if bytes.Contains(readJPEGXMPOf(t, out), []byte("HasExtendedXMP")) {
		t.Error("the merged packet still names the extended part")
	}

	// ExifTool reads the split packet as one.
	if tool, err := exec.LookPath("exiftool"); err == nil {
		path := filepath.Join(t.TempDir(), "extended.jpg")
		if err := os.WriteFile(path, out, 0o644); err != nil {
			t.Fatal(err)
		}
		j, err := exec.Command(tool, "-j", "-n", "-q", "-XMP:Title", "-XMP:Description", "-XMP:Rating", "-warning", path).Output()
		if err != nil {
			t.Fatal(err)
		}
		var got []map[string]any
		if err := json.Unmarshal(j, &got); err != nil || len(got) != 1 {
			t.Fatalf("exiftool: %v %s", err, j)
		}
		if got[0]["Title"] != "Big" || got[0]["Description"] != big || got[0]["Rating"] != 5.0 || got[0]["Warning"] != nil {
			t.Errorf("ExifTool reads title %v, a %d-byte description, rating %v, warning %v",
				got[0]["Title"], len(str(got[0]["Description"])), got[0]["Rating"], got[0]["Warning"])
		}
	}

	// A small packet over it: one segment, the extended ones gone.
	small, plan := setJPEGXMP(t, out, editXMP(t, nil, XMPEdit{Title: ptr("Small")}))
	if std, ext := xmpSegmentsOf(t, small); len(std) != 1 || len(ext) != 0 || plan.InPlace {
		t.Errorf("%d standard, %d extended segments, in place %v", len(std), len(ext), plan.InPlace)
	}
	if bytes.Contains(small, []byte("A long description")) {
		t.Error("the old extended part is still in the file")
	}
	// Writing the merged packet back splits it again, the same way.
	again, _ := setJPEGXMP(t, out, readJPEGXMPOf(t, out))
	if x, _ := ParseXMP(readJPEGXMPOf(t, again)); x.Description != big {
		t.Error("a round trip lost the description")
	}
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

// An extended part that's incomplete, or named by another GUID, isn't
// read: the standard packet comes back as it is.
func TestReadXMPJPEGIncompleteExtended(t *testing.T) {
	big := strings.Repeat("x", 150000)
	out, _ := setJPEGXMP(t, plainJPEG(t), editXMP(t, nil, XMPEdit{Title: ptr("T"), Description: &big}))
	_, ext := xmpSegmentsOf(t, out)
	// Drop the second chunk's segment.
	at := bytes.Index(out, ext[1]) - 4
	cut := append(append([]byte(nil), out[:at]...), out[at+4+len(ext[1]):]...)
	x, err := ParseXMP(readJPEGXMPOf(t, cut))
	if err != nil || x.Title != "T" || x.Description != "" {
		t.Errorf("read %+v (%v): want the standard part only", x, err)
	}
}

func TestReadXMPJPEGWithoutXMP(t *testing.T) {
	if b := readJPEGXMPOf(t, plainJPEG(t)); b != nil {
		t.Errorf("got %q", b)
	}
}

func FuzzJPEGXMP(f *testing.F) {
	jpegSeeds(f)
	small := editXMP(&testing.T{}, nil, XMPEdit{Title: ptr("T")})
	withXMP, _ := setJPEGXMP(&testing.T{}, plainJPEG(f), small)
	f.Add(withXMP)
	f.Fuzz(func(t *testing.T, data []byte) {
		withinDeadline(t, func() {
			if _, err := ReadXMP(bytes.NewReader(data), int64(len(data))); err != nil {
				return
			}
			for _, packet := range [][]byte{small, editXMP(t, nil, XMPEdit{Description: ptr(strings.Repeat("d", 70000))})} {
				p, err := PlanXMP(bytes.NewReader(data), int64(len(data)), packet)
				if err != nil {
					continue
				}
				checkPlan(t, data, p)
				var out bytes.Buffer
				if _, err := p.WriteTo(&out); err != nil {
					t.Fatal(err)
				}
				got, err := ReadXMP(bytes.NewReader(out.Bytes()), int64(out.Len()))
				if err != nil {
					t.Fatalf("ReadXMP of what PlanXMP wrote: %v", err)
				}
				want, _ := ParseXMP(packet)
				if x, err := ParseXMP(got); err != nil || x.Title != want.Title || x.Description != want.Description {
					t.Fatalf("read back %+v (%v)", x, err)
				}
			}
		})
	})
}

var binaryBE = binary.BigEndian
