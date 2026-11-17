package mediameta

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/0x20h/go-media-metadata/internal/bmff"
	"github.com/0x20h/go-media-metadata/internal/bmff/qtbuild"
)

// nctg is Nikon's NCTG payload: Make, Model, Software, CreateDate and
// DateTimeOriginal as ASCII records, and a FrameCount (LONG[2]) between.
func nctg() []byte {
	var b []byte
	rec := func(tag uint32, typ uint16, v []byte) {
		size := typeSize(typ)
		b = binary.BigEndian.AppendUint32(b, tag)
		b = binary.BigEndian.AppendUint16(b, typ)
		b = binary.BigEndian.AppendUint16(b, uint16(len(v)/size))
		b = append(b, v...)
	}
	rec(0x01, typeASCII, []byte("NIKON CORPORATION\x00"))
	rec(0x02, typeASCII, []byte("NIKON Z 6\x00"))
	rec(0x03, typeASCII, []byte("Ver.01.00\x00"))
	rec(0x13, typeLong, []byte{0, 0, 1, 0xD3, 0, 0, 0, 0})
	rec(0x11, typeASCII, []byte("2021:07:08 09:10:12\x00"))
	rec(0x12, typeASCII, []byte("2021:07:08 09:10:11\x00"))
	return b
}

// nikonMovie is a movie without Keys or a user data date, with Nikon's
// NCDT (holding NCTG) in its user data.
func nikonMovie(t *testing.T) []byte {
	t.Helper()
	o := qtbuild.Default()
	o.Keys = nil
	o.MoovFirst = false
	src, _, _ := qtbuild.Build(o)
	top, err := bmff.ReadBoxes(bytes.NewReader(src), 0, int64(len(src)))
	if err != nil {
		t.Fatal(err)
	}
	mb, _ := bmff.Find(top, bmff.T("moov"))
	moov, err := parseQTNode(src[mb.Offset:mb.End()], 0)
	if err != nil {
		t.Fatal(err)
	}
	udta := moov.child(bmff.T("udta"))
	if udta == nil {
		udta = &qtNode{typ: bmff.T("udta")}
		moov.kids = append(moov.kids, udta)
	}
	ncdt := &qtNode{typ: bmff.T("NCDT"), kids: []*qtNode{{typ: bmff.T("NCTG"), body: nctg()}}}
	udta.kids = append(udta.kids, ncdt)
	if mb.End() != int64(len(src)) {
		t.Fatal("moov isn't last")
	}
	return append(append([]byte(nil), src[:mb.Offset]...), moov.encode()...)
}

func TestNikonNCTG(t *testing.T) {
	file := nikonMovie(t)
	v, err := ReadVideo(bytes.NewReader(file), int64(len(file)))
	want := time.Date(2021, 7, 8, 9, 10, 11, 0, time.UTC)
	if err != nil || v.Make != "NIKON CORPORATION" || v.Model != "NIKON Z 6" || !v.Created.Equal(want) || v.OffsetKnown {
		t.Fatalf("%+v, %v", v, err)
	}
	if b := parseNCTGBroken(); b != "" {
		t.Errorf("a truncated record read as %q", b)
	}
	tool, err := exec.LookPath("exiftool")
	if err != nil {
		t.Skip("exiftool not installed")
	}
	p := filepath.Join(t.TempDir(), "nikon.mov")
	if err := os.WriteFile(p, file, 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(tool, "-j", "-q", "-Nikon:Make", "-Nikon:Model", "-Nikon:DateTimeOriginal", p).Output()
	var got []map[string]any
	if err != nil || json.Unmarshal(out, &got) != nil || len(got) != 1 {
		t.Fatalf("%v %s", err, out)
	}
	e := got[0]
	if e["Make"] != v.Make || e["Model"] != v.Model || e["DateTimeOriginal"] != v.Created.Format("2006:01:02 15:04:05") {
		t.Errorf("ExifTool %v, ReadVideo %+v", e, v)
	}
}

// parseNCTGBroken parses NCTG data whose last record runs past its end.
func parseNCTGBroken() string {
	b := nctg()
	_, _, dto := parseNCTG(b[:len(b)-3])
	return dto
}

// nctgSeedMovie is a small movie with NCTG for the fuzzers: an NCDT box
// appended to a qtbuild movie's user data, built without testing.T.
func nctgSeedMovie() []byte {
	o := qtbuild.Default()
	o.Keys = nil
	o.MediaSize = 64
	src, _, _ := qtbuild.Build(o)
	top, _ := bmff.ReadBoxes(bytes.NewReader(src), 0, int64(len(src)))
	mb, _ := bmff.Find(top, bmff.T("moov"))
	moov, err := parseQTNode(src[mb.Offset:mb.End()], 0)
	if err != nil {
		return src
	}
	udta := moov.child(bmff.T("udta"))
	if udta == nil {
		udta = &qtNode{typ: bmff.T("udta")}
		moov.kids = append(moov.kids, udta)
	}
	udta.kids = append(udta.kids, &qtNode{typ: bmff.T("NCDT"), kids: []*qtNode{{typ: bmff.T("NCTG"), body: nctg()}}})
	return append(append(append([]byte(nil), src[:mb.Offset]...), moov.encode()...), src[mb.End():]...)
}
