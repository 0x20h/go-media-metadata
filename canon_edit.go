package mediameta

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"time"

	"github.com/0x20h/go-media-metadata/internal/bmff"
)

// syncCanonDate sets the date of a Canon movie's thumbnail Exif (in
// moov's Canon uuid box: CNTH, its CNDA JPEG) to t, so the file doesn't
// keep a second, older date there: DateTimeOriginal and
// DateTimeDigitized, each where the Exif has it, with its offset where
// it has one. The JPEG's other bytes stay. A JPEG that grows takes the
// room from the padding after it in CNTH, which keeps CNTH's and the
// uuid box's sizes; one that doesn't fit grows them (and moov). A movie
// without the box, or a box this package doesn't walk, is left as it is.
func syncCanonDate(moov *qtNode, t time.Time) error {
	for _, k := range moov.kids {
		if k.typ != bmff.T("uuid") {
			continue
		}
		// The payload with its 16-byte user type: what setPayload takes
		// back (payload skips it).
		if len(k.raw) < 8 {
			continue
		}
		h := 8
		if binary.BigEndian.Uint32(k.raw) == 1 {
			h = 16
		}
		p := k.raw[min(h, len(k.raw)):]
		if len(p) < 16 || !bytes.Equal(p[:16], canonUUID) {
			continue
		}
		body, changed, err := canonUUIDWithDate(p, t)
		if err != nil || !changed {
			return err
		}
		k.setPayload(body)
		// A 64-bit size only where the box had one (Large counts the
		// user type as a long header).
		k.large = h == 16
		return nil
	}
	return nil
}

// canonUUIDWithDate is the Canon uuid box's payload p with the date of
// its thumbnail's Exif set to t.
func canonUUIDWithDate(p []byte, t time.Time) ([]byte, bool, error) {
	kids, _ := bmff.ReadBoxesLenient(bytes.NewReader(p), 16, int64(len(p)))
	cnth, ok := bmff.Find(kids, bmff.T("CNTH"))
	if !ok || cnth.HeaderLen != 8 {
		return nil, false, nil
	}
	inner, _ := bmff.ReadBoxesLenient(bytes.NewReader(p), cnth.Body(), cnth.End())
	if len(inner) == 0 || inner[0].Type != bmff.T("CNDA") || inner[0].HeaderLen != 8 {
		return nil, false, nil
	}
	cnda := inner[0]
	jpeg := p[cnda.Body():cnda.End()]
	x, err := ReadExif(bytes.NewReader(jpeg), int64(len(jpeg)))
	if err != nil {
		return nil, false, nil // no Exif: no date to keep in sync
	}
	edit := ExifEdit{}
	if !x.DateTimeOriginal.IsZero() {
		edit.DateTimeOriginal, edit.OffsetKnown = &t, x.OffsetKnown
	}
	if !x.DateTimeDigitized.IsZero() {
		edit.DateTimeDigitized = &t
	}
	if edit.DateTimeOriginal == nil && edit.DateTimeDigitized == nil {
		return nil, false, nil
	}
	if edit.DateTimeOriginal != nil && edit.DateTimeDigitized != nil && x.OffsetKnown != x.DigitizedOffsetKnown {
		// EditExif writes one offset choice for both.
		return nil, false, fmt.Errorf("%w: a Canon thumbnail whose two dates differ in having an offset", ErrUnsupported)
	}
	if edit.DateTimeOriginal == nil {
		edit.OffsetKnown = x.DigitizedOffsetKnown
	}
	plan, err := planExifEdit(bytes.NewReader(jpeg), int64(len(jpeg)), edit)
	if err != nil {
		return nil, false, err
	}
	var nj bytes.Buffer
	if _, err := plan.WriteTo(&nj); err != nil {
		return nil, false, err
	}
	if bytes.Equal(nj.Bytes(), jpeg) {
		return nil, false, nil
	}
	// CNTH: the new CNDA, then the padding, shortened (or lengthened with
	// zeros) by what CNDA grew (or shrank).
	pad := p[cnda.End():cnth.End()]
	grow := nj.Len() - len(jpeg)
	switch {
	case grow <= len(pad):
		pad = append(pad[:len(pad)-max(grow, 0):len(pad)-max(grow, 0)], make([]byte, max(-grow, 0))...)
	default:
		pad = nil // CNTH grows
	}
	out := make([]byte, 0, len(p)+max(grow, 0))
	out = append(out, p[:cnth.Offset]...)
	cnthLen := 8 + 8 + nj.Len() + len(pad)
	out = binary.BigEndian.AppendUint32(out, uint32(cnthLen))
	out = append(out, "CNTH"...)
	out = binary.BigEndian.AppendUint32(out, uint32(8+nj.Len()))
	out = append(out, "CNDA"...)
	out = append(out, nj.Bytes()...)
	out = append(out, pad...)
	out = append(out, p[cnth.End():]...)
	return out, true, nil
}
