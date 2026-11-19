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
// group): the text chunks with the predefined keywords Title, Description
// and Creation Time (tEXt, zTXt or iTXt; the first of each; a word of
// the keyword may start in lower case, as ExifTool matches it), and the
// tIME chunk, the image's
// last modification (ExifTool's PNG ModifyDate). XMP and Exif, which PNGs
// carry in their own chunks, aren't part of it.
type PNGText struct {
	Title, Description string
	// Modified is tIME's time, in UTC as PNG specifies it; zero without a
	// valid one.
	Modified time.Time
	// Created is the Creation Time text chunk's date (ExifTool's PNG
	// CreationTime), nil without one that reads. PNG leaves its form
	// free and recommends RFC 1123 ("Mon, 04 Mar 2014 05:06:07 GMT");
	// that, ISO 8601 and the Exif layout ("2014:03:04 05:06:07") are
	// read, as XMP dates are, with the precision they give.
	Created *Date
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
			var head [14]byte // "Creation Time\x00", the longest keyword read
			h := head[:min(c.n, int64(len(head)))]
			if _, err := r.ReadAt(h, c.data()); err != nil {
				continue
			}
			k := bytes.IndexByte(h, 0)
			if k < 0 {
				continue
			}
			var dst *string
			var created string
			switch kw := string(h[:k]); {
			case pngKeywordIs(kw, "Title"):
				dst = &t.Title
			case pngKeywordIs(kw, "Description"):
				dst = &t.Description
			case pngKeywordIs(kw, "Creation Time"):
				if t.Created != nil {
					continue
				}
				dst = &created
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
			if dst == &created {
				if d, ok := parsePNGDate(created); ok {
					t.Created = &d
				}
			}
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

// parsePNGDate reads a Creation Time text: RFC 1123 (or RFC 822's
// layout with a numeric zone, or without the weekday), else as an XMP
// date. RFC 1123's zone names other than GMT and UT read as an unknown
// offset.
func parsePNGDate(s string) (Date, bool) {
	s = strings.TrimSpace(s)
	for _, layout := range [...]string{time.RFC1123Z, time.RFC1123, "2 Jan 2006 15:04:05 -0700", "2 Jan 2006 15:04:05 MST", "Mon, 2 Jan 2006 15:04:05 -0700", "Mon, 2 Jan 2006 15:04:05 MST"} {
		at, err := time.Parse(layout, s)
		if err != nil {
			continue
		}
		d := Date{Time: at, Precision: PrecisionSecond, OffsetKnown: true}
		if strings.HasSuffix(layout, "MST") {
			if zone, _ := at.Zone(); zone != "GMT" && zone != "UT" && zone != "UTC" {
				// A zone name Go doesn't know reads as offset 0: keep the
				// clock time, without an offset.
				d.Time = time.Date(at.Year(), at.Month(), at.Day(), at.Hour(), at.Minute(), at.Second(), 0, time.UTC)
				d.OffsetKnown = false
			}
		}
		return d, true
	}
	return parseXMPDate(s)
}

// pngKeywordIs tells whether a text chunk's keyword is want, as ExifTool
// matches it: each word's first letter in either case, the rest as want
// has it ("title" and "creation Time" are Title and Creation Time,
// "TITLE" isn't).
func pngKeywordIs(kw, want string) bool {
	if len(kw) != len(want) {
		return false
	}
	for i := 0; i < len(kw); i++ {
		if kw[i] == want[i] {
			continue
		}
		if i > 0 && want[i-1] != ' ' || kw[i]|0x20 != want[i]|0x20 || want[i]|0x20 < 'a' || want[i]|0x20 > 'z' {
			return false
		}
	}
	return true
}
