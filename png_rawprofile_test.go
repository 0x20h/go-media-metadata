package mediameta

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// rawProfileText is ImageMagick's raw profile text for data: a newline,
// the type, the length, then hex in lines of 72 digits.
func rawProfileText(data []byte) []byte {
	h := hex.EncodeToString(data)
	var b strings.Builder
	fmt.Fprintf(&b, "\nexif\n%8d\n", len(data))
	for len(h) > 72 {
		b.WriteString(h[:72] + "\n")
		h = h[72:]
	}
	b.WriteString(h + "\n")
	return []byte(b.String())
}

func rawProfileTIFF() []byte {
	b := &tiffBuilder{order: binary.BigEndian}
	b.ifd0 = []tiffEntry{{tagMake, typeASCII, 8, []byte("Magick!\x00")}}
	b.exif = []tiffEntry{{tagDateTimeOriginal, typeASCII, 20, []byte("2001:02:03 04:05:06\x00")}}
	return b.build()
}

// rawProfilePNGs are a PNG with its Exif in a zTXt raw profile (after
// "Exif\0\0", as ImageMagick writes it), and one in a tEXt without the
// header.
func rawProfilePNGs(t testing.TB) map[string][]byte {
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	_, _ = zw.Write(rawProfileText(append([]byte("Exif\x00\x00"), rawProfileTIFF()...)))
	_ = zw.Close()
	ztxt := append(append([]byte(rawProfileExif), 0, 0), z.Bytes()...)
	text := append(append([]byte(rawProfileExif), 0), rawProfileText(rawProfileTIFF())...)
	return map[string][]byte{
		"ztxt.png": pngWithChunks(t, pngChunkBytes("zTXt", ztxt)),
		"text.png": pngWithChunks(t, pngChunkBytes("tEXt", text)),
	}
}

func TestPNGRawProfileExif(t *testing.T) {
	when := time.Date(2021, 7, 8, 9, 10, 11, 0, time.UTC)
	tool, _ := exec.LookPath("exiftool")
	dir := t.TempDir()
	for name, file := range rawProfilePNGs(t) {
		x, err := ReadExif(bytes.NewReader(file), int64(len(file)))
		if err != nil || x.Camera.Make != "Magick!" || x.DateTimeOriginal.Year() != 2001 {
			t.Fatalf("%s: %+v, %v", name, x, err)
		}
		// A date edit moves the Exif to an eXIf chunk, keeping its make, and
		// drops the raw profile.
		p, err := PlanMetadata(bytes.NewReader(file), int64(len(file)), MetadataEdit{Exif: &ExifEdit{DateTimeOriginal: &when}})
		if err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		if _, err := p.WriteTo(&buf); err != nil {
			t.Fatal(err)
		}
		out := buf.Bytes()
		y, err := ReadExif(bytes.NewReader(out), int64(len(out)))
		if err != nil || y.Camera.Make != "Magick!" || !y.DateTimeOriginal.Equal(when) {
			t.Errorf("%s edited: %+v, %v", name, y, err)
		}
		if bytes.Contains(out, []byte(rawProfileExif)) {
			t.Errorf("%s edited: the raw profile is still there", name)
		}
		if tool == "" {
			continue
		}
		for kind, b := range map[string][]byte{"src": file, "edited": out} {
			path := filepath.Join(dir, kind+"-"+name)
			if err := os.WriteFile(path, b, 0o644); err != nil {
				t.Fatal(err)
			}
			res, err := exec.Command(tool, "-j", "-q", "-Make", "-DateTimeOriginal", path).Output()
			var got []map[string]any
			if err != nil || json.Unmarshal(res, &got) != nil || len(got) != 1 {
				t.Fatalf("%v %s", err, res)
			}
			want := "2001:02:03 04:05:06"
			if kind == "edited" {
				want = "2021:07:08 09:10:11"
			}
			if got[0]["Make"] != "Magick!" || got[0]["DateTimeOriginal"] != want {
				t.Errorf("%s %s: ExifTool %v", kind, name, got[0])
			}
		}
	}
}
