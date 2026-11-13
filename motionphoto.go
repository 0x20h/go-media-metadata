package mediameta

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"
)

// EmbeddedMedia is media a JPEG file carries after its own image: a
// Motion Photo's video, or a secondary image such as an HDR gain map or a
// depth map.
type EmbeddedMedia struct {
	// Semantic is what the media is: the XMP container item's semantic
	// ("MotionPhoto", "GainMap", "Depth", …), "MicroVideo" for a video
	// declared the older way, or "MPF" for an image only the Multi-Picture
	// Format index lists.
	Semantic string
	// MIME is the media's type, where the file declares it.
	MIME string
	// Offset and Length are where the media is in the file.
	Offset, Length int64
}

// Embedded is what ReadEmbedded finds.
type Embedded struct {
	// Video is a Motion Photo's video, nil without one.
	Video *EmbeddedMedia
	// PresentationTimestamp is the time in the video the still image
	// shows, when the file says (HasPresentationTimestamp).
	PresentationTimestamp    time.Duration
	HasPresentationTimestamp bool
	// Images are the secondary images, in file order.
	Images []EmbeddedMedia
}

// XMP namespaces of Google's Motion Photo and container formats.
const (
	nsGCamera   = "http://ns.google.com/photos/1.0/camera/"
	nsContainer = "http://ns.google.com/photos/1.0/container/"
	nsItem      = "http://ns.google.com/photos/1.0/container/item/"
)

// ReadEmbedded reads where the media a JPEG carries after its image is:
//
//   - The XMP container directory (Container:Directory), as Motion Photos
//     and Ultra HDR images declare their parts. Its items follow the
//     primary image in order, each Item:Length bytes and Item:Padding
//     bytes after it, the last one ending the file.
//   - The older Motion Photo form, GCamera:MicroVideoOffset: the video's
//     distance from the end of the file.
//   - The Multi-Picture Format index (an APP2 "MPF" segment): its images
//     after the first.
//
// An item whose bytes aren't where the file says (outside the file, in
// its marker segments, or a video without an ftyp box, an image without a
// JPEG start) is left out. Only the marker segments and a few bytes of
// each item are read, never the image data.
func ReadEmbedded(r io.ReaderAt, size int64) (Embedded, error) {
	var out Embedded
	f, err := parseJPEG(r, size)
	if err != nil {
		return out, err
	}
	// The media data follows the marker segments: nothing embedded lies
	// in them.
	sos := f.segs[len(f.segs)-1]
	minOffset := sos.body + sos.bodyLen

	packet, err := readJPEGXMP(r, size)
	if err != nil {
		return out, err
	}
	if packet != nil {
		if d, err := parseXMPDoc(packet); err == nil {
			readContainer(r, size, minOffset, d, &out)
		}
	}
	mpf, err := f.mpf(r)
	if err != nil {
		return out, err
	}
	for _, e := range mpf.entries[min(1, len(mpf.entries)):] {
		at := mpf.base + int64(e.offset)
		if e.offset == 0 || at < minOffset || int64(e.size) > size-at || !startsJPEG(r, at) {
			continue
		}
		dup := false
		for _, m := range out.Images {
			dup = dup || m.Offset == at
		}
		if !dup {
			out.Images = append(out.Images, EmbeddedMedia{Semantic: "MPF", MIME: "image/jpeg", Offset: at, Length: int64(e.size)})
		}
	}
	sortMedia(out.Images)
	return out, nil
}

func sortMedia(ms []EmbeddedMedia) {
	for i := 1; i < len(ms); i++ {
		for j := i; j > 0 && ms[j].Offset < ms[j-1].Offset; j-- {
			ms[j], ms[j-1] = ms[j-1], ms[j]
		}
	}
}

