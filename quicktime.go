package mediameta

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/0x20h/go-media-metadata/internal/bmff"
)

// Video is what this package reads from a QuickTime (MOV) or MP4 movie:
// the fields a media library sorts, places and shows videos by. A field
// the file doesn't have, or has in a form this package doesn't accept, is
// left at its zero value.
type Video struct {
	// Created is when the video was recorded. The first of these the file
	// has: the Keys entry com.apple.quicktime.creationdate (a local time
	// with its offset, so OffsetKnown is true), the user data's ©day (the
	// same, when it carries an offset), or the movie header's creation
	// time (in UTC, OffsetKnown true). Zero without any.
	Created     time.Time
	OffsetKnown bool

	// Duration is the movie header's duration.
	Duration time.Duration

	// GPS is where the video was recorded: the Keys entry
	// com.apple.quicktime.location.ISO6709, else the user data's ©xyz.
	// Nil without a valid position.
	GPS *GPS

	// Orientation is how the first video track is shown, from its track
	// header's matrix, in the Exif numbering images use: a rotation by a
	// multiple of 90° or a mirroring. 0 for a matrix that is none of the
	// eight (scaled or skewed), without a video track, or when the movie
	// header has a matrix of its own that turns every track as well.
	Orientation Orientation

	// Width and Height are the first video track's size as stored, before
	// its matrix turns it.
	Width, Height int

	// Make and Model are the recording device's: the Keys entries
	// com.apple.quicktime.make and .model, else the user data's ©mak and
	// ©mod.
	Make, Model string

	// ContentIdentifier is the Keys entry
	// com.apple.quicktime.content.identifier, which pairs a Live Photo's
	// video with its still image (whose Apple MakerNote has the same
	// value).
	ContentIdentifier string
}

// Limits on what a movie read loads into memory: only these small boxes
// are read whole, never sample tables or media data.
const (
	maxLeafBox  = 64 << 10 // mvhd, tkhd, hdlr, an udta entry
	maxKeysIlst = 1 << 20  // a metadata box's keys and ilst
)

// qtBrands are the ftyp major and compatible brands of movies.
var qtBrands = map[string]bool{
	"qt  ": true, "isom": true, "iso2": true, "iso3": true, "iso4": true, "iso5": true, "iso6": true,
	"mp41": true, "mp42": true, "avc1": true, "M4V ": true, "M4VH": true, "M4VP": true, "M4A ": true,
	"3gp4": true, "3gp5": true, "3gp6": true, "3g2a": true, "dash": true, "MSNV": true, "XAVC": true,
}

// qtEpoch is when QuickTime and ISO times count from.
var qtEpoch = time.Date(1904, 1, 1, 0, 0, 0, 0, time.UTC)

// maxQTSeconds is 496 years of seconds: a movie time after 2400 is a
// broken field, not a date.
const maxQTSeconds = 496 * 366 * 86400

// ReadVideo reads the metadata of the QuickTime or MP4 movie in r (size
// bytes). It reads box headers and the few small boxes the fields come
// from: not the media data, and not the sample tables.
func ReadVideo(r io.ReaderAt, size int64) (Video, error) {
	var v Video
	if size < 8 {
		return v, errNotQuickTime
	}
	top, err := bmff.ReadBoxes(r, 0, size)
	if err != nil {
		if errors.Is(err, bmff.ErrMalformed) && !looksLikeMovie(r, size) {
			return v, errNotQuickTime
		}
		return v, err
	}
	if err := checkMovie(r, top); err != nil {
		return v, err
	}
	moov, ok := bmff.Find(top, bmff.T("moov"))
	if !ok {
		return v, fmt.Errorf("%w: no movie box", ErrMalformed)
	}
	kids, err := bmff.ReadBoxes(r, moov.Body(), moov.End())
	if err != nil {
		return v, err
	}
	var keys, udta qtTags
	var mvhdCreated time.Time
	video, movieTurned := false, false
	for _, k := range kids {
		switch k.Type {
		case bmff.T("mvhd"):
			b, err := readBox(r, k, maxLeafBox)
			if err != nil {
				return v, err
			}
			mvhdCreated, v.Duration = parseMvhd(b)
			movieTurned = !mvhdIdentity(b)
		case bmff.T("trak"):
			if video {
				continue // the first video track only
			}
			if video, err = readTrak(r, k, &v); err != nil {
				return v, err
			}
		case bmff.T("meta"):
			t, err := readMetaKeys(r, k)
			if err != nil {
				return v, err
			}
			keys.merge(t)
		case bmff.T("udta"):
			t, err := readUdta(r, k)
			if err != nil {
				return v, err
			}
			udta.merge(t)
		}
	}

	if at, ok := parseISO8601(keys.created); ok {
		v.Created, v.OffsetKnown = at, true
	} else if at, ok := parseISO8601(udta.created); ok {
		v.Created, v.OffsetKnown = at, true
	} else if !mvhdCreated.IsZero() {
		v.Created, v.OffsetKnown = mvhdCreated, true
	}
	if g, ok := parseISO6709(keys.location); ok {
		v.GPS = g
	} else if g, ok := parseISO6709(udta.location); ok {
		v.GPS = g
	}
	v.Make = first(keys.make, udta.make)
	v.Model = first(keys.model, udta.model)
	v.ContentIdentifier = first(keys.contentID, udta.contentID)
	if movieTurned {
		v.Orientation = 0
	}
	return v, nil
}

