package mediameta

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/0x20h/go-media-metadata/internal/bmff/qtbuild"
)

// motionPhoto is a synthetic Motion Photo: a JPEG with an XMP container
// directory and an MPF index, followed by a gain map JPEG and an MP4.
type motionPhoto struct {
	file            []byte
	gainMap, video  []byte
	gainAt, videoAt int64
}

type motionOpts struct {
	mpfFirst   bool // the MPF segment before the XMP one
	microVideo bool // the older GCamera:MicroVideoOffset form, no container
	noMPF      bool
}

func mpfSegment(order binary.AppendByteOrder, primarySize, gainSize, gainOffset uint32) []byte {
	t := []byte("MM\x00\x2a")
	if order == binary.LittleEndian {
		t = []byte("II\x2a\x00")
	}
	t = order.AppendUint32(t, 8)
	// Three entries: version, number of images, MPEntry.
	t = order.AppendUint16(t, 3)
	entry := func(tag, typ uint16, count uint32, v []byte) {
		t = order.AppendUint16(t, tag)
		t = order.AppendUint16(t, typ)
		t = order.AppendUint32(t, count)
		t = append(t, v...)
	}
	entry(0xB000, 7, 4, []byte("0100"))
	entry(0xB001, 4, 1, order.AppendUint32(nil, 2))
	entry(0xB002, 7, 32, order.AppendUint32(nil, 8+2+3*12+4))
	t = order.AppendUint32(t, 0) // next IFD
	t = order.AppendUint32(t, 0x030000)
	t = order.AppendUint32(t, primarySize)
	t = order.AppendUint32(t, 0)
	t = order.AppendUint32(t, 0)
	t = order.AppendUint32(t, 0)
	t = order.AppendUint32(t, gainSize)
	t = order.AppendUint32(t, gainOffset)
	t = order.AppendUint32(t, 0)
	body := append(append([]byte(nil), mpfHeader...), t...)
	seg := []byte{0xFF, markerAPP2}
	seg = binary.BigEndian.AppendUint16(seg, uint16(len(body)+2))
	return append(seg, body...)
}

func motionXMP(o motionOpts, gainLen, videoLen int) []byte {
	if o.microVideo {
		return []byte(fmt.Sprintf(`<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">`+
			`<rdf:Description rdf:about="" xmlns:GCamera="%s" GCamera:MicroVideo="1" GCamera:MicroVideoVersion="1" `+
			`GCamera:MicroVideoOffset="%d" GCamera:MicroVideoPresentationTimestampUs="250000"/></rdf:RDF></x:xmpmeta>`, nsGCamera, videoLen))
	}
	return []byte(fmt.Sprintf(`<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">`+
		`<rdf:Description rdf:about="" xmlns:GCamera="%s" xmlns:Container="%s" xmlns:Item="%s" `+
		`GCamera:MotionPhoto="1" GCamera:MotionPhotoVersion="1" GCamera:MotionPhotoPresentationTimestampUs="500000">`+
		`<Container:Directory><rdf:Seq>`+
		`<rdf:li rdf:parseType="Resource"><Container:Item Item:Mime="image/jpeg" Item:Semantic="Primary" Item:Length="0" Item:Padding="0"/></rdf:li>`+
		`<rdf:li rdf:parseType="Resource"><Container:Item Item:Mime="image/jpeg" Item:Semantic="GainMap" Item:Length="%d"/></rdf:li>`+
		`<rdf:li rdf:parseType="Resource"><Container:Item Item:Mime="video/mp4" Item:Semantic="MotionPhoto" Item:Length="%d" Item:Padding="0"/></rdf:li>`+
		`</rdf:Seq></Container:Directory></rdf:Description></rdf:RDF></x:xmpmeta>`,
		nsGCamera, nsContainer, nsItem, gainLen, videoLen))
}

