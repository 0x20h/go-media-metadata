package mediameta

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"

	"github.com/0x20h/go-media-metadata/internal/bmff"
)

// xmpContentType is the content type of a HEIF file's XMP item (ISO/IEC
// 23008-12 A.2.2).
const xmpContentType = "application/rdf+xml"

// maxMetadataItem caps an Exif or XMP item written.
const maxMetadataItem = 16 << 20

// metadataKind is which metadata item an edit writes.
type metadataKind int

const (
	kindExif metadataKind = iota
	kindXMP
)

func (k metadataKind) String() string {
	if k == kindExif {
		return "Exif"
	}
	return "XMP"
}

// SetExif writes to dst the HEIF file in src (size bytes) with its Exif
// item's data set to tiff: TIFF data, from its header ("II*\0" or
// "MM\0*") on, with the IFDs the caller encoded. The item is added when
// the file has none: listed in iinf and described as the primary image's
// metadata (a cdsc reference). The image items' data is copied byte for
// byte; item offsets the meta box's growth moves are moved.
//
// The Exif Orientation tag is written as given: HEIF readers show the
// image by its irot and imir properties (see SetOrientation), so a tag
// that disagrees with them misleads only readers that use Exif.
//
// On an error nothing useful has been written to dst.
func SetExif(src io.ReaderAt, size int64, dst io.Writer, tiff []byte) error {
	p, err := PlanExif(src, size, tiff)
	if err != nil {
		return err
	}
	_, err = p.WriteTo(dst)
	return err
}

// SetXMP writes to dst the HEIF file in src (size bytes) with its XMP
// item (a "mime" item of type application/rdf+xml) set to packet, UTF-8
// XMP as an x:xmpmeta element or an XMP packet. It's added when the file
// has none, as SetExif adds an Exif item.
func SetXMP(src io.ReaderAt, size int64, dst io.Writer, packet []byte) error {
	p, err := PlanXMP(src, size, packet)
	if err != nil {
		return err
	}
	_, err = p.WriteTo(dst)
	return err
}

// PlanExif is SetExif's edit, computed but not written. It is in place
// when the new data fits where the item's data is (one extent in a media
// data box or in idat, as long as the new data or longer): the data and
// the item's length are patched, and what's left of the old data stays
// as unreferenced bytes. Otherwise the data goes in a new media data box
// at the end of the file.
func PlanExif(src io.ReaderAt, size int64, tiff []byte) (*Plan, error) {
	if _, _, ok := newTIFF(tiff); !ok {
		return nil, fmt.Errorf("mediameta: Exif data that doesn't start with a TIFF header")
	}
	// A 4-byte offset to the TIFF header comes first (ISO/IEC 23008-12
	// A.2.1): 0, the header follows.
	return planItem(src, size, kindExif, append(make([]byte, 4), tiff...))
}

// PlanXMP is SetXMP's edit, computed but not written, in place as
// PlanExif's is.
func PlanXMP(src io.ReaderAt, size int64, packet []byte) (*Plan, error) {
	if !utf8.Valid(packet) || !bytes.Contains(packet, []byte("xmpmeta")) && !bytes.Contains(packet, []byte("rdf:RDF")) {
		return nil, fmt.Errorf("mediameta: XMP data that isn't UTF-8 XMP")
	}
	return planItem(src, size, kindXMP, packet)
}

