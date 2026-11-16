package mediameta

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"strings"
	"time"
	"unicode/utf8"
)

// Matroska (.mkv) and WebM (.webm): EBML (RFC 8794) elements, each an ID,
// a size and a body, nested. Read only. The fields come from three of the
// Segment's top-level elements, Info, Tracks and Tags, found by walking the
// Segment up to its first Cluster and, for those that come later, through
// the SeekHead. Clusters (the media data) are never read.

// EBML and Matroska element IDs (RFC 8794, RFC 9559), marker bits kept.
const (
	ebmlHeader     = 0x1A45DFA3
	ebmlDocType    = 0x4282
	ebmlVoid       = 0xEC
	mkvSegment     = 0x18538067
	mkvSeekHead    = 0x114D9B74
	mkvSeek        = 0x4DBB
	mkvSeekID      = 0x53AB
	mkvSeekPos     = 0x53AC
	mkvInfo        = 0x1549A966
	mkvScale       = 0x2AD7B1
	mkvDuration    = 0x4489
	mkvDateUTC     = 0x4461
	mkvTitle       = 0x7BA9
	mkvTracks      = 0x1654AE6B
	mkvTrackEntry  = 0xAE
	mkvTrackType   = 0x83
	mkvVideo       = 0xE0
	mkvPixelWidth  = 0xB0
	mkvPixelHeight = 0xBA
	mkvProjection  = 0x7670
	mkvProjType    = 0x7671
	mkvPoseYaw     = 0x7673
	mkvPosePitch   = 0x7674
	mkvPoseRoll    = 0x7675
	mkvTags        = 0x1254C367
	mkvTag         = 0x7373
	mkvTargets     = 0x63C0
	mkvTargetType  = 0x68CA
	mkvTagTrackUID = 0x63C5
	mkvTagEdition  = 0x63C9
	mkvTagChapter  = 0x63C4
	mkvTagAttached = 0x63C6
	mkvSimpleTag   = 0x67C8
	mkvTagName     = 0x45A3
	mkvTagString   = 0x4487
	mkvCluster     = 0x1F43B675
)

// Limits: the elements read whole, and how far a walk goes.
const (
	maxEBMLHeader   = 4 << 10
	maxMKVSeekHead  = 64 << 10
	maxMKVInfo      = 64 << 10
	maxMKVTracks    = 1 << 20
	maxMKVTags      = 1 << 20
	maxMKVTopLevel  = 4096 // Segment children walked before the first Cluster
	trackTypeVideo  = 1
	targetTypeMovie = 50
)

const errNotMatroska = formatError("Matroska or WebM file")

// errMatroskaReadOnly is the error of a write to a Matroska or WebM file.
var errMatroskaReadOnly = fmt.Errorf("%w: a Matroska or WebM file is read only", ErrUnsupported)

// mkvEpoch is when DateUTC counts from.
var mkvEpoch = time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)

// ebmlVint reads a variable-length integer: its value and its length. An
// ID keeps its marker bit; a size loses it, and a size of all ones is
// unknown (ok, unknown true).
func ebmlVint(b []byte, id bool) (v uint64, n int, unknown, ok bool) {
	if len(b) == 0 || b[0] == 0 {
		return 0, 0, false, false
	}
	n = 1
	for mask := byte(0x80); b[0]&mask == 0; mask >>= 1 {
		n++
	}
	if len(b) < n || id && n > 4 {
		return 0, 0, false, false
	}
	first := uint64(b[0])
	if !id {
		first &= uint64(0xFF) >> n
	}
	v = first
	all := first == uint64(0xFF)>>n
	for _, c := range b[1:n] {
		v = v<<8 | uint64(c)
		all = all && c == 0xFF
	}
	return v, n, !id && all, true
}

// ebmlElement is an element's header: its ID, where its body starts, and
// its size (-1 when unknown).
type ebmlElement struct {
	id         uint32
	body, size int64
}

func (e ebmlElement) end() int64 { return e.body + e.size }

// readEBMLHeader reads the element header at off, before end.
func readEBMLHeader(r io.ReaderAt, off, end int64) (ebmlElement, error) {
	var h [12]byte
	n := min(int64(len(h)), end-off)
	if n < 2 {
		return ebmlElement{}, fmt.Errorf("%w: EBML element header cut short at %d", ErrMalformed, off)
	}
	if _, err := r.ReadAt(h[:n], off); err != nil {
		return ebmlElement{}, fmt.Errorf("%w: EBML element header at %d: %v", ErrMalformed, off, err)
	}
	id, in, _, ok := ebmlVint(h[:n], true)
	if !ok {
		return ebmlElement{}, fmt.Errorf("%w: an EBML element ID at %d", ErrMalformed, off)
	}
	size, sn, unknown, ok := ebmlVint(h[in:n], false)
	if !ok {
		return ebmlElement{}, fmt.Errorf("%w: an EBML element size at %d", ErrMalformed, off)
	}
	e := ebmlElement{id: uint32(id), body: off + int64(in+sn), size: -1}
	if !unknown {
		if size > uint64(end-e.body) {
			return ebmlElement{}, fmt.Errorf("%w: EBML element 0x%X at %d runs past its parent", ErrMalformed, id, off)
		}
		e.size = int64(size)
	}
	return e, nil
}

