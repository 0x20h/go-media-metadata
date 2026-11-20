package mediameta

import (
	"encoding/binary"
	"testing"
)

// embeddedExif refuses an IFD before its base or past the data, and a
// block cut short reads as what fits.
func TestEmbeddedExifBounds(t *testing.T) {
	for _, c := range []struct {
		data      []byte
		ifd, base int
	}{
		{make([]byte, 20), 4, 8},
		{make([]byte, 8), 7, 0},
		{nil, 0, 0},
		{make([]byte, 20), 0, -1},
	} {
		if _, ok := embeddedExif(c.data, c.ifd, c.base, binary.BigEndian); ok {
			t.Errorf("%+v: read", c)
		}
	}
	file := readFile(t, "testdata/video/casio-qvmi.mov")
	for n := 0; n < len(file); n += 7 {
		embeddedExif(file[:n], 10, 0, binary.BigEndian)
		udtaExif("MVTG", file[:n])
		aviStreamExif(append([]byte("AVIF\x00\x00\x00\x00"), file[:n]...))
	}
}