func first(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// looksLikeMovie reports whether r starts with a box of a movie's
// top-level types, for telling a broken movie from another format.
func looksLikeMovie(r io.ReaderAt, size int64) bool {
	var h [8]byte
	if size < 8 {
		return false
	}
	if _, err := r.ReadAt(h[:], 0); err != nil {
		return false
	}
	switch string(h[4:8]) {
	case "ftyp", "moov", "mdat", "wide", "free", "skip":
		return true
	}
	return false
}

// checkMovie accepts a file whose ftyp has a movie brand, or an old
// QuickTime file without ftyp that has a moov box; it refuses a HEIF
// image (whose ftyp is an image brand).
func checkMovie(r io.ReaderAt, top []bmff.Box) error {
	if top[0].Type != bmff.T("ftyp") {
		_, hasMoov := bmff.Find(top, bmff.T("moov"))
		_, hasMeta := bmff.Find(top, bmff.T("meta"))
		if hasMoov && !hasMeta {
			return nil
		}
		return errNotQuickTime
	}
	ftyp := top[0]
	n := ftyp.BodyLen()
	if n < 8 || n > 1024 {
		return errNotQuickTime
	}
	b := make([]byte, n)
	if _, err := r.ReadAt(b, ftyp.Body()); err != nil {
		return fmt.Errorf("%w: reading ftyp: %v", ErrMalformed, err)
	}
	if qtBrands[string(b[:4])] {
		return nil
	}
	for i := 8; i+4 <= len(b); i += 4 {
		if qtBrands[string(b[i:i+4])] {
			return nil
		}
	}
	return errNotQuickTime
}

// readBox reads a box's payload, at most max bytes long.
func readBox(r io.ReaderAt, b bmff.Box, max int64) ([]byte, error) {
	if b.BodyLen() > max {
		return nil, fmt.Errorf("%w: %v box of %d bytes", ErrMalformed, b.Type, b.BodyLen())
	}
	p := make([]byte, b.BodyLen())
	if _, err := r.ReadAt(p, b.Body()); err != nil {
		return nil, fmt.Errorf("%w: reading %v: %v", ErrMalformed, b.Type, err)
	}
	return p, nil
}

// parseMvhd reads a movie header's creation time (zero for 0) and
// duration.
func parseMvhd(b []byte) (created time.Time, d time.Duration) {
	if len(b) < 4 {
		return
	}
	var secs, scale, dur uint64
	switch b[0] {
	case 0:
		if len(b) < 20 {
			return
		}
		secs = uint64(binary.BigEndian.Uint32(b[4:]))
		scale = uint64(binary.BigEndian.Uint32(b[12:]))
		dur = uint64(binary.BigEndian.Uint32(b[16:]))
		if dur == math.MaxUint32 {
			dur = 0 // unknown
		}
	case 1:
		if len(b) < 32 {
			return
		}
		secs = binary.BigEndian.Uint64(b[4:])
		scale = uint64(binary.BigEndian.Uint32(b[20:]))
		dur = binary.BigEndian.Uint64(b[24:])
		if dur == math.MaxUint64 {
			dur = 0
		}
	default:
		return
	}
	// Up to the year 2400: anything later is a broken field, not a date.
	if secs != 0 && secs < maxQTSeconds {
		created = qtEpoch.Add(time.Duration(secs) * time.Second)
	}
	if scale != 0 && dur/scale < uint64(math.MaxInt64/int64(time.Second)) {
		d = time.Duration(dur/scale)*time.Second + time.Duration(dur%scale)*time.Second/time.Duration(scale)
	}
	return created, d
}

// readTrak reads, for a video track, its size and matrix into v; video
// reports whether it was one.
func readTrak(r io.ReaderAt, trak bmff.Box, v *Video) (video bool, err error) {
	tkhd, ok, err := videoTkhd(r, trak)
	if err != nil || !ok {
		return false, err
	}
	b, err := readBox(r, tkhd, maxLeafBox)
	if err != nil {
		return false, err
	}
	m, w, hgt, ok := tkhdMatrix(b)
	if !ok {
		return false, nil
	}
	v.Orientation = matrixOrientation(m)
	v.Width, v.Height = w, hgt
	return true, nil
}

// videoTkhd is a track's header box, when the track is a video track (its
// media handler is "vide").
func videoTkhd(r io.ReaderAt, trak bmff.Box) (bmff.Box, bool, error) {
	kids, err := bmff.ReadBoxes(r, trak.Body(), trak.End())
	if err != nil {
		return bmff.Box{}, false, err
	}
	tkhd, ok := bmff.Find(kids, bmff.T("tkhd"))
	if !ok {
		return bmff.Box{}, false, nil
	}
	mdia, ok := bmff.Find(kids, bmff.T("mdia"))
	if !ok {
		return bmff.Box{}, false, nil
	}
	mk, err := bmff.ReadBoxes(r, mdia.Body(), mdia.End())
	if err != nil {
		return bmff.Box{}, false, err
	}
	hdlr, ok := bmff.Find(mk, bmff.T("hdlr"))
	if !ok {
		return bmff.Box{}, false, nil
	}
	h, err := readBox(r, hdlr, maxLeafBox)
	if err != nil {
		return bmff.Box{}, false, err
	}
	// FullBox, pre_defined (QuickTime: component type), handler type.
	if len(h) < 12 || string(h[8:12]) != "vide" {
		return bmff.Box{}, false, nil
	}
	return tkhd, true, nil
}

// tkhdMatrixAt is where a track header's matrix starts in its payload,
// for version 0 and 1.
func tkhdMatrixAt(version byte) (int, bool) {
	switch version {
	case 0:
		// FullBox 4, creation 4, modification 4, track ID 4, reserved 4,
		// duration 4, reserved 8, layer 2, alternate group 2, volume 2,
		// reserved 2.
		return 40, true
	case 1:
		// The times and duration are 8 bytes each.
		return 52, true
	}
	return 0, false
}

// tkhdMatrix reads a track header's matrix (a, b, u, c, d, v, x, y, w)
// and its width and height (16.16 fixed point, rounded down).
func tkhdMatrix(b []byte) (m [9]int32, w, h int, ok bool) {
	if len(b) < 1 {
		return
	}
	at, ok := tkhdMatrixAt(b[0])
	if !ok || len(b) < at+36+8 {
		return m, 0, 0, false
	}
	for i := range m {
		m[i] = int32(binary.BigEndian.Uint32(b[at+4*i:]))
	}
	w = int(binary.BigEndian.Uint32(b[at+36:]) >> 16)
	h = int(binary.BigEndian.Uint32(b[at+40:]) >> 16)
	return m, w, h, true
}

// Fixed-point values of a matrix's entries: a, b, c, d (and x, y) are
// 16.16, u, v and w are 2.30.
const (
	fix16One = 1 << 16
	fix30One = 1 << 30
)

// matrices are the track matrices of the eight orientations: a, b, c, d
// in 16.16, mapping a stored point (x, y) to (a·x + c·y, b·x + d·y).
var matrices = map[[4]int32]Orientation{
	{fix16One, 0, 0, fix16One}:   1,
	{-fix16One, 0, 0, fix16One}:  2,
	{-fix16One, 0, 0, -fix16One}: 3,
	{fix16One, 0, 0, -fix16One}:  4,
	{0, fix16One, fix16One, 0}:   5,
	{0, fix16One, -fix16One, 0}:  6,
	{0, -fix16One, -fix16One, 0}: 7,
	{0, -fix16One, fix16One, 0}:  8,
}

// matrixOrientation is the orientation a track matrix shows, 0 for one
// that isn't a rotation or mirroring. The translation is ignored: it only
// moves the turned picture back into view.
func matrixOrientation(m [9]int32) Orientation {
	if m[2] != 0 || m[5] != 0 || m[8] != fix30One {
		return 0
	}
	return matrices[[4]int32{m[0], m[1], m[3], m[4]}]
}

// qtTags are the metadata values a movie's Keys or user data carry.
type qtTags struct {
	created, location, make, model, contentID string
}

// merge fills t's empty fields from o.
func (t *qtTags) merge(o qtTags) {
	if t.created == "" {
		t.created = o.created
	}
	if t.location == "" {
		t.location = o.location
	}
	if t.make == "" {
		t.make = o.make
	}
	if t.model == "" {
		t.model = o.model
	}
	if t.contentID == "" {
		t.contentID = o.contentID
	}
}

// metaChildren reads a meta box's children: in an MP4 it's a FullBox, in
// QuickTime a plain container. It's the FullBox form when a child box
// doesn't start right at the payload.
func metaChildren(r io.ReaderAt, meta bmff.Box) ([]bmff.Box, error) {
	if meta.BodyLen() >= 8 {
		var h [8]byte
		if _, err := r.ReadAt(h[:], meta.Body()); err != nil {
			return nil, fmt.Errorf("%w: reading meta: %v", ErrMalformed, err)
		}
		if string(h[4:8]) == "hdlr" {
			return bmff.ReadBoxes(r, meta.Body(), meta.End())
		}
	}
	if meta.BodyLen() < 4 {
		return nil, nil
	}
	return bmff.ReadBoxes(r, meta.Body()+4, meta.End())
}

// The Keys entries read.
const (
	keyCreated   = "com.apple.quicktime.creationdate"
	keyLocation  = "com.apple.quicktime.location.ISO6709"
	keyMake      = "com.apple.quicktime.make"
	keyModel     = "com.apple.quicktime.model"
	keyContentID = "com.apple.quicktime.content.identifier"
)

// readMetaKeys reads the Keys metadata (handler mdta: keys and ilst) of a
// meta box in moov.
func readMetaKeys(r io.ReaderAt, meta bmff.Box) (qtTags, error) {
	var t qtTags
	kids, err := metaChildren(r, meta)
	if err != nil || kids == nil {
		return t, err
	}
	hb, ok := bmff.Find(kids, bmff.T("hdlr"))
	if !ok {
		return t, nil
	}
	h, err := readBox(r, hb, maxLeafBox)
	if err != nil {
		return t, err
	}
	if len(h) < 12 || string(h[8:12]) != "mdta" {
		return t, nil
	}
	kb, ok1 := bmff.Find(kids, bmff.T("keys"))
	ib, ok2 := bmff.Find(kids, bmff.T("ilst"))
	if !ok1 || !ok2 {
		return t, nil
	}
	keysBody, err := readBox(r, kb, maxKeysIlst)
	if err != nil {
		return t, err
	}
	names := parseKeys(keysBody)
	items, err := bmff.ReadBoxes(r, ib.Body(), ib.End())
	if err != nil {
		return t, err
	}
	for _, it := range items {
		idx := binary.BigEndian.Uint32(it.Type[:])
		if idx == 0 || uint64(idx) > uint64(len(names)) {
			continue
		}
		var dst *string
		switch names[idx-1] {
		case keyCreated:
			dst = &t.created
		case keyLocation:
			dst = &t.location
		case keyMake:
			dst = &t.make
		case keyModel:
			dst = &t.model
		case keyContentID:
			dst = &t.contentID
		default:
			continue
		}
		if *dst != "" {
			continue
		}
		s, err := readDataString(r, it)
		if err != nil {
			return t, err
		}
		*dst = s
	}
	return t, nil
}

// parseKeys reads a keys box's entry names (1-based in ilst), "" for an
// entry it can't read.
func parseKeys(b []byte) []string {
	if len(b) < 8 {
		return nil
	}
	n := binary.BigEndian.Uint32(b[4:])
	// Each entry takes at least 8 bytes.
	if uint64(n) > uint64(len(b)-8)/8 {
		return nil
	}
	names := make([]string, 0, n)
	off := 8
	for range n {
		if off+8 > len(b) {
			break
		}
		sz := int(binary.BigEndian.Uint32(b[off:]))
		if sz < 8 || sz > len(b)-off {
			break
		}
		if string(b[off+4:off+8]) == "mdta" {
			names = append(names, string(b[off+8:off+sz]))
		} else {
			names = append(names, "")
		}
		off += sz
	}
	return names
}

// readDataString reads the text value of an ilst item's first data box:
// UTF-8 (type 1) or UTF-16 (type 2) text. "" for any other type.
func readDataString(r io.ReaderAt, item bmff.Box) (string, error) {
	kids, err := bmff.ReadBoxes(r, item.Body(), item.End())
	if err != nil {
		return "", err
	}
	db, ok := bmff.Find(kids, bmff.T("data"))
	if !ok {
		return "", nil
	}
	b, err := readBox(r, db, maxLeafBox)
	if err != nil {
		return "", err
	}
	// Type indicator (1 reserved byte, 3 bytes of type), locale.
	if len(b) < 8 {
		return "", nil
	}
	typ := binary.BigEndian.Uint32(b) & 0xFFFFFF
	return qtText(b[8:], typ), nil
}

// qtText decodes a value of well-known type 1 (UTF-8) or 2 (UTF-16 BE).
func qtText(b []byte, typ uint32) string {
	switch typ {
	case 1:
		if !utf8.Valid(b) {
			return ""
		}
		return strings.TrimRight(string(b), "\x00")
	case 2:
		if len(b)%2 != 0 {
			return ""
		}
		u := make([]rune, 0, len(b)/2)
		for i := 0; i+1 < len(b); i += 2 {
			u = append(u, rune(binary.BigEndian.Uint16(b[i:])))
		}
		return strings.TrimRight(string(decodeUTF16(u)), "\x00")
	}
	return ""
}

func decodeUTF16(u []rune) []rune {
	out := make([]rune, 0, len(u))
	for i := 0; i < len(u); i++ {
		c := u[i]
		if c >= 0xD800 && c < 0xDC00 && i+1 < len(u) && u[i+1] >= 0xDC00 && u[i+1] < 0xE000 {
			c = (c-0xD800)<<10 | (u[i+1] - 0xDC00) + 0x10000
			i++
		} else if c >= 0xD800 && c < 0xE000 {
			c = utf8.RuneError
		}
		out = append(out, c)
	}
	return out
}

// readUdta reads the QuickTime user data text entries: ©day, ©xyz, ©mak,
// ©mod (the first string of each, of any language).
func readUdta(r io.ReaderAt, udta bmff.Box) (qtTags, error) {
	var t qtTags
	kids, err := bmff.ReadBoxes(r, udta.Body(), udta.End())
	if err != nil {
		// Some writers end udta with a 32-bit zero: tolerate a broken
		// tail, keep what was read.
		return t, nil
	}
	for _, k := range kids {
		var dst *string
		switch string(k.Type[:]) {
		case "\xa9day":
			dst = &t.created
		case "\xa9xyz":
			dst = &t.location
		case "\xa9mak":
			dst = &t.make
		case "\xa9mod":
			dst = &t.model
		case "meta":
			m, err := readMetaKeys(r, k)
			if err != nil {
				return t, err
			}
			t.merge(m)
			continue
		default:
			continue
		}
		if *dst != "" {
			continue
		}
		b, err := readBox(r, k, maxLeafBox)
		if err != nil {
			return t, err
		}
		*dst = udtaString(b)
	}
	return t, nil
}

// udtaString reads a QuickTime international text entry's first string:
// a 16-bit length, a 16-bit language code, then the text (Mac Roman for
// a packed language below 0x400, read as Latin-1 here; else UTF-8).
func udtaString(b []byte) string {
	if len(b) < 4 {
		return ""
	}
	n := int(binary.BigEndian.Uint16(b))
	if n > len(b)-4 {
		return ""
	}
	s := b[4 : 4+n]
	if utf8.Valid(s) {
		return strings.TrimRight(string(s), "\x00")
	}
	r := make([]rune, len(s))
	for i, c := range s {
		r[i] = rune(c)
	}
	return strings.TrimRight(string(r), "\x00")
}

// parseISO8601 reads a date and time with an offset: "2026-11-12T10:15:30
// +0100" in its usual forms (offset with or without a colon, or Z, an
// optional fraction of a second). A time without an offset is refused:
// its zone is unknown.
func parseISO8601(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{
		"2006-01-02T15:04:05.999999999Z07:00",
		"2006-01-02T15:04:05.999999999Z0700",
		"2006-01-02T15:04:05Z07:00",
		"2006-01-02T15:04:05Z0700",
		"2006-01-02T15:04Z07:00",
		"2006-01-02T15:04Z0700",
		"2006-01-02 15:04:05Z07:00",
		"2006-01-02 15:04:05Z0700",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// parseISO6709 reads a point in ISO 6709 string form: latitude and
// longitude, each as degrees (±DD.DD, ±DDD.DD), degrees and minutes
// (±DDMM.MM, ±DDDMM.MM) or degrees, minutes and seconds (±DDMMSS.SS,
// ±DDDMMSS.SS), an optional altitude in metres, an optional CRS, and "/".
func parseISO6709(s string) (*GPS, bool) {
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, "/")
	if i := strings.Index(s, "CRS"); i >= 0 {
		s = s[:i]
	}
	var parts []string
	for i := 0; i < len(s); {
		if s[i] != '+' && s[i] != '-' {
			return nil, false
		}
		j := i + 1
		for j < len(s) && s[j] != '+' && s[j] != '-' {
			j++
		}
		parts = append(parts, s[i:j])
		i = j
	}
	if len(parts) < 2 || len(parts) > 3 {
		return nil, false
	}
	lat, ok := iso6709Angle(parts[0], 2)
	if !ok || math.Abs(lat) > 90 {
		return nil, false
	}
	lon, ok := iso6709Angle(parts[1], 3)
	if !ok || math.Abs(lon) > 180 {
		return nil, false
	}
	g := &GPS{Latitude: lat, Longitude: lon}
	if len(parts) == 3 {
		alt, err := strconv.ParseFloat(parts[2], 64)
		if err != nil || math.IsNaN(alt) || math.IsInf(alt, 0) {
			return nil, false
		}
		g.Altitude, g.HasAltitude = alt, true
	}
	return g, true
}

// iso6709Angle reads one signed angle whose degrees take degDigits digits.
func iso6709Angle(s string, degDigits int) (float64, bool) {
	if len(s) < 2 {
		return 0, false
	}
	sign := 1.0
	if s[0] == '-' {
		sign = -1
	}
	body := s[1:]
	intPart := body
	frac := ""
	if i := strings.IndexByte(body, '.'); i >= 0 {
		intPart, frac = body[:i], body[i:]
	}
	for _, c := range []byte(intPart) {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	if frac != "" {
		for _, c := range []byte(frac[1:]) {
			if c < '0' || c > '9' {
				return 0, false
			}
		}
		if len(frac) == 1 {
			return 0, false
		}
	}
	num := func(s string) float64 {
		f, _ := strconv.ParseFloat(s, 64)
		return f
	}
	var deg float64
	switch len(intPart) {
	case degDigits:
		deg = num(intPart + frac)
	case degDigits + 2:
		min := num(intPart[degDigits:] + frac)
		if min >= 60 {
			return 0, false
		}
		deg = num(intPart[:degDigits]) + min/60
	case degDigits + 4:
		min := num(intPart[degDigits : degDigits+2])
		sec := num(intPart[degDigits+2:] + frac)
		if min >= 60 || sec >= 60 {
			return 0, false
		}
		deg = num(intPart[:degDigits]) + min/60 + sec/3600
	default:
		return 0, false
	}
	return sign * deg, true
}