func buildMotionPhoto(t testing.TB, o motionOpts) motionPhoto {
	t.Helper()
	primary := plainJPEG(t)
	gain := plainJPEG(t)
	gain[len(gain)/2] ^= 0x55 // tell it apart from the primary
	vo := qtbuild.Default()
	vo.Brand, vo.MediaSize = "mp42", 1024 // Motion Photos are MP4
	video, _, _ := qtbuild.Build(vo)
	if o.microVideo {
		gain = nil
	}
	xmp := app1(append(append([]byte(nil), xmpHeader...), motionXMP(o, len(gain), len(video))...))
	// The MPF segment's size doesn't depend on its values: place it once
	// to learn where the gain map lands, then for real.
	var mpf []byte
	build := func() []byte {
		var segs []byte
		if o.mpfFirst {
			segs = append(append(segs, mpf...), xmp...)
		} else {
			segs = append(append(segs, xmp...), mpf...)
		}
		out := append([]byte{0xFF, markerSOI}, segs...)
		return append(out, primary[2:]...)
	}
	m := motionPhoto{gainMap: gain, video: video}
	if !o.microVideo && !o.noMPF {
		mpf = mpfSegment(binary.BigEndian, 0, 0, 0)
		head := build()
		mpfAt := bytes.Index(head, mpf)
		base := int64(mpfAt + 4 + len(mpfHeader))
		m.gainAt = int64(len(head))
		mpf = mpfSegment(binary.BigEndian, uint32(len(head)), uint32(len(gain)), uint32(m.gainAt-base))
	}
	head := build()
	m.gainAt = int64(len(head))
	m.videoAt = m.gainAt + int64(len(gain))
	m.file = append(append(head, gain...), video...)
	return m
}

func checkEmbedded(t *testing.T, name string, file []byte, m motionPhoto, o motionOpts) {
	t.Helper()
	e, err := ReadEmbedded(bytes.NewReader(file), int64(len(file)))
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if e.Video == nil {
		t.Fatalf("%s: no video", name)
	}
	if got := file[e.Video.Offset : e.Video.Offset+e.Video.Length]; !bytes.Equal(got, m.video) {
		t.Errorf("%s: the video at %d (%d bytes) isn't the one embedded", name, e.Video.Offset, e.Video.Length)
	}
	wantTS := 500 * time.Millisecond
	if o.microVideo {
		wantTS = 250 * time.Millisecond
	}
	if !e.HasPresentationTimestamp || e.PresentationTimestamp != wantTS {
		t.Errorf("%s: presentation timestamp %v (%v)", name, e.PresentationTimestamp, e.HasPresentationTimestamp)
	}
	if o.microVideo {
		if len(e.Images) != 0 || e.Video.Semantic != "MicroVideo" {
			t.Errorf("%s: %+v", name, e)
		}
		return
	}
	if len(e.Images) != 1 || e.Images[0].Semantic != "GainMap" {
		t.Fatalf("%s: images %+v", name, e.Images)
	}
	if g := e.Images[0]; !bytes.Equal(file[g.Offset:g.Offset+g.Length], m.gainMap) {
		t.Errorf("%s: the gain map at %d isn't the one embedded", name, g.Offset)
	}
	if o.noMPF {
		return
	}
	// The MPF index points at the gain map, where the file has it now.
	f, err := parseJPEG(bytes.NewReader(file), int64(len(file)))
	if err != nil {
		t.Fatal(err)
	}
	x, err := f.mpf(bytes.NewReader(file))
	if err != nil || len(x.entries) != 2 {
		t.Fatalf("%s: MPF %+v, %v", name, x, err)
	}
	at := x.base + int64(x.entries[1].offset)
	if at+int64(len(m.gainMap)) > int64(len(file)) || !bytes.Equal(file[at:at+int64(len(m.gainMap))], m.gainMap) {
		t.Errorf("%s: the MPF index points at %d, not at the gain map", name, at)
	}
}

