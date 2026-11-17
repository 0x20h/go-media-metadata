package bmff

import (
	"bytes"
	"errors"
	"reflect"
	"testing"
)

// ParseBoxes gives what ReadBoxes reads from the same bytes: the boxes, or
// an error of the same kind.
func FuzzParseBoxes(f *testing.F) {
	f.Add([]byte("\x00\x00\x00\x08free\x00\x00\x00\x0cmeta\x00\x00\x00\x00"), int64(0))
	f.Add([]byte("\x00\x00\x00\x01mdat\x00\x00\x00\x00\x00\x00\x00\x10"), int64(0))
	f.Add([]byte("\x00\x00\x00\x00ftyp\x01\x02"), int64(0))
	f.Add(append([]byte("\x00\x00\x00\x18uuid"), make([]byte, 16)...), int64(0))
	f.Add([]byte("\x00\x00\x00\x09free\x00"), int64(2))
	f.Fuzz(func(t *testing.T, b []byte, start int64) {
		if start < 0 || start > int64(len(b)) {
			return
		}
		want, werr := ReadBoxes(bytes.NewReader(b), start, int64(len(b)))
		got, gerr := ParseBoxes(b, start, int64(len(b)))
		if (werr == nil) != (gerr == nil) || werr != nil && (!errors.Is(gerr, ErrMalformed) || werr.Error() != gerr.Error()) {
			t.Fatalf("ReadBoxes %v, ParseBoxes %v", werr, gerr)
		}
		if werr == nil && !reflect.DeepEqual(want, got) {
			t.Fatalf("ReadBoxes %v, ParseBoxes %v", want, got)
		}
	})
}
