package mediameta

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
)

// XPKeywords are written joined by ";", read back by ReadAll as its
// keywords (when XMP and IPTC have none), removed by an empty list, and
// keywords that would read back otherwise are refused.
func TestEditExifXPKeywords(t *testing.T) {
	src := photoTIFF(binary.LittleEndian)
	data, err := EditExif(src, ExifEdit{XPKeywords: &[]string{"Lake", "Café 😀"}})
	if err != nil {
		t.Fatal(err)
	}
	if x := parseExif(data); x.XPKeywords != "Lake;Café 😀" {
		t.Errorf("XPKeywords %q", x.XPKeywords)
	}
	file := withSegments(plainJPEG(t), exifSegment(data))
	m, err := ReadAll(bytes.NewReader(file), int64(len(file)))
	if err != nil || strings.Join(m.Keywords, "|") != "Lake|Café 😀" {
		t.Errorf("ReadAll keywords %q, %v", m.Keywords, err)
	}
	removed, err := EditExif(data, ExifEdit{XPKeywords: &[]string{}})
	if err != nil {
		t.Fatal(err)
	}
	if x := parseExif(removed); x.XPKeywords != "" {
		t.Errorf("XPKeywords %q after removing them", x.XPKeywords)
	}
	for _, bad := range [][]string{{""}, {"a;b"}, {"a\x00"}, {"\xff"}} {
		if _, err := EditExif(src, ExifEdit{XPKeywords: &bad}); err == nil {
			t.Errorf("keywords %q accepted", bad)
		}
	}
}
