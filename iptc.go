package mediameta

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"strings"
	"unicode/utf8"
)

// IPTC is what this package reads from IPTC-IIM data (IPTC Information
// Interchange Model 4.2, the Application Record): the fields that mirror
// XMP's dc:title, dc:subject and dc:description, as the IPTC Photo
// Metadata Standard maps them.
type IPTC struct {
	// Title is 2:05 Object Name.
	Title string
	// Keywords are the 2:25 Keywords, in order.
	Keywords []string
	// Description is 2:120 Caption/Abstract.
	Description string
}

// IPTCEdit is a change to IPTC-IIM data. A nil field leaves that dataset
// as it is, so the zero IPTCEdit changes nothing.
type IPTCEdit struct {
	// Title and Description set 2:05 and 2:120. nil leaves the dataset as
	// it is; "" removes it.
	Title, Description *string
	// Keywords sets the 2:25 datasets. nil leaves them as they are; an
	// empty list removes them.
	Keywords *[]string
}

// IIM datasets read and written here: record, dataset.
var (
	iimCharset     = [2]byte{1, 90}
	iimVersion     = [2]byte{2, 0}
	iimTitle       = [2]byte{2, 5}
	iimKeywords    = [2]byte{2, 25}
	iimDescription = [2]byte{2, 120}
)

// The longest value IIM allows for each dataset written, in bytes.
var iimMax = map[[2]byte]int{iimTitle: 64, iimKeywords: 64, iimDescription: 2000}

// utf8Charset is 1:90's value for UTF-8 (ISO 2022 "ESC % G").
var utf8Charset = []byte("\x1b%G")

// maxIIM caps the IIM data parsed; real data is a few kilobytes.
const maxIIM = 1 << 20

// iimDataset is one dataset: its record and number, and its value.
type iimDataset struct {
	tag   [2]byte
	value []byte
}

// parseIIM reads IIM data: datasets, each a 0x1C tag marker, record,
// dataset number and length (two bytes, or an extended length: the high
// bit set and the count of length bytes that follow).
func parseIIM(b []byte) ([]iimDataset, error) {
	if len(b) > maxIIM {
		return nil, fmt.Errorf("%w: IPTC data of %d bytes", ErrUnsupported, len(b))
	}
	var out []iimDataset
	for i := 0; i < len(b); {
		if b[i] != 0x1C {
			// Writers pad the data (with zeros) to an even length.
			if allZero(b[i:]) {
				break
			}
			return nil, fmt.Errorf("%w: IPTC: no tag marker at %d", ErrMalformed, i)
		}
		if i+5 > len(b) {
			return nil, fmt.Errorf("%w: IPTC: a dataset header past the end", ErrMalformed)
		}
		tag := [2]byte{b[i+1], b[i+2]}
		n := int(binary.BigEndian.Uint16(b[i+3:]))
		i += 5
		if n&0x8000 != 0 {
			k := n & 0x7FFF
			if k == 0 || k > 4 || i+k > len(b) {
				return nil, fmt.Errorf("%w: IPTC: an extended length of %d bytes", ErrMalformed, k)
			}
			n = 0
			for _, c := range b[i : i+k] {
				n = n<<8 | int(c)
			}
			i += k
		}
		if n < 0 || n > len(b)-i {
			return nil, fmt.Errorf("%w: IPTC: dataset %d:%d of %d bytes past the end", ErrMalformed, tag[0], tag[1], n)
		}
		out = append(out, iimDataset{tag: tag, value: b[i : i+n]})
		i += n
	}
	return out, nil
}

func allZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}

func writeIIM(ds []iimDataset) []byte {
	var out []byte
	for _, d := range ds {
		out = append(out, 0x1C, d.tag[0], d.tag[1])
		if len(d.value) < 0x8000 {
			out = binary.BigEndian.AppendUint16(out, uint16(len(d.value)))
		} else {
			out = append(out, 0x80, 4)
			out = binary.BigEndian.AppendUint32(out, uint32(len(d.value)))
		}
		out = append(out, d.value...)
	}
	return out
}

// iimText decodes a value: UTF-8 when 1:90 says so, or when it is valid
// UTF-8 anyway; otherwise Latin-1 (Windows-1252), which ExifTool assumes
// for IIM without a character set.
func iimText(v []byte, isUTF8 bool) string {
	v = bytes.TrimRight(v, "\x00")
	if isUTF8 || utf8.Valid(v) {
		return strings.ToValidUTF8(string(v), "�")
	}
	return decodeCP1252(v)
}

// cp1252 maps Windows-1252's 0x80 to 0x9F; other bytes are Latin-1.
var cp1252 = [32]rune{
	'€', 0x81, '‚', 'ƒ', '„', '…', '†', '‡', 'ˆ', '‰', 'Š', '‹', 'Œ', 0x8D, 'Ž', 0x8F,
	0x90, '‘', '’', '“', '”', '•', '–', '—', '˜', '™', 'š', '›', 'œ', 0x9D, 'ž', 'Ÿ',
}

func decodeCP1252(v []byte) string {
	var sb strings.Builder
	for _, c := range v {
		if c >= 0x80 && c < 0xA0 {
			sb.WriteRune(cp1252[c-0x80])
		} else {
			sb.WriteRune(rune(c))
		}
	}
	return sb.String()
}

func iimIsUTF8(ds []iimDataset) bool {
	for _, d := range ds {
		if d.tag == iimCharset {
			return bytes.Equal(d.value, utf8Charset)
		}
	}
	return false
}

