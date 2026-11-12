package bmff_test

import (
	"encoding/binary"
	"errors"
	"testing"

	"github.com/0x20h/go-media-metadata/internal/bmff"
)

// zeroWidthIloc returns a meta box whose iloc has offset, length and base
// offset sizes of 0, so an extent takes no bytes, and items that each
// declare 65535 extents.
func zeroWidthIloc(items int) []byte {
	var iloc []byte
	iloc = append(iloc, 0, 0, 0, 0) // version 0, flags
	iloc = append(iloc, 0x00, 0x00) // offset/length/base offset sizes 0
	iloc = binary.BigEndian.AppendUint16(iloc, uint16(items))
	for i := range items {
		iloc = binary.BigEndian.AppendUint16(iloc, uint16(i+1)) // item ID
		iloc = binary.BigEndian.AppendUint16(iloc, 0)           // data reference index
		iloc = binary.BigEndian.AppendUint16(iloc, 0xFFFF)      // extent count
	}
	box := func(typ string, body []byte) []byte {
		b := binary.BigEndian.AppendUint32(nil, uint32(8+len(body)))
		return append(append(b, typ...), body...)
	}
	return box("meta", append([]byte{0, 0, 0, 0}, box("iloc", iloc)...))
}

// Extents that take no bytes in the file mustn't let a small iloc declare
// millions of them.
func TestParseMetaZeroWidthExtents(t *testing.T) {
	_, err := bmff.ParseMeta(zeroWidthIloc(1000))
	if !errors.Is(err, bmff.ErrMalformed) {
		t.Fatalf("err = %v, want ErrMalformed", err)
	}
}