var motionVariants = []struct {
	name string
	o    motionOpts
}{
	{"XMP then MPF", motionOpts{}},
	{"MPF then XMP", motionOpts{mpfFirst: true}},
	{"no MPF", motionOpts{noMPF: true}},
	{"MicroVideo", motionOpts{microVideo: true}},
}

func TestReadEmbedded(t *testing.T) {
	for _, v := range motionVariants {
		m := buildMotionPhoto(t, v.o)
		checkEmbedded(t, v.name, m.file, m, v.o)
	}
	// A JPEG without any: nothing, no error.
	plain := plainJPEG(t)
	e, err := ReadEmbedded(bytes.NewReader(plain), int64(len(plain)))
	if err != nil || e.Video != nil || len(e.Images) != 0 {
		t.Errorf("a plain JPEG: %+v, %v", e, err)
	}
}

// Lengths that don't fit, or point at bytes that aren't a movie, aren't
// taken.
func TestReadEmbeddedBad(t *testing.T) {
	m := buildMotionPhoto(t, motionOpts{noMPF: true})
	for name, edit := range map[string]func([]byte) []byte{
		"length past the start": func(b []byte) []byte {
			// As many digits, so the segment keeps its length.
			n := len(fmt.Sprint(len(m.video)))
			return bytes.Replace(b, []byte(fmt.Sprintf(`Item:Length="%d" Item:Padding="0"/>`, len(m.video))),
				[]byte(fmt.Sprintf(`Item:Length="%s" Item:Padding="0"/>`, bytes.Repeat([]byte("9"), n))), 1)
		},
		"not a movie": func(b []byte) []byte {
			b = bytes.Clone(b)
			copy(b[len(b)-len(m.video)+4:], "xxxx")
			return b
		},
	} {
		file := edit(m.file)
		e, err := ReadEmbedded(bytes.NewReader(file), int64(len(file)))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if e.Video != nil {
			t.Errorf("%s: video %+v", name, e.Video)
		}
	}
}

// Every JPEG writer keeps the embedded media byte for byte, and the MPF
// index pointing at its images, whichever segment it grows or adds.
func TestJPEGWritesKeepEmbedded(t *testing.T) {
	long := string(bytes.Repeat([]byte("a long title "), 40))
	keywords := []string{"one", "two", "three"}
	writes := []struct {
		name string
		plan func(src []byte) (*Plan, error)
	}{
		{"orientation", func(b []byte) (*Plan, error) { return PlanOrientation(bytes.NewReader(b), int64(len(b)), 6) }},
		{"Exif date", func(b []byte) (*Plan, error) {
			d := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
			return PlanExifEdit(bytes.NewReader(b), int64(len(b)), ExifEdit{DateTimeOriginal: &d, GPS: &GPS{Latitude: 1, Longitude: 2}})
		}},
		{"XMP title", func(b []byte) (*Plan, error) {
			return PlanXMPEdit(bytes.NewReader(b), int64(len(b)), XMPEdit{Title: &long, Keywords: &keywords})
		}},
		{"IPTC", func(b []byte) (*Plan, error) {
			return PlanMetadata(bytes.NewReader(b), int64(len(b)), MetadataEdit{IPTC: &IPTCEdit{Title: &long, Keywords: &keywords}})
		}},
		{"all", func(b []byte) (*Plan, error) {
			d := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
			return PlanMetadata(bytes.NewReader(b), int64(len(b)), MetadataEdit{
				Exif: &ExifEdit{DateTimeOriginal: &d},
				XMP:  &XMPEdit{Title: &long},
				IPTC: &IPTCEdit{Keywords: &keywords},
			})
		}},
	}
	for _, v := range motionVariants {
		m := buildMotionPhoto(t, v.o)
		for _, w := range writes {
			name := v.name + "/" + w.name
			p, err := w.plan(m.file)
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			checkPlan(t, m.file, p)
			var out bytes.Buffer
			if _, err := p.WriteTo(&out); err != nil {
				t.Fatal(err)
			}
			checkEmbedded(t, name, out.Bytes(), m, v.o)
		}
	}
}

