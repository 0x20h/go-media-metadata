package mediameta

import (
	"bytes"
	"compress/zlib"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
)

// A PNG's IPTC-IIM data lives in a raw profile text chunk, "Raw profile
// type iptc" (as ImageMagick and ExifTool write it): hex of Photoshop
// image resources whose 0x0404 holds the IIM data, or of the IIM data
// alone.

// readIPTC reads the IIM data of the PNG whose chunks m lists, nil
// without any.
func (m *pngMetadata) readIPTC(r io.ReaderAt) ([]byte, error) {
	if m.rawIPTC < 0 {
		return nil, nil
	}
	b, ok := decodeRawProfile(r, m.chunks[m.rawIPTC])
	if !ok {
		return nil, fmt.Errorf("%w: a PNG IPTC raw profile that doesn't decode", ErrMalformed)
	}
	return iimOfProfile(b)
}

// iimOfProfile is the IIM data in a raw IPTC profile's data.
func iimOfProfile(b []byte) ([]byte, error) {
	switch {
	case bytes.HasPrefix(b, []byte("8BIM")):
		rs, err := parseIRBs(b)
		if err != nil {
			return nil, err
		}
		for _, x := range rs {
			if x.id == irbIPTC && string(x.sig[:]) == "8BIM" {
				return bytes.Clone(x.data), nil
			}
		}
		return nil, nil
	case len(b) > 0 && b[0] == 0x1C:
		return bytes.Clone(b), nil
	}
	return nil, fmt.Errorf("%w: a PNG IPTC raw profile that isn't IIM", ErrMalformed)
}

// rawProfileChunk is a zTXt chunk holding a raw profile of data, as
// ExifTool writes it: "\n<label> profile\n<length>\n" and the hex in
// lines of 72 digits.
func rawProfileChunk(keyword, label string, data []byte) []byte {
	var t strings.Builder
	fmt.Fprintf(&t, "\n%s profile\n%8d\n", label, len(data))
	h := hex.EncodeToString(data)
	for len(h) > 72 {
		t.WriteString(h[:72] + "\n")
		h = h[72:]
	}
	t.WriteString(h + "\n")
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	_, _ = zw.Write([]byte(t.String()))
	_ = zw.Close()
	return pngChunkBytes("zTXt", append(append([]byte(keyword), 0, 0), z.Bytes()...))
}

// planPNGIPTC plans edit applied to a PNG's IPTC: the raw profile chunk
// replaced (other image resources kept, when the profile has them), added
// before the image data where there's none, removed when no IIM data is
// left.
func planPNGIPTC(src io.ReaderAt, size int64, edit IPTCEdit) (*Plan, error) {
	m, err := readPNGMetadata(src, size)
	if err != nil {
		return nil, err
	}
	var old, profile []byte
	if m.rawIPTC >= 0 {
		b, ok := decodeRawProfile(src, m.chunks[m.rawIPTC])
		if !ok {
			return nil, fmt.Errorf("%w: a PNG IPTC raw profile that doesn't decode", ErrMalformed)
		}
		profile = b
		if old, err = iimOfProfile(b); err != nil {
			return nil, err
		}
	}
	iim, err := EditIPTC(old, edit)
	if err != nil {
		return nil, err
	}
	e := &spliceEdit{}
	if bytes.Equal(iim, old) {
		return e.plan(src, size), nil
	}
	var chunk []byte
	if len(iim) > 0 {
		// The IIM data as resource 0x0404, with the profile's other
		// resources where it had them.
		rs := []irb{{sig: [4]byte{'8', 'B', 'I', 'M'}, id: irbIPTC, name: []byte{0, 0}, data: iim}}
		if bytes.HasPrefix(profile, []byte("8BIM")) {
			if old, err := parseIRBs(profile); err == nil {
				placed := false
				for i, x := range old {
					if x.id == irbIPTC && string(x.sig[:]) == "8BIM" && !placed {
						old[i].data, placed = iim, true
					}
				}
				if !placed {
					old = append(old, rs[0])
				}
				rs = old
			}
		}
		chunk = rawProfileChunk(rawProfileIPTC, "IPTC", writeIRBs(rs))
	}
	if m.rawIPTC >= 0 {
		c := m.chunks[m.rawIPTC]
		e.add(splice{at: c.at, del: c.end() - c.at, data: chunk})
	} else if chunk != nil {
		// Before the image data, where ExifTool puts it.
		at := m.chunks[0].end()
		for _, c := range m.chunks {
			if string(c.typ[:]) == "IDAT" {
				at = c.at
				break
			}
		}
		e.add(splice{at: at, data: chunk})
	}
	if err := e.sorted(); err != nil {
		return nil, err
	}
	return e.plan(src, size), nil
}