// readEBMLBody reads an element's body whole, at most limit bytes.
func readEBMLBody(r io.ReaderAt, e ebmlElement, limit int64) ([]byte, error) {
	if e.size < 0 {
		return nil, fmt.Errorf("%w: EBML element 0x%X of unknown size", ErrMalformed, e.id)
	}
	if e.size > limit {
		return nil, fmt.Errorf("%w: EBML element 0x%X of %d bytes, more than %d", ErrMalformed, e.id, e.size, limit)
	}
	b := make([]byte, e.size)
	if _, err := r.ReadAt(b, e.body); err != nil {
		return nil, fmt.Errorf("%w: EBML element 0x%X: %v", ErrMalformed, e.id, err)
	}
	return b, nil
}

// ebmlChildren calls fn with each child element in b, a body read whole.
func ebmlChildren(b []byte, fn func(id uint32, body []byte) error) error {
	for len(b) > 0 {
		id, in, _, ok := ebmlVint(b, true)
		if !ok {
			return fmt.Errorf("%w: an EBML element ID", ErrMalformed)
		}
		size, sn, unknown, ok := ebmlVint(b[in:], false)
		if !ok || unknown || size > uint64(len(b)-in-sn) {
			return fmt.Errorf("%w: the size of EBML element 0x%X", ErrMalformed, id)
		}
		body := b[in+sn : in+sn+int(size)]
		if err := fn(uint32(id), body); err != nil {
			return err
		}
		b = b[in+sn+int(size):]
	}
	return nil
}

// ebmlUint reads an unsigned integer element: 0 to 8 bytes, big-endian.
func ebmlUint(b []byte) (uint64, error) {
	if len(b) > 8 {
		return 0, fmt.Errorf("%w: an EBML integer of %d bytes", ErrMalformed, len(b))
	}
	var v uint64
	for _, c := range b {
		v = v<<8 | uint64(c)
	}
	return v, nil
}

// ebmlFloat reads a float element: 0, 4 or 8 bytes.
func ebmlFloat(b []byte) (float64, error) {
	switch len(b) {
	case 0:
		return 0, nil
	case 4:
		return float64(math.Float32frombits(binary.BigEndian.Uint32(b))), nil
	case 8:
		return math.Float64frombits(binary.BigEndian.Uint64(b)), nil
	}
	return 0, fmt.Errorf("%w: an EBML float of %d bytes", ErrMalformed, len(b))
}

// ebmlString reads a string element: UTF-8, maybe padded with zero bytes.
// "" for one that isn't valid UTF-8.
func ebmlString(b []byte) string {
	s := strings.TrimRight(string(b), "\x00")
	if !utf8.ValidString(s) {
		return ""
	}
	return strings.TrimSpace(s)
}

// matroskaHeader reads the EBML header at the start of r: the offset after
// it, and whether its DocType is "matroska" or "webm" (Matroska's default
// without one).
func matroskaHeader(r io.ReaderAt, size int64) (int64, bool) {
	var magic [4]byte
	if size < 8 {
		return 0, false
	}
	if _, err := r.ReadAt(magic[:], 0); err != nil || binary.BigEndian.Uint32(magic[:]) != ebmlHeader {
		return 0, false
	}
	e, err := readEBMLHeader(r, 0, size)
	if err != nil {
		return 0, false
	}
	b, err := readEBMLBody(r, e, maxEBMLHeader)
	if err != nil {
		return 0, false
	}
	doc := "matroska"
	if ebmlChildren(b, func(id uint32, body []byte) error {
		if id == ebmlDocType {
			doc = strings.TrimRight(string(body), "\x00")
		}
		return nil
	}) != nil {
		return 0, false
	}
	return e.end(), doc == "matroska" || doc == "webm"
}

func isMatroska(r io.ReaderAt, size int64) bool {
	_, ok := matroskaHeader(r, size)
	return ok
}

// mkvRead is what a read has found so far.
type mkvRead struct {
	v                            Video
	info, tracks, tags, seekHead bool
	tagTitle                     string
	seeks                        map[uint32]int64 // element ID → offset from the Segment's body
}

