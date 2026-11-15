package mediameta

import (
	"bytes"
	"encoding/binary"
	"os"
	"testing"
)

// appleNote is an Apple maker note as an iPhone writes it: "Apple iOS",
// NUL, version 1, the byte order, and an IFD at offset 14 whose offsets
// count from the note's start, with the ContentIdentifier and another
// entry before it.
func appleNote(order byteOrder, id string) []byte {
	note := []byte(appleMakerNote + "\x00\x01")
	if order.String() == binary.LittleEndian.String() {
		note = append(note, "II"...)
	} else {
		note = append(note, "MM"...)
	}
	value := append([]byte(id), 0)
	note = order.AppendUint16(note, 2)
	// 0x0001, a SLONG version, inline.
	note = order.AppendUint16(note, 0x0001)
	note = order.AppendUint16(note, 9)
	note = order.AppendUint32(note, 1)
	note = order.AppendUint32(note, 14)
	note = order.AppendUint16(note, tagAppleContentIdentifier)
	note = order.AppendUint16(note, typeASCII)
	note = order.AppendUint32(note, uint32(len(value)))
	note = order.AppendUint32(note, uint32(14+2+2*12+4))
	note = order.AppendUint32(note, 0)
	return append(note, value...)
}

// withMakerNote is photoTIFF's Exif with a maker note in its Exif IFD.
func withMakerNote(order byteOrder, note []byte) []byte {
	b := &tiffBuilder{order: order}
	n, date := asciiValue("2025:05:04 13:14:15")
	b.ifd0 = []tiffEntry{{tagOrientation, typeShort, 1, b.short(1)}}
	b.exif = []tiffEntry{
		{tagDateTimeOriginal, typeASCII, n, date},
		{tagMakerNote, typeUndef, uint32(len(note)), note},
	}
	return b.build()
}

const testContentID = "5A7E21BB-FFC6-4275-957A-A864EDD15D84"

func TestAppleContentIdentifier(t *testing.T) {
	for _, order := range []byteOrder{binary.BigEndian, binary.LittleEndian} {
		note := appleNote(order, testContentID)
		if got := appleContentIdentifier(note); got != testContentID {
			t.Errorf("%v: %q, want %q", order, got, testContentID)
		}
		// In a JPEG's Exif, the TIFF in the other byte order: the note
		// has its own.
		other := binary.ByteOrder(binary.LittleEndian)
		if order.String() == other.String() {
			other = binary.BigEndian
		}
		file := withSegments(plainJPEG(t), exifSegment(withMakerNote(other.(byteOrder), note)))
		x, err := ReadExif(bytes.NewReader(file), int64(len(file)))
		if err != nil || x.ContentIdentifier != testContentID || x.DateTimeOriginal.IsZero() {
			t.Errorf("%v in a JPEG: %q, taken %v, %v", order, x.ContentIdentifier, x.DateTimeOriginal, err)
		}
	}
}

// Another maker's note, or an Apple note that's damaged, reads as no
// identifier, and the rest of the Exif as it is.
func TestAppleContentIdentifierDamaged(t *testing.T) {
	good := appleNote(binary.BigEndian, testContentID)
	cases := map[string][]byte{
		"empty":           nil,
		"another maker":   append([]byte("Nikon\x00\x02\x10\x00\x00MM\x00*"), good[14:]...),
		"no byte order":   append(append([]byte(nil), good[:12]...), append([]byte("XX"), good[14:]...)...),
		"header only":     good[:14],
		"half an entry":   good[:14+2+12+6],
		"value cut off":   good[:len(good)-10],
		"count too large": func() []byte { b := bytes.Clone(good); binary.BigEndian.PutUint16(b[14:], 0xFFFF); return b }(),
		"value outside": func() []byte {
			b := bytes.Clone(good)
			binary.BigEndian.PutUint32(b[14+2+12+8:], 0x7FFFFFF0)
			return b
		}(),
		// The IFD's next pointer at itself: only the one IFD is read.
		"looping": func() []byte {
			b := bytes.Clone(good)
			binary.BigEndian.PutUint32(b[14+2+2*12:], 14)
			return b
		}(),
	}
	for name, note := range cases {
		got := appleContentIdentifier(note)
		// A count too large reads the entries that fit, as any IFD.
		if name == "looping" || name == "count too large" {
			if got != testContentID {
				t.Errorf("%s: %q, want the identifier", name, got)
			}
			continue
		}
		if got != "" {
			t.Errorf("%s: %q, want none", name, got)
		}
		file := withSegments(plainJPEG(t), exifSegment(withMakerNote(binary.BigEndian, note)))
		x, err := ReadExif(bytes.NewReader(file), int64(len(file)))
		if err != nil || x.DateTimeOriginal.IsZero() {
			t.Errorf("%s: the rest of the Exif: taken %v, %v", name, x.DateTimeOriginal, err)
		}
	}
}

// An iPhone's HEIC carries the identifier ExifTool reads
// (exiftool -Apple:ContentIdentifier).
func TestAppleContentIdentifierIPhone(t *testing.T) {
	f, err := os.Open("testdata/iphone/grid-gainmap-orientation6.heic")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	info, _ := f.Stat()
	x, err := ReadExif(f, info.Size())
	if err != nil {
		t.Fatal(err)
	}
	if x.ContentIdentifier != testContentID {
		t.Errorf("ContentIdentifier %q, want %q", x.ContentIdentifier, testContentID)
	}
}
