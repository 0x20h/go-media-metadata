package mediameta

import (
	"bytes"
	"crypto/md5"
	"encoding/binary"
	"fmt"
	"io"
)

// A JPEG's IPTC-IIM is in its APP13 segment, among Photoshop's image
// resource blocks (Adobe Photoshop File Formats Specification, "Image
// Resource Blocks"): resource 0x0404 holds the IIM data, and 0x0425, where
// there is one, its MD5 digest, which Photoshop compares to tell whether
// another program changed the IIM.

var photoshopHeader = []byte("Photoshop 3.0\x00")

const (
	markerAPP13  = 0xED
	irbIPTC      = 0x0404
	irbIPTCMD5   = 0x0425
	maxIRBBlocks = 4096
)

// irb is one image resource block: its signature ("8BIM", or another
// writer's), id, name (a Pascal string, padded) and data.
type irb struct {
	sig  [4]byte
	id   uint16
	name []byte // the Pascal string as written, length byte and padding included
	data []byte
}

func parseIRBs(b []byte) ([]irb, error) {
	var out []irb
	for i := 0; i < len(b); {
		if len(out) >= maxIRBBlocks {
			return nil, fmt.Errorf("%w: more than %d image resources", ErrMalformed, maxIRBBlocks)
		}
		if allZero(b[i:]) {
			break
		}
		if i+7 > len(b) {
			return nil, fmt.Errorf("%w: image resource header past the end", ErrMalformed)
		}
		var r irb
		copy(r.sig[:], b[i:i+4])
		switch string(r.sig[:]) {
		case "8BIM", "PHUT", "AgHg", "DCSR", "MeSa":
		default:
			return nil, fmt.Errorf("%w: image resource signature %q", ErrMalformed, r.sig[:])
		}
		r.id = binary.BigEndian.Uint16(b[i+4:])
		i += 6
		n := 1 + int(b[i]) // the name, padded to an even length
		n += n & 1
		if i+n+4 > len(b) {
			return nil, fmt.Errorf("%w: image resource name past the end", ErrMalformed)
		}
		r.name = b[i : i+n]
		i += n
		size := int64(binary.BigEndian.Uint32(b[i:]))
		i += 4
		if size > int64(len(b)-i) {
			return nil, fmt.Errorf("%w: image resource 0x%04X of %d bytes past the end", ErrMalformed, r.id, size)
		}
		r.data = b[i : i+int(size)]
		i += int(size) + int(size&1)
		out = append(out, r)
	}
	return out, nil
}

func writeIRBs(rs []irb) []byte {
	var out []byte
	for _, r := range rs {
		out = append(out, r.sig[:]...)
		out = binary.BigEndian.AppendUint16(out, r.id)
		out = append(out, r.name...)
		out = binary.BigEndian.AppendUint32(out, uint32(len(r.data)))
		out = append(out, r.data...)
		if len(r.data)&1 == 1 {
			out = append(out, 0)
		}
	}
	return out
}

// photoshopSegments are a JPEG's APP13 segments that hold image resources,
// and their resources together: a writer splits them across segments when
// they pass one.
func (f *jpegFile) photoshopSegments(r io.ReaderAt) ([]jpegSegment, []irb, error) {
	var segs []jpegSegment
	var all []byte
	for _, s := range f.segs {
		if s.marker != markerAPP13 || s.bodyLen < int64(len(photoshopHeader)) {
			continue
		}
		body := make([]byte, s.bodyLen)
		if _, err := r.ReadAt(body, s.body); err != nil {
			return nil, nil, fmt.Errorf("%w: reading APP13: %v", ErrMalformed, err)
		}
		if !bytes.HasPrefix(body, photoshopHeader) {
			continue
		}
		segs = append(segs, s)
		all = append(all, body[len(photoshopHeader):]...)
	}
	if segs == nil {
		return nil, nil, nil
	}
	rs, err := parseIRBs(all)
	return segs, rs, err
}

// ReadIPTC returns the IPTC-IIM data of the JPEG file in r (size bytes):
// the Photoshop image resource 0x0404 in its APP13 segments. Nil without
// one. ParseIPTC reads its fields.
func ReadIPTC(r io.ReaderAt, size int64) ([]byte, error) {
	f, err := parseJPEG(r, size)
	if err != nil {
		return nil, err
	}
	_, rs, err := f.photoshopSegments(r)
	if err != nil {
		return nil, err
	}
	for _, x := range rs {
		if x.id == irbIPTC && string(x.sig[:]) == "8BIM" {
			return bytes.Clone(x.data), nil
		}
	}
	return nil, nil
}

