package mediameta

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"strings"
	"time"
)

// AVI (RIFF "AVI "): the Microsoft AVI file format. Its date is written
// (avi_edit.go); the rest is read only. The header
// list (hdrl) has the main header (avih: the frame rate and count, the
// size); an IDIT chunk the recording date, as cameras write it; the INFO
// list the title (INAM), a comment (ICMT), keywords (IKEY) and a creation
// date (ICRD); Nikon's ncdt list the make, model and date taken (nctg).

// errAVIReadOnly is the error of a rotation or orientation write to an AVI
// file.
var errAVIReadOnly = fmt.Errorf("%w: an AVI file's rotation is read only", ErrUnsupported)

// maxAVIChunk caps the header chunks read whole.
const maxAVIChunk = 1 << 20

func isAVI(r io.ReaderAt, size int64) bool {
	var h [12]byte
	if size < 12 {
		return false
	}
	if _, err := r.ReadAt(h[:], 0); err != nil {
		return false
	}
	return string(h[:4]) == "RIFF" && string(h[8:12]) == "AVI "
}

// aviChunk is a RIFF chunk: its id (a LIST's own type for a list), where
// its data starts and how long it is.
type aviChunk struct {
	id    string
	list  bool
	at, n int64
}

// aviChunks reads the chunks in [start, end).
func aviChunks(r io.ReaderAt, start, end int64) ([]aviChunk, error) {
	var out []aviChunk
	for at := start; at+8 <= end; {
		var h [12]byte
		if _, err := r.ReadAt(h[:8], at); err != nil {
			return out, fmt.Errorf("%w: reading a chunk header: %v", ErrMalformed, err)
		}
		n := int64(binary.LittleEndian.Uint32(h[4:]))
		c := aviChunk{id: string(h[:4]), at: at + 8, n: n}
		if c.at+n > end {
			n = end - c.at // a file cut short: what's there
			c.n = n
		}
		if c.id == "LIST" && n >= 4 {
			if _, err := r.ReadAt(h[8:12], c.at); err != nil {
				return out, fmt.Errorf("%w: reading a list: %v", ErrMalformed, err)
			}
			c.id, c.list, c.at, c.n = string(h[8:12]), true, c.at+4, n-4
		}
		out = append(out, c)
		at += 8 + n + n&1
		if len(out) > 10000 {
			return out, fmt.Errorf("%w: an AVI with too many chunks", ErrUnsupported)
		}
	}
	return out, nil
}

func readAVIChunk(r io.ReaderAt, c aviChunk) ([]byte, error) {
	if c.n > maxAVIChunk {
		return nil, fmt.Errorf("%w: an AVI %s chunk of %d bytes", ErrUnsupported, c.id, c.n)
	}
	b := make([]byte, c.n)
	if _, err := r.ReadAt(b, c.at); err != nil {
		return nil, fmt.Errorf("%w: reading %s: %v", ErrMalformed, c.id, err)
	}
	return b, nil
}

// aviText is a chunk's text, up to its NUL, trimmed.
func aviText(b []byte) string {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return strings.TrimSpace(string(b))
}