func FuzzReadEmbedded(f *testing.F) {
	for _, v := range motionVariants {
		f.Add(buildMotionPhoto(f, v.o).file)
	}
	long := string(bytes.Repeat([]byte("x"), 300))
	f.Fuzz(func(t *testing.T, data []byte) {
		withinDeadline(t, func() {
			e, err := ReadEmbedded(bytes.NewReader(data), int64(len(data)))
			if err != nil {
				return
			}
			all := append([]EmbeddedMedia(nil), e.Images...)
			if e.Video != nil {
				all = append(all, *e.Video)
			}
			for _, m := range all {
				if m.Offset < 0 || m.Length <= 0 || m.Offset+m.Length > int64(len(data)) {
					t.Fatalf("media %+v outside the file of %d bytes", m, len(data))
				}
			}
			// A write that grows the XMP keeps every part.
			p, err := PlanXMPEdit(bytes.NewReader(data), int64(len(data)), XMPEdit{Title: &long})
			if err != nil {
				return
			}
			var out bytes.Buffer
			if _, err := p.WriteTo(&out); err != nil {
				t.Fatal(err)
			}
			e2, err := ReadEmbedded(bytes.NewReader(out.Bytes()), int64(out.Len()))
			if err != nil {
				t.Fatalf("the written file doesn't read: %v", err)
			}
			if e.Video != nil {
				v := e.Video
				if e2.Video == nil {
					t.Fatal("the video is gone")
				}
				if !bytes.Equal(out.Bytes()[e2.Video.Offset:e2.Video.Offset+e2.Video.Length], data[v.Offset:v.Offset+v.Length]) {
					t.Fatal("the video changed")
				}
			}
		})
	})
}

// ExifTool extracts the same video and gain map, before and after every
// write. Runs where it's installed.
func TestEmbeddedExifTool(t *testing.T) {
	if _, err := exec.LookPath("exiftool"); err != nil {
		t.Skip("exiftool not installed")
	}
	dir := t.TempDir()
	long := string(bytes.Repeat([]byte("a long title "), 40))
	keywords := []string{"one"}
	for i, v := range motionVariants {
		m := buildMotionPhoto(t, v.o)
		p, err := PlanMetadata(bytes.NewReader(m.file), int64(len(m.file)), MetadataEdit{XMP: &XMPEdit{Title: &long}, IPTC: &IPTCEdit{Keywords: &keywords}})
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if _, err := p.WriteTo(&out); err != nil {
			t.Fatal(err)
		}
		for j, file := range [][]byte{m.file, out.Bytes()} {
			path := filepath.Join(dir, fmt.Sprintf("%d-%d.jpg", i, j))
			if err := os.WriteFile(path, file, 0o600); err != nil {
				t.Fatal(err)
			}
			tags := map[string][]byte{"MotionPhotoVideo": m.video}
			if v.o.microVideo {
				tags = map[string][]byte{"EmbeddedVideo": m.video}
			}
			if !v.o.microVideo && !v.o.noMPF {
				tags["MPImage2"] = m.gainMap
			}
			for tag, want := range tags {
				got, err := exec.Command("exiftool", "-ee", "-b", "-"+tag, path).Output()
				if err != nil {
					t.Fatalf("exiftool: %v", err)
				}
				if !bytes.Equal(got, want) {
					t.Errorf("%s (%s): ExifTool's %s is %d bytes, not the %d embedded", v.name, map[int]string{0: "before", 1: "after"}[j], tag, len(got), len(want))
				}
			}
		}
	}
}

