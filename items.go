package mediameta

import (
	"bytes"
	"encoding/binary"
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
//
// A JPEG's XMP goes in its XMP APP1 segment: in place where it fits there
// (padded with whitespace), else in a new segment that replaces it, after
// the Exif segment for a JPEG without XMP. A packet past one segment's
// 64 KB is split into a standard and an extended part (XMP Part 3): the
// largest properties go to the extended part, in chunks after the
// standard segment. Old extended segments are removed.
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
// the item's length are patched. Otherwise the data goes in a new media
// data box at the end of the file. Either way the old data is overwritten
// with zeros, so a value removed from the metadata (a GPS position, say)
// isn't left in the file to be found; the patch of an in-place edit
// covers the whole old extent. A JPEG's first Exif segment is set as
// PlanExifEdit sets it.
func PlanExif(src io.ReaderAt, size int64, tiff []byte) (*Plan, error) {
	if _, _, ok := newTIFF(tiff); !ok {
		return nil, fmt.Errorf("mediameta: Exif data that doesn't start with a TIFF header")
	}
	if isJPEG(src, size) {
		return planJPEGExif(src, size, func([]byte) ([]byte, error) { return tiff, nil })
	}
	if p, ok, err := planChunked(src, size, kindExif, func([]byte) ([]byte, error) { return tiff, nil }); ok {
		return p, err
	}
	// A 4-byte offset to the TIFF header comes first (ISO/IEC 23008-12
	// A.2.1): 0, the header follows.
	return planItem(src, size, kindExif, append(make([]byte, 4), tiff...))
}

// PlanXMP is SetXMP's edit, computed but not written, in place as
// PlanExif's is, and like it overwriting the old data with zeros.
func PlanXMP(src io.ReaderAt, size int64, packet []byte) (*Plan, error) {
	if !utf8.Valid(packet) || !bytes.Contains(packet, []byte("xmpmeta")) && !bytes.Contains(packet, []byte("rdf:RDF")) {
		return nil, fmt.Errorf("mediameta: XMP data that isn't UTF-8 XMP")
	}
	if isJPEG(src, size) {
		return planJPEGXMP(src, size, packet)
	}
	if p, ok, err := planChunked(src, size, kindXMP, func([]byte) ([]byte, error) { return packet, nil }); ok {
		return p, err
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
	// The meta box changes; no image's data may lie in it.
	if err := f.noDataInMeta(); err != nil {
		return nil, err
	}
	// Data that runs past the end of idat, where it would be read from the
	// meta box's other boxes or from the media data after it, makes it
	// unclear which bytes are whose.
	if err := f.idatInBounds(); err != nil {
		return nil, err
	}
	var patches []patch
	var old []oldData
	var inPlace oldData // where the new data goes when it fits
	var inPlaceData []byte
	placed := false
	if found {
		if inPlace, inPlaceData, placed, err = f.patchItem(id, payload); err != nil {
			return nil, err
		}
		if !placed {
			if old, err = f.oldExtents(id, size); err != nil {
				return nil, err
			}
		}
	} else if id, err = f.addMetadataItem(kind); err != nil {
		return nil, err
	}
	if !placed {
		// Nothing may take in the data appended at the end.
		if err := f.endsBefore(id, size); err != nil {
			return nil, err
		}
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
		// A last box that runs to the end of the file would take in the
		// data appended after it: it gets its size.
		closing, err := f.closeLastBox(src, delta, id)
		if err != nil {
			return nil, err
		}
		patches = append(patches, closing...)
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
	if !found {
		if err := readsBack(newMeta, kind, id, f.m.Primary); err != nil {
			return nil, err
		}
	}
	if placed {
		// Placed in the new file only now: a meta box that changed size
		// moves the data after it, and idat with it.
		if at, ok := f.outAt(inPlace, newMeta, delta); ok {
			patches = append(patches, patch{at: at, data: inPlaceData})
		} else {
			return nil, fmt.Errorf("%w: the item's data has no place in the new file", ErrUnsupported)
		}
	}
	patches = append(patches, f.zeroPatches(old, newMeta, delta)...)
	p := &Plan{Size: size, NewSize: size + delta + int64(len(tail)), src: src, f: f, meta: newMeta, patches: patches, tail: tail}
	if p.NewSize == size {
		p.InPlace = true
		p.Patches = inPlacePatches(f.meta.Offset, f.raw, newMeta, patches)
	}
	return p, nil
}

// readsBack checks that an item added to the meta box is there when it's
// read again: its type (and content type), its cdsc reference to the
// primary image, its location. A box the edit appended to can hold
// entries that hide what follows them (one that runs to its parent's end,
// say), and the file is refused rather than written with an item no
// reader finds.
func readsBack(meta []byte, kind metadataKind, id, primary uint32) error {
	m, err := bmff.ParseMeta(meta)
	if err != nil {
		return fmt.Errorf("%w: the new meta box doesn't read back: %v", ErrUnsupported, err)
	}
	want, ct := bmff.T("Exif"), ""
	if kind == kindXMP {
		want, ct = bmff.T("mime"), xmpContentType
	}
	refers := false
	for _, r := range m.Refs {
		if r.Type == bmff.T("cdsc") && r.From == id {
			for _, to := range r.To {
				refers = refers || to == primary
			}
		}
	}
	located := false
	if m.Loc != nil {
		_, located = m.Loc.Item(id)
	}
	if m.ItemTypes[id] != want || m.ContentType(id) != ct || !refers || !located {
		return fmt.Errorf("%w: the new %v item doesn't read back from the meta box", ErrUnsupported, kind)
	}
	return nil
}

// endsBefore checks that no item but id has data that would take in what
// is appended to the file: an extent that runs to the end of the file
// (length 0), or past it.
func (f *heifFile) endsBefore(id uint32, size int64) error {
	for _, it := range f.m.Loc.Items {
		if it.ID == id || it.ConstructionMethod != 0 || it.DataRefIndex != 0 {
			continue
		}
		for _, e := range it.Extents {
			start := it.BaseOffset + e.Offset
			switch {
			case e.Length == 0:
				return fmt.Errorf("%w: item %d runs to the end of the file, where the new data would go", ErrUnsupported, it.ID)
			case start < it.BaseOffset || start+e.Length < start || start+e.Length > uint64(size):
				return fmt.Errorf("%w: item %d's data runs past the end of the file", ErrMalformed, it.ID)
			}
		}
	}
	return nil
}

// idatInBounds checks that every idat extent lies within idat's payload.
func (f *heifFile) idatInBounds() error {
	_, idatLen := idatPayload(f.raw)
	for _, it := range f.m.Loc.Items {
		if it.ConstructionMethod != 1 || it.DataRefIndex != 0 {
			continue
		}
		for _, e := range it.Extents {
			start := it.BaseOffset + e.Offset
			if start < it.BaseOffset || start+e.Length < start || start+e.Length > uint64(max(idatLen, 0)) {
				return fmt.Errorf("%w: item %d's data runs past the end of idat", ErrMalformed, it.ID)
			}
		}
	}
	return nil
}

// closeLastBox is the patch that writes the size of the file's last box
// into its header when the header says 0, "to the end of the file" (ISO/IEC
// 14496-12 4.2): a box appended after it would otherwise be read as part of
// it. The meta box is encoded with its size, so it needs none. A box past 4
// GB can't take its size in the 32-bit field and is refused, as is a header
// another item's data covers (id, the item written, is left out).
func (f *heifFile) closeLastBox(src io.ReaderAt, delta int64, id uint32) ([]patch, error) {
	last := f.boxes[len(f.boxes)-1]
	if last.Offset == f.meta.Offset {
		return nil, nil
	}
	var h [4]byte
	if _, err := src.ReadAt(h[:], last.Offset); err != nil {
		return nil, fmt.Errorf("%w: reading the last box: %v", ErrMalformed, err)
	}
	if binary.BigEndian.Uint32(h[:]) != 0 {
		return nil, nil
	}
	if last.Size > 0xFFFFFFFF {
		return nil, fmt.Errorf("%w: a last box of %d bytes that runs to the end of the file", ErrUnsupported, last.Size)
	}
	// The size field is a box header, which no item's data should cover;
	// where one's does, patching it would change that item's data.
	for _, it := range f.m.Loc.Items {
		// id is the item being written: its data moves to the end.
		if it.ID == id || it.ConstructionMethod != 0 || it.DataRefIndex != 0 {
			continue
		}
		for _, e := range it.Extents {
			start := it.BaseOffset + e.Offset
			if start < uint64(last.Offset)+4 && (e.Length == 0 || start+e.Length > uint64(last.Offset)) {
				return nil, fmt.Errorf("%w: item %d's data covers the header of the last box", ErrUnsupported, it.ID)
			}
		}
	}
	at := last.Offset
	if at >= f.meta.End() {
		at += delta
	}
	return []patch{{at: at, data: binary.BigEndian.AppendUint32(nil, uint32(last.Size))}}, nil
}

// metadataItem finds the file's Exif or XMP item; two of a kind are
// refused, as it isn't clear which one to change.
func (f *heifFile) metadataItem(kind metadataKind) (uint32, bool, error) {
	var id uint32
	found := false
	for i, t := range f.m.ItemTypes {
		is := kind == kindExif && t == bmff.T("Exif") ||
			kind == kindXMP && t == bmff.T("mime") && f.m.ContentType(i) == xmpContentType
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

// patchItem places payload over the item's data where it fits: one
// extent, in an mdat's payload or idat's, that no other item uses, at
// least as long as payload. It returns where that is, in the source, and
// what goes there: payload, then zeros to the old extent's end. The
// extent's length becomes payload's. ok is false where it doesn't fit.
func (f *heifFile) patchItem(id uint32, payload []byte) (where oldData, data []byte, ok bool, err error) {
	l := f.m.Loc
	loc, _ := l.Item(id)
	if loc.DataRefIndex != 0 || len(loc.Extents) != 1 {
		return oldData{}, nil, false, nil
	}
	e := &loc.Extents[0]
	start := loc.BaseOffset + e.Offset
	// An extent of length 0, the only kind an iloc without length fields
	// has, runs to the end of the file (ISO/IEC 14496-12 8.11.3): data of
	// exactly that length is written over it, and the length stays 0.
	if e.Length == 0 && loc.ConstructionMethod == 0 && start >= loc.BaseOffset {
		end := f.boxes[len(f.boxes)-1].End()
		s := int64(start)
		if s < 0 || s >= end || end-s != int64(len(payload)) || !f.inOneMediaData(s, end) || f.shared(id, 0, start, uint64(end)) {
			return oldData{}, nil, false, nil
		}
		return oldData{at: s, n: end - s, afterEnd: s >= f.meta.End()}, bytes.Clone(payload), true, nil
	}
	if l.LengthSize == 0 {
		return oldData{}, nil, false, nil
	}
	if start < loc.BaseOffset || e.Length < uint64(len(payload)) || e.Length == 0 || start+e.Length < start {
		return oldData{}, nil, false, nil
	}
	n := int64(len(payload))
	switch loc.ConstructionMethod {
	case 0:
		s, end := int64(start), int64(start+e.Length)
		if !f.inOneMediaData(s, end) {
			return oldData{}, nil, false, nil
		}
		where = oldData{at: s, n: int64(e.Length), afterEnd: s >= f.meta.End()}
	case 1:
		idat, idatLen := idatPayload(f.raw)
		if idat < 0 || start+e.Length > uint64(idatLen) {
			return oldData{}, nil, false, nil
		}
		where = oldData{idat: true, at: int64(start), n: int64(e.Length)}
	default:
		return oldData{}, nil, false, nil
	}
	if f.shared(id, loc.ConstructionMethod, start, start+e.Length) {
		return oldData{}, nil, false, nil
	}
	// The rest of the old data is zeroed: it isn't left to be found.
	data = append(bytes.Clone(payload), make([]byte, e.Length-uint64(n))...)
	e.Length = uint64(n)
	return where, data, true, nil
}

// oldData is a range of an item's old data to zero: a file offset for
// construction method 0, an offset in idat's payload for 1.
type oldData struct {
	idat     bool
	at, n    int64
	afterEnd bool // past the meta box: it moves with the boxes after it
}

// oldExtents are the ranges of an item's data that are zeroed when the
// data moves: those in an mdat's or idat's payload that no other item
// uses. Data anywhere else (a box header, another file) is left alone; a
// metadata item never lies there in a file this package reads.
func (f *heifFile) oldExtents(id uint32, size int64) ([]oldData, error) {
	loc, _ := f.m.Loc.Item(id)
	if loc.DataRefIndex != 0 {
		return nil, nil
	}
	idat, idatLen := idatPayload(f.raw)
	var out []oldData
	for _, e := range loc.Extents {
		start := loc.BaseOffset + e.Offset
		if start < loc.BaseOffset {
			continue
		}
		n := e.Length
		switch loc.ConstructionMethod {
		case 0:
			if n == 0 && start <= uint64(size) {
				n = uint64(size) - start
			}
			if start+n < start || !f.inOneMediaData(int64(start), int64(start+n)) {
				continue
			}
		case 1:
			if n == 0 && idat >= 0 && start <= uint64(idatLen) {
				n = uint64(idatLen) - start
			}
			if idat < 0 || start+n < start || start+n > uint64(idatLen) || n == 0 {
				continue
			}
		default:
			continue
		}
		if f.shared(id, loc.ConstructionMethod, start, start+n) {
			continue
		}
		if n > maxMetadataItem {
			return nil, fmt.Errorf("%w: an old metadata extent of %d bytes", ErrUnsupported, n)
		}
		out = append(out, oldData{idat: loc.ConstructionMethod == 1, at: int64(start), n: int64(n),
			afterEnd: loc.ConstructionMethod == 0 && int64(start) >= f.meta.End()})
	}
	return out, nil
}

// zeroPatches are the output patches that zero old data, placed in the
// edited file (outAt).
func (f *heifFile) zeroPatches(old []oldData, newMeta []byte, delta int64) []patch {
	var out []patch
	for _, o := range old {
		if at, ok := f.outAt(o, newMeta, delta); ok {
			out = append(out, patch{at: at, data: make([]byte, o.n)})
		}
	}
	return out
}

// outAt is where a range of the source is in the edited file: past the
// meta box shifted by delta, in idat where the new meta box has it. ok is
// false for idat data when the new meta box has no idat.
func (f *heifFile) outAt(o oldData, newMeta []byte, delta int64) (int64, bool) {
	switch {
	case o.idat:
		idat, _ := idatPayload(newMeta)
		if idat < 0 {
			return 0, false
		}
		return f.meta.Offset + idat + o.at, true
	case o.afterEnd:
		return o.at + delta, true
	}
	return o.at, true
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
//
// The ranges are compared where they are in the file, whatever the
// construction method: an idat extent is a range of the meta box, which a
// malformed file can place over an mdat's data, and the other way round.
func (f *heifFile) shared(id uint32, cm uint8, start, end uint64) bool {
	s0, e0, ok := f.fileRange(cm, start, end-start)
	if !ok {
		return true
	}
	for _, it := range f.m.Loc.Items {
		if it.ID == id || it.DataRefIndex != 0 {
			continue
		}
		for _, e := range it.Extents {
			s := it.BaseOffset + e.Offset
			if e.Length == 0 && it.ConstructionMethod == cm {
				return true // to the end of the file or idat
			}
			s1, e1, ok := f.fileRange(it.ConstructionMethod, s, e.Length)
			if !ok {
				continue // past its container: refused before it gets here
			}
			if s1 < e0 && e1 > s0 {
				return true
			}
		}
	}
	return false
}

// fileRange is where n bytes at offset at of construction method cm (0:
// the file, 1: idat's payload) lie in the file; n 0 runs to the end of the
// file or of idat. ok is false for a range it can't place.
func (f *heifFile) fileRange(cm uint8, at, n uint64) (start, end uint64, ok bool) {
	var base, limit uint64
	switch cm {
	case 0:
		limit = uint64(f.boxes[len(f.boxes)-1].End())
	case 1:
		idat, idatLen := idatPayload(f.raw)
		if idat < 0 {
			return 0, 0, false
		}
		base, limit = uint64(f.meta.Offset+idat), uint64(idatLen)
	default:
		return 0, 0, false
	}
	if n == 0 {
		if at > limit {
			return 0, 0, false
		}
		n = limit - at
	}
	if at+n < at || at+n > limit {
		return 0, 0, false
	}
	return base + at, base + at + n, true
}

// ReadXMP returns the XMP packet of the JPEG, HEIF, PNG, WebP or camera
// RAW file in r (size bytes): a JPEG's XMP APP1 segment (with its
// extended XMP merged in, as one packet, where the file has all of it), a
// HEIF file's XMP item, a PNG's XMP iTXt chunk, a WebP's "XMP " chunk, a
// TIFF-based RAW's XMLPacket tag, a CR3's XMP box. Nil without one.
func ReadXMP(r io.ReaderAt, size int64) ([]byte, error) {
	switch {
	case isJPEG(r, size):
		return readJPEGXMP(r, size)
	case isPNG(r, size):
		return readPNGXMP(r, size)
	case isWebP(r, size):
		return readWebPXMP(r, size)
	}
	f, err := parseHEIF(r, size)
	if err != nil {
		if b, ok, rerr := readRAWXMP(r, size); ok {
			return b, rerr
		}
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
