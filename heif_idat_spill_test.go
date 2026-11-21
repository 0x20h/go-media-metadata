package mediameta

import (
	"bytes"
	"crypto/md5"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
)

// FuzzSetRotation's input b15f74e0a94d5127: an hvc1 item in idat whose
// extent (offset 997, length 337) runs far past idat. HashImageData reads
// it as ExifTool does, from idat's start on into the file; an edit of
// meta would move what lies there, so every HEIF write is refused, and
// the hash stays ExifTool's.
func TestHEIFIdatSpillRefused(t *testing.T) {
	raw, err := os.ReadFile("testdata/fuzz/FuzzSetRotation/b15f74e0a94d5127")
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	s = s[strings.Index(s, "[]byte(")+len("[]byte(") : strings.LastIndex(s, ")")]
	q, err := strconv.Unquote(s)
	if err != nil {
		t.Fatal(err)
	}
	file := []byte(q)
	r, n := bytes.NewReader(file), int64(len(file))
	h := md5.New()
	if _, err := HashImageData(r, n, h); err != nil {
		t.Fatal(err)
	}
	if _, err := PlanRotation(r, n, 90); !errors.Is(err, ErrUnsupported) {
		t.Errorf("PlanRotation: %v, want ErrUnsupported", err)
	}
	if _, err := PlanOrientation(r, n, 6); !errors.Is(err, ErrUnsupported) {
		t.Errorf("PlanOrientation: %v, want ErrUnsupported", err)
	}
	title := "T"
	// PlanMetadata reads idat's items itself, and refuses the file as
	// malformed.
	if _, err := PlanMetadata(r, n, MetadataEdit{XMP: &XMPEdit{Title: &title}}); err == nil {
		t.Error("PlanMetadata: no error")
	}
}