// SetIPTC writes to dst the JPEG file in src (size bytes) with its
// IPTC-IIM data set to iim, as PlanIPTC plans it.
func SetIPTC(src io.ReaderAt, size int64, dst io.Writer, iim []byte) error {
	p, err := PlanIPTC(src, size, iim)
	if err != nil {
		return err
	}
	_, err = p.WriteTo(dst)
	return err
}

// PlanIPTC plans setting a JPEG's IPTC-IIM data (empty data removes it).
// The other image resources stay as they are, in their order; an IPTC
// digest (0x0425) is updated to the new data's MD5, so Photoshop doesn't
// take the change for one it should undo. The resources go in one APP13
// segment, in place of the old ones: in place when it keeps the old
// segment's length, else a new segment (the old ones aren't copied). A
// JPEG without APP13 gets one after its other APPn segments. Image
// resources past one segment's 64 KB are refused (ErrUnsupported).
func PlanIPTC(src io.ReaderAt, size int64, iim []byte) (*Plan, error) {
	if len(iim) > 0 {
		if _, err := parseIIM(iim); err != nil {
			return nil, err
		}
	}
	f, err := parseJPEG(src, size)
	if err != nil {
		return nil, err
	}
	segs, rs, err := f.photoshopSegments(src)
	if err != nil {
		return nil, err
	}
	var out []irb
	found := false
	for _, x := range rs {
		if x.id == irbIPTC && string(x.sig[:]) == "8BIM" {
			if !found && len(iim) > 0 {
				x.data = iim
				out = append(out, x)
			}
			found = true
			continue
		}
		out = append(out, x)
	}
	if !found && len(iim) > 0 {
		out = append(out, irb{sig: [4]byte{'8', 'B', 'I', 'M'}, id: irbIPTC, name: []byte{0, 0}, data: iim})
	}
	for i, x := range out {
		if x.id == irbIPTCMD5 && string(x.sig[:]) == "8BIM" {
			if len(iim) == 0 {
				out = append(out[:i], out[i+1:]...)
				break
			}
			sum := md5.Sum(iim)
			out[i].data = sum[:]
		}
	}
	e := &spliceEdit{}
	if len(out) == 0 {
		for _, s := range segs {
			e.add(splice{at: s.offset, del: s.body + s.bodyLen - s.offset})
		}
		return e.plan(src, size), nil
	}
	body := append(append([]byte(nil), photoshopHeader...), writeIRBs(out)...)
	if len(body) > maxAPP1 {
		return nil, fmt.Errorf("%w: image resources of %d bytes, past a JPEG segment's %d", ErrUnsupported, len(body), maxAPP1)
	}
	switch {
	case len(segs) == 1 && int64(len(body)) == segs[0].bodyLen:
		e.add(splice{at: segs[0].body, del: segs[0].bodyLen, data: body})
	case len(segs) > 0:
		e.add(splice{at: segs[0].offset, del: segs[0].body + segs[0].bodyLen - segs[0].offset, data: app13(body)})
		for _, s := range segs[1:] {
			e.add(splice{at: s.offset, del: s.body + s.bodyLen - s.offset})
		}
	default:
		e.add(splice{at: f.afterAPPn(), data: app13(body)})
	}
	if err := e.sorted(); err != nil {
		return nil, err
	}
	return e.plan(src, size), nil
}

func app13(body []byte) []byte {
	seg := []byte{0xFF, markerAPP13}
	seg = binary.BigEndian.AppendUint16(seg, uint16(len(body)+2))
	return append(seg, body...)
}

// afterAPPn is where a new APP13 goes: after the APPn segments (JFIF, Exif,
// XMP, ICC) at the start of the file, before the tables and the frame.
func (f *jpegFile) afterAPPn() int64 {
	at := int64(2)
	for _, s := range f.segs {
		if s.marker < 0xE0 || s.marker > 0xEF {
			break
		}
		at = s.body + s.bodyLen
	}
	return at
}
