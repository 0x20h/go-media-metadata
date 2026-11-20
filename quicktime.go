package mediameta

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/0x20h/go-media-metadata/internal/bmff"
)

// Video is what this package reads from a QuickTime (MOV) or MP4 movie:
// the fields a media library sorts, places and shows videos by. An MPEG-2
// transport stream and a Matroska or WebM file fill some of them; see
// docs/m2ts.md and docs/matroska.md. A field
// the file doesn't have, or has in a form this package doesn't accept, is
// left at its zero value.
type Video struct {
	// Created is when the video was recorded. The first of these the file
	// has: the Keys entry com.apple.quicktime.creationdate (a local time
	// with its offset, so OffsetKnown is true), the user data's ©day (the
	// same, when it carries an offset), Nikon's or Canon's camera tags (a
	// clock time, OffsetKnown false), the movie's XMP (in TakenFrom's
	// order: exif:DateTimeOriginal, photoshop:DateCreated, xmp:CreateDate;
	// OffsetKnown as written), or the movie header's creation time (in
	// UTC, OffsetKnown true). Zero without any.
	Created     time.Time
	OffsetKnown bool

	// MovieCreated is the movie header's (mvhd) creation time on its own,
	// in UTC, as ExifTool's QuickTime:CreateDate gives it: zero when the
	// header has none (0). It's Created's last resort; a caller that dates
	// videos by the movie header reads it here even when Keys or ©day give
	// another date.
	MovieCreated time.Time

	// Duration is the movie header's duration.
	Duration time.Duration

	// GPS is where the video was recorded: the Keys entry
	// com.apple.quicktime.location.ISO6709, else the user data's ©xyz,
	// else its 3GPP loci (as 3GP and Android writers put it), else the
	// movie's XMP (exif:GPSLatitude, exif:GPSLongitude). Nil without a
	// valid position.
	GPS *GPS

	// Orientation is how the first video track is shown, from its track
	// header's matrix, in the Exif numbering images use: a rotation by a
	// multiple of 90° or a mirroring. 0 for a matrix that is none of the
	// eight (scaled or skewed), without a video track, or when the movie
	// header has a matrix of its own that turns every track as well.
	Orientation Orientation

	// Width and Height are the first video track's size as stored, before
	// its matrix turns it; Shown gives it as it's shown.
	Width, Height int

	// Make and Model are the recording device's: the Keys entries
	// com.apple.quicktime.make and .model, else the user data's ©mak and
	// ©mod, else Nikon's NCTG, else the Exif of a Canon movie's thumbnail.
	// An AVI's come from Nikon's nctg chunk.
	Make, Model string
	// LensModel is the Keys entry com.apple.quicktime.camera.lens_model,
	// as iPhones write it ("iPhone 12 Pro back camera 4.2mm f/1.6").
	LensModel string

	// ContentIdentifier is the Keys entry
	// com.apple.quicktime.content.identifier, which pairs a Live Photo's
	// video with its still image (whose Apple MakerNote has the same
	// value).
	ContentIdentifier string

	// Title and Description are the Keys entries
	// com.apple.quicktime.title and .description, else the user data's
	// ©nam and ©des, else an MP4 item list's ©nam and desc.
	Title, Description string
	// Keywords are the Keys entry com.apple.quicktime.keywords (each data
	// value one keyword; a value of several, comma-separated, is split),
	// else an MP4 item list's keyw.
	Keywords []string
	// Rating is the Keys entry com.apple.quicktime.rating.user, -1
	// (rejected) or 0 to 5, when HasRating.
	Rating    int
	HasRating bool
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

// unixEpochQT is 1970-01-01 in seconds since 1904: 66 years and 17 leap
// days.
const unixEpochQT = (66*365 + 17) * 86400

// ReadVideo reads the metadata of the QuickTime or MP4 movie in r (size
// bytes). It reads box headers and the few small boxes the fields come
// from: not the media data, and not the sample tables.
func ReadVideo(r io.ReaderAt, size int64) (Video, error) {
	return readVideoTop(r, size, nil)
}

// readVideoTop is ReadVideo of a movie whose top-level boxes a caller has
// read already (strictly, as bmff.ReadBoxes reads them); nil reads them.
func readVideoTop(r io.ReaderAt, size int64, top []bmff.Box) (Video, error) {
	var v Video
	if size < 8 {
		return v, errNotQuickTime
	}
	// A transport stream is tried once the file isn't a movie, so a
	// movie's read costs no more than it did.
	var err error
	if top == nil {
		top, err = movieBoxes(r, size)
	}
	if err != nil {
		if isTS(r, size) && !looksLikeMovie(r, size) {
			return readTSVideo(r, size)
		}
		if isMatroska(r, size) {
			return readMatroskaVideo(r, size)
		}
		if isAVI(r, size) {
			return readAVIVideo(r, size)
		}
		if isFLV(r, size) {
			return readFLVVideo(r, size)
		}
		if isASF(r, size) {
			return readASFVideo(r, size)
		}
		if isMPEGPS(r, size) {
			return readMPEGPSVideo(r, size)
		}
		if errors.Is(err, bmff.ErrMalformed) && !looksLikeMovie(r, size) {
			return v, errNotQuickTime
		}
		return v, err
	}
	if err := checkMovie(r, top); err != nil {
		if isTS(r, size) && !startsWithFtyp(r, size) {
			return readTSVideo(r, size)
		}
		if isMatroska(r, size) {
			return readMatroskaVideo(r, size)
		}
		if isAVI(r, size) {
			return readAVIVideo(r, size)
		}
		if isFLV(r, size) {
			return readFLVVideo(r, size)
		}
		if isASF(r, size) {
			return readASFVideo(r, size)
		}
		if isMPEGPS(r, size) {
			return readMPEGPSVideo(r, size)
		}
		return v, err
	}
	moov, ok := bmff.Find(top, bmff.T("moov"))
	if !ok {
		return v, fmt.Errorf("%w: no movie box", ErrMalformed)
	}
	// The movie box in one read, when it's at most maxMoovRead: the many
	// small reads of the boxes in it are served from that; reads outside
	// it (an MP4's XMP uuid box) go to the file.
	if s := readSpan(r, moov.Offset, moov.Size); s != nil {
		defer s.release()
		r = s
	}
	kids, err := bmff.ReadBoxes(r, moov.Body(), moov.End())
	if err != nil {
		return v, err
	}
	var keys, udta, canon qtTags
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
			v.MovieCreated = mvhdCreated
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
		case bmff.T("uuid"):
			canon.merge(readCanonUUID(r, k))
		}
	}
	// Canon's tags come after the user data's.
	udta.merge(canon)

	// The movie's XMP, read only when the fields before it are missing.
	// Each source parsed once, in order; the movie's XMP read only when
	// the ones before it have nothing.
	var x XMP
	xRead := false
	if at, ok := parseISO8601(keys.created); ok {
		v.Created, v.OffsetKnown = at, true
	} else if at, ok := parseISO8601(udta.created); ok {
		v.Created, v.OffsetKnown = at, true
	} else if at, _, ok := parseExifDate(udta.nikonDate); ok {
		// Nikon's recording start: the camera's clock time, no offset.
		v.Created, v.OffsetKnown = at, false
	} else if !udta.canonTaken.IsZero() {
		// Canon's: its thumbnail's Exif, a clock time as a rule.
		v.Created, v.OffsetKnown = udta.canonTaken, udta.canonOffsetKnown
	} else if t := TakenFrom(Exif{}, *movieXMPOnce(r, size, top, udta, &x, &xRead), IPTC{}); t.Source != SourceNone {
		v.Created, v.OffsetKnown = t.Time, t.OffsetKnown
	} else if !mvhdCreated.IsZero() {
		v.Created, v.OffsetKnown = mvhdCreated, true
	}
	if g, ok := parseISO6709(keys.location); ok {
		v.GPS = g
	} else if g, ok := parseISO6709(udta.location); ok {
		v.GPS = g
	} else if udta.loci != nil {
		v.GPS = udta.loci
	} else if g := movieXMPOnce(r, size, top, udta, &x, &xRead).GPS; g != nil {
		v.GPS = g
	}
	v.Make = first(keys.make, udta.make)
	v.Model = first(keys.model, udta.model)
	v.LensModel = keys.lensModel
	v.ContentIdentifier = first(keys.contentID, udta.contentID)
	v.Title = first(keys.title, udta.title)
	v.Description = first(keys.description, udta.description)
	v.Keywords = keys.keywords
	if v.Keywords == nil {
		v.Keywords = udta.keywords
	}
	// (Parsed only when there's a rating: ParseFloat's error allocates.)
	if r := first(keys.rating, udta.rating); r != "" {
		if f, err := strconv.ParseFloat(r, 64); err == nil && f == math.Trunc(f) && f >= -1 && f <= 5 {
			v.Rating, v.HasRating = int(f), true
		}
	}
	if movieTurned {
		v.Orientation = 0
	}
	return v, nil
}