// ParseIPTC reads the fields of IPTC from IPTC-IIM data (the datasets, as
// a JPEG's Photoshop IRB 0x0404 holds them).
func ParseIPTC(iim []byte) (IPTC, error) {
	ds, err := parseIIM(iim)
	if err != nil {
		return IPTC{}, err
	}
	isUTF8 := iimIsUTF8(ds)
	var x IPTC
	for _, d := range ds {
		switch d.tag {
		case iimTitle:
			if x.Title == "" {
				x.Title = iimText(d.value, isUTF8)
			}
		case iimDescription:
			if x.Description == "" {
				x.Description = iimText(d.value, isUTF8)
			}
		case iimKeywords:
			if k := strings.TrimSpace(iimText(d.value, isUTF8)); k != "" {
				x.Keywords = append(x.Keywords, k)
			}
		}
	}
	return x, nil
}

// clip cuts s to at most n bytes at a character boundary.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// EditIPTC returns IPTC-IIM data with edit applied. Every other dataset
// stays, in its place. A value set replaces the dataset where it was; a
// new one goes after the other datasets of the Application Record. Values
// are written as UTF-8, and 1:90 is set to say so, with the other
// datasets re-encoded from Latin-1 where they were; a value longer than
// IIM allows (64 bytes for a title or keyword, 2000 for a description) is
// cut at a character boundary, as IIM readers expect. With empty data,
// EditIPTC makes new data holding the edit. An edit that changes nothing
// returns the data as it was.
func EditIPTC(iim []byte, edit IPTCEdit) ([]byte, error) {
	for _, s := range []*string{edit.Title, edit.Description} {
		if s != nil && !utf8.ValidString(*s) {
			return nil, fmt.Errorf("mediameta: text that isn't UTF-8")
		}
	}
	if edit.Keywords != nil {
		for _, k := range *edit.Keywords {
			if !utf8.ValidString(k) {
				return nil, fmt.Errorf("mediameta: a keyword that isn't UTF-8")
			}
		}
	}
	ds, err := parseIIM(iim)
	if err != nil {
		return nil, err
	}
	before, _ := ParseIPTC(iim)
	same := (edit.Title == nil || clip(*edit.Title, iimMax[iimTitle]) == before.Title) &&
		(edit.Description == nil || clip(*edit.Description, iimMax[iimDescription]) == before.Description) &&
		(edit.Keywords == nil || equalKeywords(*edit.Keywords, before.Keywords))
	if same {
		return bytes.Clone(iim), nil
	}

	// Everything as UTF-8 from here: re-encode the record's text where it
	// wasn't, so that one character set covers it all.
	if !iimIsUTF8(ds) {
		for i, d := range ds {
			if d.tag[0] == 2 && !utf8.Valid(d.value) {
				ds[i].value = []byte(decodeCP1252(d.value))
			}
		}
	}
	ds = setIIM(ds, iimCharset, [][]byte{utf8Charset})
	if edit.Title != nil {
		ds = setIIM(ds, iimTitle, values(*edit.Title, iimMax[iimTitle]))
	}
	if edit.Description != nil {
		ds = setIIM(ds, iimDescription, values(*edit.Description, iimMax[iimDescription]))
	}
	if edit.Keywords != nil {
		var vs [][]byte
		for _, k := range *edit.Keywords {
			if k = clip(strings.TrimSpace(k), iimMax[iimKeywords]); k != "" {
				vs = append(vs, []byte(k))
			}
		}
		ds = setIIM(ds, iimKeywords, vs)
	}
	// The Application Record starts with 2:00, its version (4).
	if !hasRecord(ds, iimVersion) && hasRecord2(ds) {
		ds = setIIM(ds, iimVersion, [][]byte{{0, 4}})
	}
	return writeIIM(ds), nil
}

func equalKeywords(a, b []string) bool {
	var c []string
	for _, k := range a {
		if k = clip(strings.TrimSpace(k), iimMax[iimKeywords]); k != "" {
			c = append(c, k)
		}
	}
	if len(c) != len(b) {
		return false
	}
	for i := range c {
		if c[i] != b[i] {
			return false
		}
	}
	return true
}

func values(s string, max int) [][]byte {
	if s = clip(s, max); s == "" {
		return nil
	}
	return [][]byte{[]byte(s)}
}

func hasRecord(ds []iimDataset, tag [2]byte) bool {
	for _, d := range ds {
		if d.tag == tag {
			return true
		}
	}
	return false
}

func hasRecord2(ds []iimDataset) bool {
	for _, d := range ds {
		if d.tag[0] == 2 {
			return true
		}
	}
	return false
}

// setIIM gives tag the values vs (none removes it): in place of its first
// occurrence, else in order among the other datasets (by record, then
// dataset number, as IIM orders them).
func setIIM(ds []iimDataset, tag [2]byte, vs [][]byte) []iimDataset {
	at := -1
	var out []iimDataset
	for _, d := range ds {
		if d.tag == tag {
			if at < 0 {
				at = len(out)
			}
			continue
		}
		out = append(out, d)
	}
	if at < 0 {
		at = len(out)
		for i, d := range out {
			if d.tag[0] > tag[0] || d.tag[0] == tag[0] && d.tag[1] > tag[1] {
				at = i
				break
			}
		}
	}
	add := make([]iimDataset, len(vs))
	for i, v := range vs {
		add[i] = iimDataset{tag: tag, value: v}
	}
	return append(out[:at], append(add, out[at:]...)...)
}
