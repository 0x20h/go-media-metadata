package mediameta

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"io"
	"strings"
	"time"
	"unicode/utf8"
)

// PNGText is a PNG's own text metadata, as ExifTool reads it (its PNG
// group): the text chunks with the predefined keywords Title and
// Description (tEXt, zTXt or iTXt; the first of each), and the tIME chunk,
// the image's last modification (ExifTool's PNG ModifyDate). XMP and
// Exif, which PNGs carry in their own chunks, aren't part of it.
type PNGText struct {
	Title, Description string
	// Modified is tIME's time, in UTC as PNG specifies it; zero without a
	// valid one.
	Modified time.Time
}

// ReadPNGText reads a PNG's text metadata (ErrUnknownFormat for a file
// that isn't a PNG).
func ReadPNGText(r io.ReaderAt, size int64) (PNGText, error) {
	if !isPNG(r, size) {
		return PNGText{}, ErrUnknownFormat
	}
	chunks, err := pngChunks(r, size)
	if err != nil {
		return PNGText{}, err
	}
	return readPNGText(r, chunks), nil
}

// readPNGText reads the text metadata of the PNG whose chunks are listed.
// A chunk that can't be read or decoded is skipped.
func readPNGText(r io.ReaderAt, chunks []pngChunk) PNGText {
	var t PNGText
	for _, c := range chunks {
		switch typ := string(c.typ[:]); typ {
		case "tIME":
			if c.n != 7 || !t.Modified.IsZero() {
				continue
			}
			var b [7]byte
			if _, err := r.ReadAt(b[:], c.data()); err != nil {
				continue
			}
			y := int(binary.BigEndian.Uint16(b[:]))
			mo, d, h, mi, s := int(b[2]), int(b[3]), int(b[4]), int(b[5]), int(b[6])
			at := time.Date(y, time.Month(mo), d, h, mi, s, 0, time.UTC)
			// The values as written: no month 13, no 30 February. A leap
			// second (60) is allowed by PNG and read as the next second.
			if y > 0 && mo >= 1 && mo <= 12 && d >= 1 && at.Day() == d && h < 24 && mi < 60 && s <= 60 {
				t.Modified = at
			}
		case "tEXt", "zTXt", "iTXt":
			if c.n > maxMetadataItem {
				continue
			}
			head := make([]byte, min(c.n, 12))
			if _, err := r.ReadAt(head, c.data()); err != nil {
				continue
			}
			var dst *string
			switch {
			case bytes.HasPrefix(head, []byte("Title\x00")):
				dst = &t.Title
			case bytes.HasPrefix(head, []byte("Description\x00")):
				dst = &t.Description
			default:
				continue
			}
			if *dst != "" {
				continue
			}
			b, err := readChunk(r, c.data(), c.n)
			if err != nil {
				continue
			}
			*dst = pngTextValue(typ, b)
		}
	}
	return t
}

// pngTextValue is a text chunk's text: tEXt Latin-1, zTXt Latin-1
// compressed, iTXt UTF-8 (compressed or not); "" for one that doesn't
// decode.
func pngTextValue(typ string, b []byte) string {
	k := bytes.IndexByte(b, 0)
	if k < 0 {
		return ""
	}
	var text []byte
	latin1 := true
	switch typ {
	case "tEXt":
		text = b[k+1:]
	case "zTXt":
		if k+2 > len(b) || b[k+1] != 0 {
			return ""
		}
		text = inflateText(b[k+2:])
	case "iTXt":
		it, ok := parseITXt(b)
		if !ok || !utf8.Valid(it.text) {
			return ""
		}
		text, latin1 = it.text, false
	}
	if latin1 {
		r := make([]rune, len(text))
		for i, c := range text {
			r[i] = rune(c)
		}
		return strings.TrimSpace(string(r))
	}
	return strings.TrimSpace(string(text))
}

// inflateText inflates a zlib stream, up to maxMetadataItem bytes; nil
// for one that doesn't inflate.
func inflateText(b []byte) []byte {
	zr, err := zlib.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil
	}
	out, err := io.ReadAll(io.LimitReader(zr, maxMetadataItem+1))
	if err != nil || len(out) > maxMetadataItem {
		return nil
	}
	return out
}