// Shown is the size the video is shown at: Width and Height swapped when
// Orientation turns it a quarter (5 to 8), as stored otherwise.
func (v Video) Shown() (width, height int) {
	return ImageSize{Width: v.Width, Height: v.Height, Orientation: v.Orientation}.Shown()
}

func first(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// looksLikeMovie reports whether r starts with a box of a movie's
// top-level types, for telling a broken movie from another format.
// movieBoxes reads a movie's top-level boxes for reading. A movie cut
// short (its mdat runs past the end) or with junk after its last box
// reads as ExifTool reads it: the boxes before, as long as moov is among
// them whole. Writes read the boxes strictly and refuse such a file.
func movieBoxes(r io.ReaderAt, size int64) ([]bmff.Box, error) {
	top, err := bmff.ReadBoxes(r, 0, size)
	if err == nil || !errors.Is(err, bmff.ErrMalformed) || !looksLikeMovie(r, size) {
		return top, err
	}
	lenient, _ := bmff.ReadBoxesLenient(r, 0, size)
	for _, b := range lenient {
		if b.Type == bmff.T("moov") && b.End() <= size {
			// Whole unless it was the box cut to the end.
			if last := lenient[len(lenient)-1]; last.Type != bmff.T("moov") || !truncatedAt(r, last, size) {
				return lenient, nil
			}
		}
	}
	return nil, err
}

// truncatedAt reports whether b's header claims more than reaches size.
func truncatedAt(r io.ReaderAt, b bmff.Box, size int64) bool {
	var h [16]byte
	if _, err := r.ReadAt(h[:8], b.Offset); err != nil {
		return true
	}
	n := int64(binary.BigEndian.Uint32(h[:4]))
	if n == 1 {
		if _, err := r.ReadAt(h[8:16], b.Offset+8); err != nil {
			return true
		}
		n = int64(binary.BigEndian.Uint64(h[8:16]))
	}
	return n != 0 && b.Offset+n > size
}

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
	// Some writers count from 1970 instead of 1904 (Unix time): a value
	// below 1970's in 1904 seconds is read from 1970, as ExifTool reads
	// it, rather than as a date before 1970.
	switch {
	case secs >= unixEpochQT && secs < maxQTSeconds:
		created = qtEpoch.Add(time.Duration(secs) * time.Second)
	case secs != 0 && secs < unixEpochQT:
		created = time.Unix(int64(secs), 0).UTC()
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
	nikonDate                                 string    // Nikon NCTG's DateTimeOriginal
	canonTaken                                time.Time // Canon's thumbnail Exif DateTimeOriginal
	canonOffsetKnown                          bool
	loci                                      *GPS   // 3GPP loci
	xmp                                       []byte // an XMP packet
	title, description, rating                string
	keywords                                  []string
	lensModel                                 string
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
	if t.lensModel == "" {
		t.lensModel = o.lensModel
	}
	if t.nikonDate == "" {
		t.nikonDate = o.nikonDate
	}
	if t.canonTaken.IsZero() {
		t.canonTaken, t.canonOffsetKnown = o.canonTaken, o.canonOffsetKnown
	}
	if t.contentID == "" {
		t.contentID = o.contentID
	}
	if t.loci == nil {
		t.loci = o.loci
	}
	if t.xmp == nil {
		t.xmp = o.xmp
	}
	if t.title == "" {
		t.title = o.title
	}
	if t.description == "" {
		t.description = o.description
	}
	if t.rating == "" {
		t.rating = o.rating
	}
	if t.keywords == nil {
		t.keywords = o.keywords
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
	keyLensModel = "com.apple.quicktime.camera.lens_model"
	keyContentID = "com.apple.quicktime.content.identifier"
	keyTitle     = "com.apple.quicktime.title"
	keyDesc      = "com.apple.quicktime.description"
	keyKeywords  = "com.apple.quicktime.keywords"
	keyRating    = "com.apple.quicktime.rating.user"
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
	if len(h) >= 12 && string(h[8:12]) == "mdir" {
		return readItemList(r, kids)
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
		case keyLensModel:
			dst = &t.lensModel
		case keyContentID:
			dst = &t.contentID
		case keyTitle:
			dst = &t.title
		case keyDesc:
			dst = &t.description
		case keyRating:
			dst = &t.rating
		case keyKeywords:
			if t.keywords == nil {
				vs, err := readDataStrings(r, it)
				if err != nil {
					return t, err
				}
				t.keywords = splitKeywords(vs)
			}
			continue
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
// readItemList reads an MP4 item list (a meta box of handler mdir): ©nam,
// ©des and desc, ©xyz and keyw.
func readItemList(r io.ReaderAt, metaKids []bmff.Box) (qtTags, error) {
	var t qtTags
	ib, ok := bmff.Find(metaKids, bmff.T("ilst"))
	if !ok || ib.BodyLen() > maxKeysIlst {
		return t, nil
	}
	items, err := bmff.ReadBoxes(r, ib.Body(), ib.End())
	if err != nil {
		return t, nil
	}
	for _, it := range items {
		switch string(it.Type[:]) {
		case "\xa9nam", "desc", "\xa9des":
			s, err := readDataString(r, it)
			if err != nil {
				return t, err
			}
			if typ := string(it.Type[:]); typ == "desc" || typ == "\xa9des" {
				t.description = first(t.description, s)
			} else {
				t.title = first(t.title, s)
			}
		case "\xa9xyz":
			// ISO 6709 text, as Android cameras and ExifTool's ItemList
			// GPSCoordinates have it.
			s, err := readDataString(r, it)
			if err != nil {
				return t, err
			}
			t.location = first(t.location, s)
		case "keyw":
			if t.keywords == nil {
				vs, err := readDataStrings(r, it)
				if err != nil {
					return t, err
				}
				t.keywords = splitKeywords(vs)
			}
		}
	}
	return t, nil
}

// splitKeywords is the keywords of data values: each one, or several in
// one value separated by commas, trimmed; nil without any.
func splitKeywords(vs []string) []string {
	var out []string
	for _, v := range vs {
		for _, k := range strings.Split(v, ",") {
			if k = strings.TrimSpace(k); k != "" {
				out = append(out, k)
			}
		}
	}
	return out
}

// readDataStrings reads every data box of an item list item, as text: a
// number (types 21, 22: big-endian integers) as its decimal digits.
func readDataStrings(r io.ReaderAt, item bmff.Box) ([]string, error) {
	kids, err := bmff.ReadBoxes(r, item.Body(), item.End())
	if err != nil {
		return nil, err
	}
	var out []string
	for _, db := range kids {
		if db.Type != bmff.T("data") {
			continue
		}
		b, err := readBox(r, db, maxLeafBox)
		if err != nil {
			return nil, err
		}
		if len(b) < 8 {
			continue
		}
		if s := qtText(b[8:], binary.BigEndian.Uint32(b)&0xFFFFFF); s != "" {
			out = append(out, s)
		}
	}
	return out, nil
}

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

// qtText decodes a value of well-known type 1 (UTF-8) or 2 (UTF-16 BE),
// or a big-endian integer (21 signed, 22 unsigned) as its digits.
func qtText(b []byte, typ uint32) string {
	switch typ {
	case 21, 22:
		if len(b) == 0 || len(b) > 8 {
			return ""
		}
		var u uint64
		for _, c := range b {
			u = u<<8 | uint64(c)
		}
		if typ == 21 && b[0]&0x80 != 0 {
			return strconv.FormatInt(int64(u)-int64(1)<<(8*len(b)), 10)
		}
		return strconv.FormatUint(u, 10)
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
	// The item list (a meta box in udta) comes first, the user data text
	// entries after it, whatever their order in the file.
	var t, items qtTags
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
		case "loci":
			if t.loci == nil {
				b, err := readBox(r, k, maxLeafBox)
				if err != nil {
					return t, err
				}
				t.loci = parseLoci(b)
			}
			continue
		case "XMP_":
			if t.xmp == nil {
				b, err := readBox(r, k, maxXMP)
				if err != nil {
					return t, err
				}
				t.xmp = b
			}
			continue
		case "\xa9nam":
			dst = &t.title
		case "\xa9des":
			dst = &t.description
		case "NCDT":
			// Nikon's tags: an NCTG box in NCDT (see parseNCTG).
			if err := readNCDT(r, k, &t); err != nil {
				return t, err
			}
			continue
		case "titl", "dscp":
			// 3GPP strings (TS 26.244 8.1), as ExifTool writes UserData
			// Title and Description in an MP4.
			s := &t.title
			if k.Type == bmff.T("dscp") {
				s = &t.description
			}
			if *s == "" {
				b, err := readBox(r, k, maxLeafBox)
				if err != nil {
					return t, err
				}
				*s = gppString(b)
			}
			continue
		case "meta":
			m, err := readMetaKeys(r, k)
			if err != nil {
				return t, err
			}
			items.merge(m)
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
	items.merge(t)
	return items, nil
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
// longitude, each as degrees (±DD.DD, ±DDD.DD, or with fewer integer
// digits, as some writers have it: ±D.DD), degrees and minutes
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
	switch n := len(intPart); {
	case n >= 1 && n <= degDigits:
		// Degrees. Some writers leave out ISO 6709's leading zeros
		// (Android's "+8.0630" for "+008.0630"); ExifTool reads them too.
		deg = num(intPart + frac)
	case n == degDigits+2:
		min := num(intPart[degDigits:] + frac)
		if min >= 60 {
			return 0, false
		}
		deg = num(intPart[:degDigits]) + min/60
	case n == degDigits+4:
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

// parseLoci reads a 3GPP location information box's payload (3GPP TS
// 26.244 8.10): version and flags, a language, the place's name (UTF-8
// up to a NUL, or UTF-16 after a byte order mark up to a 16-bit NUL), a
// role byte, then longitude, latitude and altitude as signed 16.16 fixed
// point. Nil when it's cut short or out of range.
func parseLoci(b []byte) *GPS {
	if len(b) < 6 {
		return nil
	}
	p := b[6:]
	if len(p) >= 2 && p[0] == 0xFE && p[1] == 0xFF {
		i := 2
		for ; i+1 < len(p) && (p[i] != 0 || p[i+1] != 0); i += 2 {
		}
		p = p[min(i+2, len(p)):]
	} else {
		i := bytes.IndexByte(p, 0)
		if i < 0 {
			return nil
		}
		p = p[i+1:]
	}
	if len(p) < 13 {
		return nil
	}
	fixed := func(b []byte) float64 { return float64(int32(binary.BigEndian.Uint32(b))) / 65536 }
	g := &GPS{Longitude: fixed(p[1:]), Latitude: fixed(p[5:]), Altitude: fixed(p[9:]), HasAltitude: true}
	if math.Abs(g.Latitude) > 90 || math.Abs(g.Longitude) > 180 {
		return nil
	}
	return g
}

// gppString reads a 3GPP asset string box's text (TS 26.244 8.1): a full
// box header, a pad bit and packed language, then UTF-8 up to a NUL, or
// UTF-16 after a byte order mark.
func gppString(b []byte) string {
	if len(b) < 6 {
		return ""
	}
	p := b[6:]
	if len(p) >= 2 && p[0] == 0xFE && p[1] == 0xFF {
		p = p[2:]
		i := 0
		for ; i+1 < len(p) && (p[i] != 0 || p[i+1] != 0); i += 2 {
		}
		return strings.TrimSpace(qtText(p[:i], 2))
	}
	if i := bytes.IndexByte(p, 0); i >= 0 {
		p = p[:i]
	}
	return strings.TrimSpace(qtText(p, 1))
}

// xmpUUID is the uuid box type of an MP4's XMP packet.
var xmpUUID = []byte{0xBE, 0x7A, 0xCF, 0xCB, 0x97, 0xA9, 0x42, 0xE8, 0x9C, 0x71, 0x99, 0x94, 0x91, 0xE3, 0xAF, 0xAC}

// movieXMP reads a movie's XMP packet: a top-level uuid box of xmpUUID (as
// MP4 files have it), else the user data's XMP_ (QuickTime). Nil without
// one.
func movieXMP(r io.ReaderAt, size int64, top []bmff.Box, udta qtTags) ([]byte, error) {
	for _, b := range top {
		// The box's header ends with its 16-byte extended type.
		if b.Type != bmff.T("uuid") || b.HeaderLen < 24 {
			continue
		}
		var id [16]byte
		if _, err := r.ReadAt(id[:], b.Body()-16); err != nil {
			return nil, fmt.Errorf("%w: reading uuid: %v", ErrMalformed, err)
		}
		if !bytes.Equal(id[:], xmpUUID) {
			continue
		}
		if b.BodyLen() > maxXMP {
			return nil, fmt.Errorf("%w: XMP of %d bytes", ErrUnsupported, b.BodyLen())
		}
		p := make([]byte, b.BodyLen())
		if _, err := r.ReadAt(p, b.Body()); err != nil {
			return nil, fmt.Errorf("%w: reading XMP: %v", ErrMalformed, err)
		}
		return p, nil
	}
	return udta.xmp, nil
}

// readMovieXMP reads a movie's XMP packet, nil without one.
func readMovieXMP(r io.ReaderAt, size int64) ([]byte, error) {
	top, err := movieBoxes(r, size)
	if err != nil {
		return nil, err
	}
	var udta qtTags
	if moov, ok := bmff.Find(top, bmff.T("moov")); ok {
		kids, err := bmff.ReadBoxes(r, moov.Body(), moov.End())
		if err != nil {
			return nil, err
		}
		if u, ok := bmff.Find(kids, bmff.T("udta")); ok {
			if udta, err = readUdta(r, u); err != nil {
				return nil, err
			}
		}
	}
	return movieXMP(r, size, top, udta)
}

// movieXMPOnce parses a movie's XMP into x the first time it's called
// (read false), and returns x: empty without a packet, or with one it
// can't read.
func movieXMPOnce(r io.ReaderAt, size int64, top []bmff.Box, udta qtTags, x *XMP, read *bool) *XMP {
	if !*read {
		*read = true
		if packet, err := movieXMP(r, size, top, udta); err == nil && packet != nil {
			*x, _ = ParseXMP(packet)
		}
	}
	return x
}

// canonUUID is the uuid of the box in moov where Canon cameras keep their
// tags (CNCV, CNTH, as ExifTool documents them).
var canonUUID = []byte{0x85, 0xC0, 0xB6, 0x87, 0x82, 0x0F, 0x11, 0xE0, 0x81, 0x11, 0xF4, 0xCE, 0x46, 0x2B, 0x6A, 0x48}

// maxCanonThumb caps the thumbnail read for its Exif.
const maxCanonThumb = 1 << 20

// readCanonUUID reads the make, model and date taken of a Canon movie: the
// Exif of the thumbnail JPEG in the CNDA box, the one box of CNTH (what
// follows it there is padding, not boxes). Anything else, or a box that
// doesn't parse, gives nothing.
func readCanonUUID(r io.ReaderAt, b bmff.Box) qtTags {
	var t qtTags
	if b.HeaderLen < 24 {
		return t
	}
	var id [16]byte
	if _, err := r.ReadAt(id[:], b.Body()-16); err != nil || !bytes.Equal(id[:], canonUUID) {
		return t
	}
	kids, _ := bmff.ReadBoxesLenient(r, b.Body(), b.End())
	cnth, ok := bmff.Find(kids, bmff.T("CNTH"))
	if !ok {
		return t
	}
	inner, _ := bmff.ReadBoxesLenient(r, cnth.Body(), cnth.End())
	if len(inner) == 0 || inner[0].Type != bmff.T("CNDA") {
		return t
	}
	jpeg, err := readBox(r, inner[0], maxCanonThumb)
	if err != nil {
		return t
	}
	x, err := ReadExif(bytes.NewReader(jpeg), int64(len(jpeg)))
	if err != nil {
		return t
	}
	t.make, t.model = strings.TrimSpace(x.Camera.Make), strings.TrimSpace(x.Camera.Model)
	t.canonTaken, t.canonOffsetKnown = x.DateTimeOriginal, x.OffsetKnown
	return t
}

// readNCDT reads the NCTG box in a Nikon NCDT box of the user data: its
// make, model and recording start (into t where they're unset).
func readNCDT(r io.ReaderAt, ncdt bmff.Box, t *qtTags) error {
	kids, err := bmff.ReadBoxes(r, ncdt.Body(), ncdt.End())
	if err != nil {
		return nil // a broken NCDT is skipped, as other user data is
	}
	b, ok := bmff.Find(kids, bmff.T("NCTG"))
	if !ok {
		return nil
	}
	data, err := readBox(r, b, maxLeafBox)
	if err != nil {
		return err
	}
	make_, model, dto := parseNCTG(data)
	t.make, t.model = first(t.make, make_), first(t.model, model)
	t.nikonDate = first(t.nikonDate, dto)
	return nil
}

// parseNCTG reads Nikon's NCTG records (as ExifTool documents its Nikon
// NCTG tags): each a 32-bit tag, a 16-bit Exif format code, a 16-bit
// count, then the value, big-endian. Make (1), Model (2) and
// DateTimeOriginal (0x12, the recording's start: Exif date text, the
// camera's clock time) are read.
func parseNCTG(b []byte) (make_, model, dto string) {
	for pos := 0; pos+8 <= len(b); {
		tag := binary.BigEndian.Uint32(b[pos:])
		typ := binary.BigEndian.Uint16(b[pos+4:])
		count := int(binary.BigEndian.Uint16(b[pos+6:]))
		size := typeSize(typ) * count
		pos += 8
		if typeSize(typ) == 0 || size > len(b)-pos {
			return
		}
		if typ == typeASCII {
			s, _ := ascii(field{typ: typ, count: count, value: b[pos : pos+size]})
			switch tag {
			case 0x01:
				make_ = s
			case 0x02:
				model = s
			case 0x12:
				dto = s
			}
		}
		pos += size
	}
	return
}

// maxMoovRead is the largest movie box ReadVideo reads in one call; a
// larger one (long movies' sample tables) is read box by box.
const maxMoovRead = 1 << 20

// spanReader serves reads within [base, base+len(b)) from b, read in one
// call, and the others from r. Its buffer is pooled: nothing read through
// it keeps a slice of it.
type spanReader struct {
	r    io.ReaderAt
	base int64
	b    []byte
}

var spanReaders = sync.Pool{New: func() any { return new(spanReader) }}

// readSpan reads [at, at+n) of r into a pooled spanReader; nil when n is
// past maxMoovRead or the read fails (r is then read as before).
func readSpan(r io.ReaderAt, at, n int64) *spanReader {
	if n <= 0 || n > maxMoovRead {
		return nil
	}
	s := spanReaders.Get().(*spanReader)
	if int64(cap(s.b)) < n {
		s.b = make([]byte, n)
	}
	s.b = s.b[:n]
	if _, err := r.ReadAt(s.b, at); err != nil {
		s.b = s.b[:0]
		spanReaders.Put(s)
		return nil
	}
	s.r, s.base = r, at
	return s
}

func (s *spanReader) ReadAt(p []byte, off int64) (int, error) {
	if off >= s.base && off+int64(len(p)) <= s.base+int64(len(s.b)) {
		return copy(p, s.b[off-s.base:]), nil
	}
	return s.r.ReadAt(p, off)
}

func (s *spanReader) release() {
	s.r, s.b = nil, s.b[:0]
	spanReaders.Put(s)
}