// readAVIVideo reads Video from an AVI file: the duration and size from the
// main header, the date from IDIT (else INFO's ICRD), the title, comment
// and keywords from INFO.
func readAVIVideo(r io.ReaderAt, size int64) (Video, error) {
	if !isAVI(r, size) {
		return Video{}, formatError("AVI file")
	}
	v := Video{Orientation: 1}
	riffEnd := min(size, 8+int64(binary.LittleEndian.Uint32(mustRead(r, 4, 4))))
	top, err := aviChunks(r, 12, riffEnd)
	if err != nil && len(top) == 0 {
		return v, err
	}
	var icrd, nikonDate string
	for _, c := range top {
		switch {
		case c.list && c.id == "hdrl":
			kids, _ := aviChunks(r, c.at, c.at+c.n)
			for _, k := range kids {
				switch k.id {
				case "avih":
					b, err := readAVIChunk(r, k)
					if err != nil || len(b) < 40 {
						continue
					}
					usPerFrame := int64(binary.LittleEndian.Uint32(b))
					frames := int64(binary.LittleEndian.Uint32(b[16:]))
					// Both are 32-bit: their product is in range, but not
					// in nanoseconds past about 292 years.
					if us := usPerFrame * frames; us >= 0 && us <= math.MaxInt64/int64(time.Microsecond) {
						v.Duration = time.Duration(us) * time.Microsecond
					}
					w, h := binary.LittleEndian.Uint32(b[32:]), binary.LittleEndian.Uint32(b[36:])
					if w <= 1<<16 && h <= 1<<16 {
						v.Width, v.Height = int(w), int(h)
					}
				case "IDIT":
					if b, err := readAVIChunk(r, k); err == nil {
						if at, ok := parseAVIDate(aviText(b)); ok {
							v.Created = at
						}
					}
				}
			}
		case c.id == "IDIT":
			if b, err := readAVIChunk(r, c); err == nil {
				if at, ok := parseAVIDate(aviText(b)); ok && v.Created.IsZero() {
					v.Created = at
				}
			}
		case c.list && c.id == "ncdt":
			// Nikon's tags (ExifTool's Nikon AVI tags): an nctg chunk.
			kids, _ := aviChunks(r, c.at, c.at+c.n)
			for _, k := range kids {
				if k.id != "nctg" {
					continue
				}
				if b, err := readAVIChunk(r, k); err == nil {
					make_, model, dto := parseNikonAVITags(b)
					v.Make, v.Model = first(v.Make, make_), first(v.Model, model)
					nikonDate = first(nikonDate, dto)
				}
			}
		case c.list && c.id == "INFO":
			kids, _ := aviChunks(r, c.at, c.at+c.n)
			for _, k := range kids {
				b, err := readAVIChunk(r, k)
				if err != nil {
					continue
				}
				switch s := aviText(b); k.id {
				case "INAM":
					v.Title = s
				case "ICMT", "ISBJ":
					if v.Description == "" {
						v.Description = s
					}
				case "IKEY":
					v.Keywords = splitAVIKeywords(s)
				case "ICRD":
					icrd = s
				}
			}
		}
	}
	// Nikon's DateTimeOriginal: the camera's clock time, no offset.
	if v.Created.IsZero() {
		if at, _, ok := parseExifDate(nikonDate); ok {
			v.Created = at
		}
	}
	// ICRD, when it holds a whole date: a year alone would read as 1
	// January, more precise than written.
	if v.Created.IsZero() && len(icrd) >= len("2006-01-02") {
		if at, ok := parseAVIDate(icrd); ok {
			v.Created = at
		}
	}
	if v.Width <= 0 || v.Height <= 0 {
		v.Width, v.Height = 0, 0
	}
	return v, nil
}

// parseNikonAVITags reads a Nikon AVI's nctg records: each a 16-bit tag
// and a 16-bit size, little-endian, then that many bytes of value. Make
// is tag 3, Model 4, DateTimeOriginal 0x13, each a string.
func parseNikonAVITags(b []byte) (make_, model, dto string) {
	for at := 0; at+4 <= len(b); {
		tag := binary.LittleEndian.Uint16(b[at:])
		n := int(binary.LittleEndian.Uint16(b[at+2:]))
		at += 4
		if at+n > len(b) {
			break
		}
		switch s := aviText(b[at : at+n]); tag {
		case 0x03:
			make_ = s
		case 0x04:
			model = s
		case 0x13:
			dto = s
		}
		at += n
	}
	return make_, model, dto
}

func mustRead(r io.ReaderAt, at, n int64) []byte {
	b := make([]byte, n)
	if _, err := r.ReadAt(b, at); err != nil {
		return make([]byte, n)
	}
	return b
}

// splitAVIKeywords splits IKEY's keywords, separated by ";" (or ",").
func splitAVIKeywords(s string) []string {
	var out []string
	for _, k := range strings.FieldsFunc(s, func(r rune) bool { return r == ';' || r == ',' }) {
		if k = strings.TrimSpace(k); k != "" {
			out = append(out, k)
		}
	}
	return out
}

// parseAVIDate reads the dates AVI writers put in IDIT and ICRD: C's
// asctime ("Tue Jun 07 08:09:10 2005"), the Exif layout ("2005:06:07
// 08:09:10"), ISO dates ("2005-06-07"), or a year alone. A clock time
// without a zone: in time.UTC, OffsetKnown false.
func parseAVIDate(s string) (time.Time, bool) {
	s = strings.Join(strings.Fields(s), " ")
	for _, layout := range []string{
		"Mon Jan 2 15:04:05 2006", "Mon Jan _2 15:04:05 2006", "Mon Jan 02 15:04:05 2006",
		"2006:01:02 15:04:05", "2006-01-02 15:04:05", "2006-01-02T15:04:05", "2006/01/02 15:04:05",
		"2006-01-02", "2006:01:02", "2006/01/02", "2006",
	} {
		if t, err := time.ParseInLocation(layout, s, time.UTC); err == nil && t.Year() > 0 {
			return t, true
		}
	}
	return time.Time{}, false
}