// readMatroskaVideo reads Video from a Matroska or WebM file: from Info the
// duration, DateUTC and title, from the first video track its size and
// rotation, and from Tags the movie's TITLE when Info has none.
func readMatroskaVideo(r io.ReaderAt, size int64) (Video, error) {
	off, ok := matroskaHeader(r, size)
	if !ok {
		return Video{}, errNotMatroska
	}
	// The Segment, after any Void elements.
	var seg ebmlElement
	for {
		e, err := readEBMLHeader(r, off, size)
		if err != nil {
			return Video{}, err
		}
		if e.id == mkvSegment {
			seg = e
			break
		}
		if e.id != ebmlVoid || e.size < 0 {
			return Video{}, fmt.Errorf("%w: EBML element 0x%X before the Segment", ErrMalformed, e.id)
		}
		off = e.end()
	}
	segEnd := size
	if seg.size >= 0 {
		segEnd = seg.end()
	}
	m := mkvRead{seeks: map[uint32]int64{}}
	// The Segment's children up to the first Cluster: Info and Tracks come
	// first in every muxer's output; Tags often come after the Clusters.
	at := seg.body
	for n := 0; at < segEnd && n < maxMKVTopLevel; n++ {
		e, err := readEBMLHeader(r, at, segEnd)
		if err != nil {
			return Video{}, err
		}
		if e.id == mkvCluster {
			break
		}
		if err := m.element(r, e); err != nil {
			return Video{}, err
		}
		if e.size < 0 {
			break // an unknown-size element other than a Cluster: no further
		}
		at = e.end()
	}
	// What the walk didn't reach, through the SeekHead (and a second
	// SeekHead it points to).
	for range 2 {
		for _, id := range []uint32{mkvInfo, mkvTracks, mkvTags, mkvSeekHead} {
			pos, ok := m.seeks[id]
			if !ok || m.has(id) {
				continue
			}
			delete(m.seeks, id)
			if pos < 0 || pos >= segEnd-seg.body {
				continue
			}
			e, err := readEBMLHeader(r, seg.body+pos, segEnd)
			if err != nil || e.id != id {
				continue // a stale seek entry: the element isn't there
			}
			if err := m.element(r, e); err != nil {
				return Video{}, err
			}
		}
	}
	if !m.info {
		return Video{}, fmt.Errorf("%w: a Matroska segment without its Info", ErrMalformed)
	}
	if m.v.Title == "" {
		m.v.Title = m.tagTitle
	}
	return m.v, nil
}

func (m *mkvRead) has(id uint32) bool {
	switch id {
	case mkvInfo:
		return m.info
	case mkvTracks:
		return m.tracks
	case mkvTags:
		return m.tags
	case mkvSeekHead:
		return m.seekHead
	}
	return false
}

// element reads one of the Segment's children, if it is one of those the
// fields come from and the first of its kind.
func (m *mkvRead) element(r io.ReaderAt, e ebmlElement) error {
	var limit int64
	var parse func([]byte) error
	switch {
	case e.id == mkvInfo && !m.info:
		limit, parse, m.info = maxMKVInfo, m.parseInfo, true
	case e.id == mkvTracks && !m.tracks:
		limit, parse, m.tracks = maxMKVTracks, m.parseTracks, true
	case e.id == mkvTags && !m.tags:
		limit, parse, m.tags = maxMKVTags, m.parseTags, true
	case e.id == mkvSeekHead && !m.seekHead:
		limit, parse, m.seekHead = maxMKVSeekHead, m.parseSeekHead, true
	default:
		return nil
	}
	b, err := readEBMLBody(r, e, limit)
	if err != nil {
		return err
	}
	return parse(b)
}