// planItem sets the data of the file's Exif or XMP item to payload, adding
// the item where there's none.
func planItem(src io.ReaderAt, size int64, kind metadataKind, payload []byte) (*Plan, error) {
	if len(payload) > maxMetadataItem {
		return nil, fmt.Errorf("%w: %v item of %d bytes", ErrUnsupported, kind, len(payload))
	}
	f, err := parseHEIF(src, size)
	if err != nil {
		return nil, err
	}
	id, found, err := f.metadataItem(kind)
	if err != nil {
		return nil, err
	}
	var patches []patch
	placed := false
	if found {
		if patches, placed, err = f.patchItem(id, payload); err != nil {
			return nil, err
		}
	} else if id, err = f.addMetadataItem(kind); err != nil {
		return nil, err
	}
	loc, _ := f.m.Loc.Item(id)
	if !placed {
		// Somewhere offsets don't move from, until it's placed.
		*loc = bmff.LocItem{ID: id, Extents: []bmff.Extent{{}}}
	}
	newMeta, err := f.m.Encode()
	if err != nil {
		return nil, err
	}
	delta := int64(len(newMeta)) - f.meta.Size
	if delta != 0 {
		if err := f.shiftOffsets(delta); err != nil {
			return nil, err
		}
	}
	var tail []byte
	if !placed {
		// A media data box at the end holds the data.
		tail = bmff.AppendHeader(nil, bmff.T("mdat"), len(payload), false)
		at := uint64(size + delta + int64(len(tail)))
		tail = append(tail, payload...)
		l := f.m.Loc
		switch {
		case l.OffsetSize > 0:
			loc.Extents[0].Offset = at
		case l.BaseOffsetSize > 0:
			loc.BaseOffset = at
		default:
			return nil, fmt.Errorf("%w: an iloc without offsets", ErrUnsupported)
		}
		// Without a length field the extent runs to the end of the file,
		// which is where the data ends.
		loc.Extents[0].Length = uint64(len(payload))
		if l.LengthSize == 0 {
			loc.Extents[0].Length = 0
		}
	}
	if newMeta, err = f.m.Encode(); err != nil {
		if bmff.ErrFieldTooSmall(err) {
			return nil, fmt.Errorf("%w: an item offset outgrows its field", ErrUnsupported)
		}
		return nil, err
	}
	if int64(len(newMeta))-f.meta.Size != delta {
		return nil, fmt.Errorf("%w: the meta box changed size twice", ErrUnsupported)
	}
	p := &Plan{Size: size, NewSize: size + delta + int64(len(tail)), src: src, f: f, meta: newMeta, patches: patches, tail: tail}
	if p.NewSize == size {
		p.InPlace = true
		p.Patches = inPlacePatches(f.meta.Offset, f.raw, newMeta, patches)
	}
	return p, nil
}

// metadataItem finds the file's Exif or XMP item; two of a kind are
// refused, as it isn't clear which one to change.
func (f *heifFile) metadataItem(kind metadataKind) (uint32, bool, error) {
	var id uint32
	found := false
	for i, t := range f.m.ItemTypes {
		is := kind == kindExif && t == bmff.T("Exif") ||
			kind == kindXMP && t == bmff.T("mime") && f.m.ContentTypes[i] == xmpContentType
		if !is {
			continue
		}
		if found {
			return 0, false, fmt.Errorf("%w: two %v items", ErrUnsupported, kind)
		}
		id, found = i, true
	}
	if found {
		if _, ok := f.m.Loc.Item(id); !ok {
			return 0, false, fmt.Errorf("%w: the %v item has no location", ErrMalformed, kind)
		}
	}
	return id, found, nil
}

// addMetadataItem lists a new Exif or XMP item that describes the primary
// image, with a location to be placed.
func (f *heifFile) addMetadataItem(kind metadataKind) (uint32, error) {
	var id uint32
	for i := range f.m.ItemTypes {
		id = max(id, i)
	}
	for _, it := range f.m.Loc.Items {
		id = max(id, it.ID)
	}
	id++
	if id == 0 || f.m.Loc.Version < 2 && id > 0xFFFF {
		return 0, fmt.Errorf("%w: no item id left", ErrUnsupported)
	}
	typ, ct := bmff.T("Exif"), ""
	if kind == kindXMP {
		typ, ct = bmff.T("mime"), xmpContentType
	}
	if err := f.m.AddItem(id, typ, ct); err != nil {
		return 0, noRoom(err)
	}
	if err := f.m.AddRef(bmff.T("cdsc"), id, []uint32{f.m.Primary}); err != nil {
		return 0, noRoom(err)
	}
	f.m.Loc.Items = append(f.m.Loc.Items, bmff.LocItem{ID: id})
	return id, nil
}

func noRoom(err error) error {
	if errors.Is(err, bmff.ErrNoRoom) {
		return fmt.Errorf("%w: %v", ErrUnsupported, err)
	}
	return err
}

