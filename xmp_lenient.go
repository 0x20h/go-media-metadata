package mediameta

import (
	"bytes"
	"errors"
	"unicode/utf8"
)

// ErrXMPRepaired is returned by ParseXMP, with the fields it read, for a
// packet that isn't well-formed XML only because of characters XML doesn't
// allow: a control character, a byte that isn't UTF-8, or an "&" that
// starts no entity. Like ExifTool, ParseXMP reads such a packet anyway:
// the control characters and bad bytes become U+FFFD, a stray "&" stays a
// literal "&". EditXMP keeps refusing such a packet, so no write ever
// passes one on.
var ErrXMPRepaired = errors.New("mediameta: XMP with characters XML doesn't allow, read leniently")

// repairXML returns b with what ErrXMPRepaired describes replaced, and whether
// anything was.
func repairXML(b []byte) ([]byte, bool) {
	var out []byte
	changed := false
	for i := 0; i < len(b); {
		c := b[i]
		switch {
		case c == '&':
			if n := entityLen(b[i:]); n > 0 {
				out = append(out, b[i:i+n]...)
				i += n
				continue
			}
			out, changed = append(out, "&amp;"...), true
			i++
		case c < 0x20 && c != '\t' && c != '\n' && c != '\r':
			out, changed = utf8.AppendRune(out, utf8.RuneError), true
			i++
		case c < utf8.RuneSelf:
			out = append(out, c)
			i++
		default:
			r, n := utf8.DecodeRune(b[i:])
			// Valid, and allowed in XML: not a surrogate (DecodeRune
			// rejects those), U+FFFE or U+FFFF.
			if r == utf8.RuneError && n == 1 || r == 0xFFFE || r == 0xFFFF {
				out, changed = utf8.AppendRune(out, utf8.RuneError), true
				i += n
				continue
			}
			out = append(out, b[i:i+n]...)
			i += n
		}
	}
	return out, changed
}

// entityLen is the length of the entity or character reference that b
// starts with ("&amp;", "&#38;", "&#x26;", "&name;"), 0 if it starts none.
// A character reference to a character XML doesn't allow counts as none.
func entityLen(b []byte) int {
	end := bytes.IndexByte(b[:min(len(b), 40)], ';')
	if end < 2 {
		return 0
	}
	ref := b[1:end]
	if ref[0] == '#' {
		code, ok := 0, len(ref) > 1
		digits, base := ref[1:], 10
		if len(digits) > 0 && digits[0] == 'x' {
			digits, base = digits[1:], 16
		}
		for _, d := range digits {
			v := hexDigit(d)
			if v < 0 || v >= base || code > utf8.MaxRune {
				return 0
			}
			code = code*base + v
		}
		if !ok || len(digits) == 0 || !xmlChar(rune(code)) {
			return 0
		}
		return end + 1
	}
	for i, c := range ref {
		letter := c == '_' || c == ':' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= 0x80
		if !letter && (i == 0 || !(c >= '0' && c <= '9' || c == '-' || c == '.')) {
			return 0
		}
	}
	switch string(ref) {
	case "amp", "lt", "gt", "quot", "apos":
		return end + 1
	}
	return 0 // an entity XMP can't declare: not one
}

func hexDigit(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return -1
}

// xmlChar reports whether XML 1.0 allows r (its Char production).
func xmlChar(r rune) bool {
	return r == '\t' || r == '\n' || r == '\r' || r >= 0x20 && r <= 0xD7FF ||
		r >= 0xE000 && r <= 0xFFFD || r >= 0x10000 && r <= utf8.MaxRune
}