// readContainer reads the XMP's container directory and Motion Photo
// properties into out.
func readContainer(r io.ReaderAt, size, minOffset int64, d *xmpDoc, out *Embedded) {
	for _, local := range []string{"MotionPhotoPresentationTimestampUs", "MicroVideoPresentationTimestampUs"} {
		if ps := d.props(nsGCamera, local); len(ps) > 0 && !out.HasPresentationTimestamp {
			if us, err := strconv.ParseInt(ps[0].value(), 10, 64); err == nil && us >= 0 && us < math.MaxInt64/1000 {
				out.PresentationTimestamp, out.HasPresentationTimestamp = time.Duration(us)*time.Microsecond, true
			}
		}
	}
	type item struct {
		semantic, mime  string
		length, padding int64
	}
	var dir []item
	if ps := d.props(nsContainer, "Directory"); len(ps) > 0 {
		if c := ps[0].container(); c != nil {
			in := ps[0].in.enter(ps[0].elem).enter(c)
			for _, li := range items(c) {
				a := itemFields(li, in.enter(li))
				it := item{semantic: a["Semantic"], mime: a["Mime"]}
				var err1, err2 error
				if s := a["Length"]; s != "" {
					it.length, err1 = strconv.ParseInt(s, 10, 64)
				}
				if s := a["Padding"]; s != "" {
					it.padding, err2 = strconv.ParseInt(s, 10, 64)
				}
				if err1 != nil || err2 != nil || it.length < 0 || it.padding < 0 {
					dir = nil
					break
				}
				dir = append(dir, it)
			}
		}
	}
	// The items after the first (the primary image) end the file, in
	// order: place them from the end.
	if len(dir) > 1 {
		end := size
		placed := make([]EmbeddedMedia, len(dir))
		ok := true
		for i := len(dir) - 1; i >= 1; i-- {
			it := dir[i]
			if it.length == 0 || it.padding > end || it.length > end-it.padding {
				ok = false
				break
			}
			end -= it.padding
			start := end - it.length
			placed[i] = EmbeddedMedia{Semantic: it.semantic, MIME: it.mime, Offset: start, Length: it.length}
			end = start
		}
		if ok && end >= minOffset {
			for _, m := range placed[1:] {
				if isVideoItem(m) {
					if out.Video == nil && startsMovie(r, m.Offset, m.Length) {
						v := m
						out.Video = &v
					}
					continue
				}
				if strings.HasPrefix(m.MIME, "image/jpeg") && !startsJPEG(r, m.Offset) {
					continue
				}
				out.Images = append(out.Images, m)
			}
		}
	}
	if out.Video == nil {
		if ps := d.props(nsGCamera, "MicroVideoOffset"); len(ps) > 0 {
			if n, err := strconv.ParseInt(ps[0].value(), 10, 64); err == nil && n > 0 && n <= size-minOffset {
				m := EmbeddedMedia{Semantic: "MicroVideo", MIME: "video/mp4", Offset: size - n, Length: n}
				if startsMovie(r, m.Offset, m.Length) {
					out.Video = &m
				}
			}
		}
	}
}

func isVideoItem(m EmbeddedMedia) bool {
	return m.Semantic == "MotionPhoto" || strings.HasPrefix(m.MIME, "video/")
}

// itemFields are the Item: fields of a container directory's rdf:li: the
// attributes of its Container:Item element (or of the li itself), or
// their element forms.
func itemFields(li *xmlNode, in scope) map[string]string {
	out := map[string]string{}
	add := func(n *xmlNode, s scope) {
		for _, a := range n.attrs {
			if a.Name.Space == "" || a.Name.Space == nsXMLNS {
				continue
			}
			if uri, _ := s.ns(a.Name, true); uri == nsItem {
				out[a.Name.Local] = a.Value
			}
		}
		for _, k := range elements(n) {
			if uri, _ := s.enter(k).ns(k.name, false); uri == nsItem {
				out[k.name.Local] = strings.TrimSpace(textOf(k))
			}
		}
	}
	add(li, in)
	for _, k := range elements(li) {
		ks := in.enter(k)
		if uri, _ := ks.ns(k.name, false); uri == nsContainer && k.name.Local == "Item" {
			add(k, ks)
		}
	}
	return out
}

// startsMovie reports whether an ISO base media file starts at off: an
// ftyp box (or another top-level box a movie starts with).
func startsMovie(r io.ReaderAt, off, n int64) bool {
	var h [8]byte
	if n < 8 {
		return false
	}
	if _, err := r.ReadAt(h[:], off); err != nil {
		return false
	}
	switch string(h[4:8]) {
	case "ftyp", "moov", "mdat", "wide", "free":
		return true
	}
	return false
}