// patchItem writes payload over the item's data where it fits: one
// extent, in an mdat's payload or idat's, that no other item uses, at
// least as long as payload. The extent's length becomes payload's. ok is
// false where it doesn't fit.
func (f *heifFile) patchItem(id uint32, payload []byte) ([]patch, bool, error) {
	l := f.m.Loc
	loc, _ := l.Item(id)
	if loc.DataRefIndex != 0 || len(loc.Extents) != 1 || l.LengthSize == 0 {
		return nil, false, nil
	}
	e := &loc.Extents[0]
	start := loc.BaseOffset + e.Offset
	if start < loc.BaseOffset || e.Length < uint64(len(payload)) || e.Length == 0 || start+e.Length < start {
		return nil, false, nil
	}
	n := int64(len(payload))
	var at int64
	switch loc.ConstructionMethod {
	case 0:
		s, end := int64(start), int64(start+e.Length)
		if !f.inOneMediaData(s, end) {
			return nil, false, nil
		}
		at = s
	case 1:
		idat, idatLen := idatPayload(f.raw)
		if idat < 0 || start+e.Length > uint64(idatLen) {
			return nil, false, nil
		}
		at = f.meta.Offset + idat + int64(start)
	default:
		return nil, false, nil
	}
	if f.shared(id, loc.ConstructionMethod, start, start+e.Length) {
		return nil, false, nil
	}
	e.Length = uint64(n)
	return []patch{{at: at, data: bytes.Clone(payload)}}, true, nil
}

// inOneMediaData reports whether [start, end) lies in one top-level mdat
// box's payload.
func (f *heifFile) inOneMediaData(start, end int64) bool {
	for _, b := range f.boxes {
		if b.Type == bmff.T("mdat") && start >= b.Body() && end <= b.End() && start < end {
			return true
		}
	}
	return false
}

// shared reports whether another item's data overlaps [start, end) of
// construction method cm.
func (f *heifFile) shared(id uint32, cm uint8, start, end uint64) bool {
	for _, it := range f.m.Loc.Items {
		if it.ID == id || it.ConstructionMethod != cm || it.DataRefIndex != 0 {
			continue
		}
		for _, e := range it.Extents {
			s := it.BaseOffset + e.Offset
			if e.Length == 0 || s < end && s+e.Length > start {
				return true
			}
		}
	}
	return false
}

// ReadXMP returns the XMP item of the HEIF file in r (size bytes), nil
// without one.
func ReadXMP(r io.ReaderAt, size int64) ([]byte, error) {
	f, err := parseHEIF(r, size)
	if err != nil {
		return nil, err
	}
	id, found, err := f.metadataItem(kindXMP)
	if err != nil || !found {
		return nil, err
	}
	return f.itemBytes(r, size, id)
}

// itemBytes reads an item's whole data (construction method 0 or 1).
func (f *heifFile) itemBytes(r io.ReaderAt, size int64, id uint32) ([]byte, error) {
	loc, _ := f.m.Loc.Item(id)
	if loc.DataRefIndex != 0 {
		return nil, fmt.Errorf("%w: item %d in another file", ErrUnsupported, id)
	}
	var base int64
	switch loc.ConstructionMethod {
	case 0:
	case 1:
		idat, _ := idatPayload(f.raw)
		if idat < 0 {
			return nil, fmt.Errorf("%w: item %d in an idat that isn't there", ErrMalformed, id)
		}
		base = f.meta.Offset + idat
	default:
		return nil, fmt.Errorf("%w: item %d's construction method", ErrUnsupported, id)
	}
	var out []byte
	for _, e := range loc.Extents {
		at := base + int64(loc.BaseOffset+e.Offset)
		n := int64(e.Length)
		if n == 0 {
			n = size - at
		}
		if at < 0 || n < 0 || at > size || n > size-at || int64(len(out))+n > maxMetadataItem {
			return nil, fmt.Errorf("%w: item %d outside the file", ErrMalformed, id)
		}
		b := make([]byte, n)
		if _, err := r.ReadAt(b, at); err != nil && err != io.EOF {
			return nil, fmt.Errorf("%w: reading item %d: %v", ErrMalformed, id, err)
		}
		out = append(out, b...)
	}
	return out, nil
}