// checkMPFExact checks every MPF entry against where the images are: the
// primary from 0 to the gain map's start, the gain map at its offset with
// its length. Where exiftool is installed, ExifTool's MPImage lengths and
// starts say the same.
func checkMPFExact(t *testing.T, name string, file []byte, m motionPhoto) {
	t.Helper()
	f, err := parseJPEG(bytes.NewReader(file), int64(len(file)))
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	x, err := f.mpf(bytes.NewReader(file))
	if err != nil || len(x.entries) != 2 {
		t.Fatalf("%s: MPF %+v, %v", name, x, err)
	}
	sos := f.segs[len(f.segs)-1]
	gainAt := int64(bytes.Index(file[sos.body:], m.gainMap)) + sos.body
	if gainAt < sos.body {
		t.Fatalf("%s: the gain map is gone", name)
	}
	if e := x.entries[0]; e.offset != 0 || int64(e.size) != gainAt {
		t.Errorf("%s: primary entry offset %d size %d, want 0 and %d", name, e.offset, e.size, gainAt)
	}
	if e := x.entries[1]; x.base+int64(e.offset) != gainAt || int(e.size) != len(m.gainMap) {
		t.Errorf("%s: gain map entry at %d size %d, want %d and %d", name, x.base+int64(e.offset), e.size, gainAt, len(m.gainMap))
	}
	if _, err := exec.LookPath("exiftool"); err != nil {
		return
	}
	path := filepath.Join(t.TempDir(), "mpf.jpg")
	if err := os.WriteFile(path, file, 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("exiftool", "-n", "-s3", "-MPImage1:MPImageStart", "-MPImage1:MPImageLength",
		"-MPImage2:MPImageStart", "-MPImage2:MPImageLength", path).Output()
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("0\n%d\n%d\n%d\n", gainAt, gainAt, len(m.gainMap))
	if string(out) != want {
		t.Errorf("%s: ExifTool's MPImage start/length %q, want %q", name, out, want)
	}
}

// Writes that grow and shrink the primary image's segments, before and
// after the MPF segment, keep every MPF entry exact.
func TestMPFAfterWrites(t *testing.T) {
	long := string(bytes.Repeat([]byte("a long title "), 40))
	empty, none := "", []string{}
	keywords := []string{"one", "two"}
	date := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	steps := []struct {
		name string
		edit MetadataEdit
	}{
		{"Exif added (grows)", MetadataEdit{Exif: &ExifEdit{DateTimeOriginal: &date, GPS: &GPS{Latitude: 1, Longitude: 2}}}},
		{"XMP grows", MetadataEdit{XMP: &XMPEdit{Title: &long}}},
		{"IPTC added (grows)", MetadataEdit{IPTC: &IPTCEdit{Title: &long, Keywords: &keywords}}},
		{"IPTC removed (shrinks)", MetadataEdit{IPTC: &IPTCEdit{Title: &empty, Keywords: &none}}},
		{"all at once", MetadataEdit{Exif: &ExifEdit{RemoveGPS: true}, XMP: &XMPEdit{Title: &empty}, IPTC: &IPTCEdit{Keywords: &keywords}}},
	}
	for _, v := range motionVariants {
		if v.o.microVideo || v.o.noMPF {
			continue
		}
		m := buildMotionPhoto(t, v.o)
		checkMPFExact(t, v.name+"/as built", m.file, m)
		file := m.file
		for _, s := range steps {
			p, err := PlanMetadata(bytes.NewReader(file), int64(len(file)), s.edit)
			if err != nil {
				t.Fatalf("%s/%s: %v", v.name, s.name, err)
			}
			var out bytes.Buffer
			if _, err := p.WriteTo(&out); err != nil {
				t.Fatal(err)
			}
			if s.name == "IPTC removed (shrinks)" && out.Len() >= len(file) {
				t.Errorf("%s/%s: %d → %d bytes, didn't shrink", v.name, s.name, len(file), out.Len())
			}
			file = out.Bytes()
			checkMPFExact(t, v.name+"/"+s.name, file, m)
			checkEmbedded(t, v.name+"/"+s.name, file, m, v.o)
		}
	}
}
