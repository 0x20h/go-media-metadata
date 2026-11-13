package mediameta

import (
	"encoding/binary"
	"fmt"
	"io"
)

// maxAPP1 is the most a JPEG segment's payload holds: its 2-byte length
// counts itself.
const maxAPP1 = 1<<16 - 1 - 2

// planJPEGExif sets a JPEG's Exif: edit makes the new TIFF data from the
// old (nil without Exif). The first Exif APP1 segment is the one read
// and the one written. New data no longer than the old fills the
// segment, padded with zeros: in place, the old values overwritten. Longer
// data replaces the segment, which isn't copied, so nothing of the old
// values stays in the file. A JPEG without Exif gets a segment after its
// JFIF header. Data past a segment's 64 KB is refused (ErrUnsupported).
func planJPEGExif(src io.ReaderAt, size int64, edit func(old []byte) ([]byte, error)) (*Plan, error) {
	f, err := parseJPEG(src, size)
	if err != nil {
		return nil, err
	}
	seg, payload, found, err := f.exifSegment(src)
	if err != nil {
		return nil, err
	}
	var old []byte
	if found {
		old = payload[len(exifHeader):]
	}
	tiff, err := edit(old)
	if err != nil {
		return nil, err
	}
	body := append(append(make([]byte, 0, len(exifHeader)+len(tiff)), exifHeader...), tiff...)
	if len(body) > maxAPP1 {
		return nil, fmt.Errorf("%w: Exif of %d bytes, past a JPEG segment's %d", ErrUnsupported, len(body), maxAPP1)
	}
	e := &spliceEdit{}
	switch {
	case found && int64(len(body)) <= seg.bodyLen:
		padded, _ := padTo(body, int(seg.bodyLen), 0)
		// The segment's own header ("Exif" and its pad byte) stays as it
		// was: some writers put 0xFF after the NUL.
		copy(padded, payload[:len(exifHeader)])
		e.add(splice{at: seg.body, del: seg.bodyLen, data: padded})
	case found:
		e.add(splice{at: seg.offset, del: seg.body + seg.bodyLen - seg.offset, data: app1(body)})
	default:
		e.add(splice{at: f.afterJFIF(), data: app1(body)})
	}
	if err := f.fixMPF(src, e); err != nil {
		return nil, err
	}
	return e.plan(src, size), nil
}

// app1 is an APP1 segment holding body.
func app1(body []byte) []byte {
	seg := []byte{0xFF, markerAPP1}
	seg = binary.BigEndian.AppendUint16(seg, uint16(len(body)+2))
	return append(seg, body...)
}
