package mediameta

import (
	"bytes"
	"fmt"
	"io"
	"time"
)

// MPEG program streams (ISO/IEC 11172-1 and 13818-1): .mpg, .mpeg, .vob,
// as DVD and older cameras wrote them. Read only. The size comes from the
// first video sequence header; the duration is the span from the first
// pack's system clock reference to the last's. (ExifTool gives the size,
// and no duration for a variable bitrate.)

// errMPEGPSReadOnly is the error of a write to an MPEG program stream.
var errMPEGPSReadOnly = fmt.Errorf("%w: an MPEG program stream is read only", ErrUnsupported)

const (
	maxPSScan = 4 << 20 // read from the start for the sequence header
	maxPSBack = 512_000 // read from the end for the last pack
)

var packStart = []byte{0, 0, 1, 0xBA}

func isMPEGPS(r io.ReaderAt, size int64) bool {
	var h [5]byte
	if size < 16 {
		return false
	}
	if _, err := r.ReadAt(h[:], 0); err != nil {
		return false
	}
	// A pack header: MPEG-2 ('01' after the start code) or MPEG-1 ('0010').
	return bytes.Equal(h[:4], packStart) && (h[4]&0xC0 == 0x40 || h[4]&0xF0 == 0x20)
}

// packSCR reads a pack header's system clock reference, in 27 MHz ticks,
// from the bytes after its start code.
func packSCR(b []byte) (int64, bool) {
	if len(b) < 6 {
		return 0, false
	}
	switch {
	case b[0]&0xC0 == 0x40: // MPEG-2: 33-bit base and 9-bit extension
		base := int64(b[0]>>3&7)<<30 | int64(b[0]&3)<<28 | int64(b[1])<<20 | int64(b[2]>>3)<<15 |
			int64(b[2]&3)<<13 | int64(b[3])<<5 | int64(b[4]>>3)
		ext := int64(b[4]&3)<<7 | int64(b[5]>>1)
		return base*300 + ext, true
	case b[0]&0xF0 == 0x20: // MPEG-1: a 33-bit base at 90 kHz
		base := int64(b[0]>>1&7)<<30 | int64(b[1])<<22 | int64(b[2]>>1)<<15 | int64(b[3])<<7 | int64(b[4]>>1)
		return base * 300, true
	}
	return 0, false
}

// readMPEGPSVideo reads Video from an MPEG program stream.
func readMPEGPSVideo(r io.ReaderAt, size int64) (Video, error) {
	if !isMPEGPS(r, size) {
		return Video{}, formatError("MPEG program stream")
	}
	v := Video{Orientation: 1}
	head := make([]byte, min(size, maxPSScan))
	if _, err := r.ReadAt(head, 0); err != nil && err != io.EOF {
		return v, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	first, ok := packSCR(head[4:])
	if !ok {
		return v, fmt.Errorf("%w: a pack header cut short", ErrMalformed)
	}
	// The sequence header: 12 bits of width, 12 of height.
	if i := bytes.Index(head, []byte{0, 0, 1, 0xB3}); i >= 0 && i+7 <= len(head) {
		b := head[i+4:]
		v.Width = int(b[0])<<4 | int(b[1]>>4)
		v.Height = int(b[1]&0x0F)<<8 | int(b[2])
	}
	// The last pack, near the end.
	tail := head
	if size > int64(len(head)) {
		tail = make([]byte, min(size, maxPSBack))
		if _, err := r.ReadAt(tail, size-int64(len(tail))); err != nil && err != io.EOF {
			return v, fmt.Errorf("%w: %v", ErrMalformed, err)
		}
	}
	if i := bytes.LastIndex(tail, packStart); i >= 0 {
		if last, ok := packSCR(tail[i+4:]); ok && last > first {
			// 27 MHz ticks to nanoseconds, without overflow.
			v.Duration = time.Duration((last - first) * 1000 / 27)
		}
	}
	return v, nil
}
