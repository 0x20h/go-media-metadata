package mediameta

import (
	"encoding/json"
	"errors"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRepairXML(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"a & b", "a &amp; b"},
		{"a &amp; &lt;&gt;&quot;&apos; &#38; &#x26;", "a &amp; &lt;&gt;&quot;&apos; &#38; &#x26;"},
		{"&#1; &#xD800; &#; &#x; &nbsp; &a b;", "&amp;#1; &amp;#xD800; &amp;#; &amp;#x; &amp;nbsp; &amp;a b;"},
		{"x\x01y\x0b\tz\r\n", "x�y�\tz\r\n"},
		{"caf\xe9 \xff", "caf� �"},
		{"￾ ok é \U0001F600", "� ok é \U0001F600"},
		{"&", "&amp;"},
	} {
		got, changed := repairXML([]byte(c.in))
		if string(got) != c.want || changed != (c.in != c.want) {
			t.Errorf("repairXML(%q) = %q, %v, want %q", c.in, got, changed, c.want)
		}
	}
}

// A packet broken only by its characters reads as ExifTool reads it, with
// ErrXMPRepaired; EditXMP refuses it.
func TestParseXMPLenient(t *testing.T) {
	paths, _ := filepath.Glob("testdata/xmp-lenient/*.xmp")
	if len(paths) < 5 {
		t.Fatalf("%d packets", len(paths))
	}
	for _, p := range paths {
		packet := readFile(t, p)
		x, err := ParseXMP(packet)
		if !errors.Is(err, ErrXMPRepaired) {
			t.Errorf("%s: %v, want ErrXMPRepaired", p, err)
		}
		if x.Rating == 0 {
			t.Errorf("%s: nothing read: %+v", p, x)
		}
		if _, err := EditXMP(packet, XMPEdit{Rating: ptr(1)}); !errors.Is(err, ErrMalformed) {
			t.Errorf("%s: EditXMP %v, want ErrMalformed", p, err)
		}
	}
	tool, err := exec.LookPath("exiftool")
	if err != nil {
		t.Skip("exiftool not installed")
	}
	out, err := exec.Command(tool, append([]string{"-j", "-q", "-XMP-dc:Title", "-XMP-dc:Description", "-XMP-dc:Subject",
		"-XMP-xmp:Rating", "-XMP-photoshop:DateCreated"}, paths...)...).Output()
	if err != nil {
		t.Fatal(err)
	}
	var got []struct {
		SourceFile, Title, Description, DateCreated string
		Subject                                     any
		Rating                                      int
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	// ExifTool keeps a control character, which XML can't carry, and shows
	// a byte that isn't UTF-8 as "?" in JSON; ParseXMP gives U+FFFD for
	// both.
	same := func(lib, tool string) bool {
		var b strings.Builder
		for _, r := range tool {
			if r < 0x20 && r != '\t' && r != '\n' && r != '\r' || r == '?' {
				r = '�'
			}
			b.WriteRune(r)
		}
		return lib == b.String()
	}
	for _, e := range got {
		x, _ := ParseXMP(readFile(t, e.SourceFile))
		var subject []string
		switch s := e.Subject.(type) {
		case string:
			subject = []string{s}
		case []any:
			for _, v := range s {
				subject = append(subject, v.(string))
			}
		}
		if !same(x.Title, e.Title) || !same(x.Description, e.Description) || x.Rating != e.Rating ||
			len(x.Keywords)+len(subject) > 0 && !reflect.DeepEqual(x.Keywords, subject) {
			t.Errorf("%s: %q %q %v %d, ExifTool %+v", e.SourceFile, x.Title, x.Description, x.Keywords, x.Rating, e)
		}
		if e.DateCreated != "" {
			want, _ := time.Parse("2006:01:02 15:04:05", e.DateCreated)
			if x.DateCreated == nil || !x.DateCreated.Time.Equal(want) {
				t.Errorf("%s: DateCreated %+v, ExifTool %s", e.SourceFile, x.DateCreated, e.DateCreated)
			}
		}
	}
}

// The lenient parse is never worse than the strict one: a packet the
// strict parse reads gives no ErrXMPRepaired and the same fields.
func FuzzParseXMPLenient(f *testing.F) {
	paths, _ := filepath.Glob("testdata/xmp-lenient/*.xmp")
	for _, p := range paths {
		f.Add(readFile(f, p))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		withinDeadline(t, func() {
			strict, serr := parseXMPDoc(data)
			x, err := ParseXMP(data)
			if serr == nil {
				if err != nil {
					t.Fatalf("strict read, lenient %v", err)
				}
				if !reflect.DeepEqual(strict.fields(), x) {
					t.Fatalf("strict %+v, lenient %+v", strict.fields(), x)
				}
				return
			}
			if errors.Is(err, ErrXMPRepaired) {
				fixed, _ := repairXML(data)
				if again, changed := repairXML(fixed); changed || string(again) != string(fixed) {
					t.Fatalf("repair isn't stable: %q", again)
				}
			}
		})
	})
}