func startsJPEG(r io.ReaderAt, off int64) bool {
	var h [3]byte
	if _, err := r.ReadAt(h[:], off); err != nil {
		return false
	}
	return h[0] == 0xFF && h[1] == markerSOI && h[2] == 0xFF
}

// mpfIndex is a JPEG's Multi-Picture Format index (CIPA DC-007).
type mpfIndex struct {
	seg jpegSegment
	// base is the file offset the entries' offsets count from: the TIFF
	// header after "MPF\0".
	base    int64
	entries []mpEntry
	order   binary.ByteOrder
}

// mpEntry is one image of the index; at is the file offset of its
// offset field.
type mpEntry struct {
	size, offset uint32
	at           int64
}

var mpfHeader = []byte("MPF\x00")

// markerAPP2 holds the MPF index (and ICC profiles).
const markerAPP2 = 0xE2

// mpf reads the first APP2 segment holding an MPF index; entries is
// empty without one, or for one this package can't read.
func (f *jpegFile) mpf(r io.ReaderAt) (mpfIndex, error) {
	var x mpfIndex
	for _, s := range f.segs {
		if s.marker != markerAPP2 || s.bodyLen < int64(len(mpfHeader))+8 {
			continue
		}
		body := make([]byte, s.bodyLen)
		if _, err := r.ReadAt(body, s.body); err != nil {
			return x, fmt.Errorf("%w: reading APP2: %v", ErrMalformed, err)
		}
		if !bytes.HasPrefix(body, mpfHeader) {
			continue
		}
		x.seg, x.base = s, s.body+int64(len(mpfHeader))
		t := body[len(mpfHeader):]
		var order binary.ByteOrder
		switch string(t[:2]) {
		case "II":
			order = binary.LittleEndian
		case "MM":
			order = binary.BigEndian
		default:
			return x, nil
		}
		ifd := int64(order.Uint32(t[4:]))
		if ifd < 8 || ifd+2 > int64(len(t)) {
			return x, nil
		}
		n := int64(order.Uint16(t[ifd:]))
		if ifd+2+12*n > int64(len(t)) {
			return x, nil
		}
		for i := range n {
			e := t[ifd+2+12*i:]
			// MPEntry: UNDEFINED, 16 bytes per image.
			if order.Uint16(e) != 0xB002 || order.Uint16(e[2:]) != 7 {
				continue
			}
			count := int64(order.Uint32(e[4:]))
			if count%16 != 0 || count <= 4 {
				return x, nil
			}
			off := int64(order.Uint32(e[8:]))
			if off < 8 || count > int64(len(t))-off {
				return x, nil
			}
			for j := int64(0); j < count/16; j++ {
				p := t[off+16*j:]
				x.entries = append(x.entries, mpEntry{
					size:   order.Uint32(p[4:]),
					offset: order.Uint32(p[8:]),
					at:     x.base + off + 16*j + 8,
				})
			}
			x.order = order
			return x, nil
		}
		return x, nil
	}
	return x, nil
}

// fixMPF keeps a JPEG edit's MPF index pointing at its images: where the
// edit changes the size of what lies between the index and the images
// (a segment after the MPF one), it patches the index's offsets by that
// much. An edit that would change the MPF segment itself is refused.
func (f *jpegFile) fixMPF(r io.ReaderAt, e *spliceEdit) error {
	x, err := f.mpf(r)
	if err != nil || len(x.entries) < 2 {
		return err
	}
	segStart, segEnd := x.seg.offset, x.seg.body+x.seg.bodyLen
	var delta int64
	for _, s := range e.splices {
		switch {
		case s.at+s.del <= segStart:
			// Before the index: it moves with its images.
		case s.at >= segEnd:
			delta += int64(len(s.data)) - s.del
		default:
			return fmt.Errorf("%w: an edit inside the MPF segment", ErrUnsupported)
		}
	}
	if delta == 0 {
		return nil
	}
	for _, en := range x.entries[1:] {
		if en.offset == 0 {
			continue
		}
		v := int64(en.offset) + delta
		if v <= 0 || v > math.MaxUint32 {
			return fmt.Errorf("%w: an MPF offset out of range after the edit", ErrUnsupported)
		}
		b := make([]byte, 4)
		x.order.PutUint32(b, uint32(v))
		e.add(splice{at: en.at, del: 4, data: b})
	}
	return e.sorted()
}