func (m *mkvRead) parseSeekHead(b []byte) error {
	return ebmlChildren(b, func(id uint32, body []byte) error {
		if id != mkvSeek {
			return nil
		}
		var target uint32
		pos := int64(-1)
		err := ebmlChildren(body, func(id uint32, v []byte) error {
			switch id {
			case mkvSeekID:
				t, _, _, ok := ebmlVint(v, true)
				if ok {
					target = uint32(t)
				}
			case mkvSeekPos:
				p, err := ebmlUint(v)
				if err != nil {
					return err
				}
				if p <= math.MaxInt64 {
					pos = int64(p)
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		if _, seen := m.seeks[target]; target != 0 && pos >= 0 && !seen {
			m.seeks[target] = pos
		}
		return nil
	})
}

func (m *mkvRead) parseInfo(b []byte) error {
	scale := uint64(1_000_000) // TimestampScale's default: milliseconds
	var duration float64
	var hasDuration bool
	err := ebmlChildren(b, func(id uint32, body []byte) error {
		var err error
		switch id {
		case mkvScale:
			if scale, err = ebmlUint(body); err != nil {
				return err
			}
		case mkvDuration:
			if duration, err = ebmlFloat(body); err != nil {
				return err
			}
			hasDuration = true
		case mkvDateUTC:
			if len(body) != 8 {
				return fmt.Errorf("%w: a DateUTC of %d bytes", ErrMalformed, len(body))
			}
			ns := int64(binary.BigEndian.Uint64(body))
			m.v.Created, m.v.OffsetKnown = mkvEpoch.Add(time.Duration(ns)), true
		case mkvTitle:
			m.v.Title = ebmlString(body)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if d := duration * float64(scale); hasDuration && scale > 0 && d >= 0 && d < math.MaxInt64 {
		m.v.Duration = time.Duration(d)
	}
	return nil
}

func (m *mkvRead) parseTracks(b []byte) error {
	found := false
	return ebmlChildren(b, func(id uint32, entry []byte) error {
		if id != mkvTrackEntry || found {
			return nil
		}
		var kind uint64
		var video []byte
		err := ebmlChildren(entry, func(id uint32, body []byte) error {
			var err error
			switch id {
			case mkvTrackType:
				kind, err = ebmlUint(body)
			case mkvVideo:
				video = body
			}
			return err
		})
		if err != nil || kind != trackTypeVideo {
			return err
		}
		found = true
		return m.parseVideo(video)
	})
}

// parseVideo reads a video track's Video element: its size, and its
// rotation from a rectangular Projection's roll.
func (m *mkvRead) parseVideo(b []byte) error {
	var w, h uint64
	o := Orientation(1)
	err := ebmlChildren(b, func(id uint32, body []byte) error {
		var err error
		switch id {
		case mkvPixelWidth:
			w, err = ebmlUint(body)
		case mkvPixelHeight:
			h, err = ebmlUint(body)
		case mkvProjection:
			o, err = projectionOrientation(body)
		}
		return err
	})
	if err != nil {
		return err
	}
	if w > 0 && h > 0 && w <= math.MaxInt32 && h <= math.MaxInt32 {
		m.v.Width, m.v.Height = int(w), int(h)
	}
	m.v.Orientation = o
	return nil
}

// projectionOrientation turns a Projection into an orientation: a roll (a
// counter-clockwise turn, in degrees) by a multiple of 90° on a
// rectangular picture. 0 for any other projection.
func projectionOrientation(b []byte) (Orientation, error) {
	var kind uint64
	var yaw, pitch, roll float64
	err := ebmlChildren(b, func(id uint32, body []byte) error {
		var err error
		switch id {
		case mkvProjType:
			kind, err = ebmlUint(body)
		case mkvPoseYaw:
			yaw, err = ebmlFloat(body)
		case mkvPosePitch:
			pitch, err = ebmlFloat(body)
		case mkvPoseRoll:
			roll, err = ebmlFloat(body)
		}
		return err
	})
	if err != nil {
		return 0, err
	}
	if kind != 0 || yaw != 0 || pitch != 0 {
		return 0, nil
	}
	switch math.Mod(math.Mod(-roll, 360)+360, 360) { // clockwise
	case 0:
		return 1, nil
	case 90:
		return 6, nil
	case 180:
		return 3, nil
	case 270:
		return 8, nil
	}
	return 0, nil
}

// parseTags keeps the first TITLE of a tag that targets the whole file: no
// track, edition, chapter or attachment, at the movie level (50, the
// default).
func (m *mkvRead) parseTags(b []byte) error {
	return ebmlChildren(b, func(id uint32, tag []byte) error {
		if id != mkvTag || m.tagTitle != "" {
			return nil
		}
		whole, title := true, ""
		err := ebmlChildren(tag, func(id uint32, body []byte) error {
			switch id {
			case mkvTargets:
				return ebmlChildren(body, func(id uint32, v []byte) error {
					switch id {
					case mkvTargetType, mkvTagTrackUID, mkvTagEdition, mkvTagChapter, mkvTagAttached:
					default:
						return nil
					}
					t, err := ebmlUint(v)
					if err != nil {
						return err
					}
					// A UID other than 0 makes the tag about a part.
					if id == mkvTargetType && t != targetTypeMovie || id != mkvTargetType && t != 0 {
						whole = false
					}
					return nil
				})
			case mkvSimpleTag:
				var name, value string
				if err := ebmlChildren(body, func(id uint32, v []byte) error {
					switch id {
					case mkvTagName:
						name = ebmlString(v)
					case mkvTagString:
						value = ebmlString(v)
					}
					return nil
				}); err != nil {
					return err
				}
				if title == "" && strings.EqualFold(name, "TITLE") {
					title = value
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		if whole {
			m.tagTitle = title
		}
		return nil
	})
}
