package mediameta

import (
	"bytes"
	"encoding/binary"
	"time"
)

// Exif in camera videos: some cameras write a standard Exif block (IFD0,
// with the Exif and GPS IFDs it points to) into a movie's user data or an
// AVI's chunks, as ExifTool documents them:
//
//   - QuickTime user data QVMI (Casio): IFD0 10 bytes into the box's data,
//     big-endian, offsets from the start of the data.
//   - QuickTime user data MVTG (Fujifilm): IFD0 16 bytes in,
//     little-endian, offsets from IFD0.
//   - QuickTime user data RMKN (Ricoh) and SIGM (Sigma): TIFF data, header
//     included.
//   - AVI strd chunk in a stream's strl, starting "AVIF": IFD0 8 bytes in,
//     little-endian, offsets from IFD0.
//   - AVI top-level JUNK chunk starting "QVMI" (Casio): IFD0 10 bytes in,
//     big-endian, offsets from the start of the chunk's data.
//
// They give the date taken, the make, model and lens, and the GPS
// position, where the movie's own fields don't.

// embeddedExif reads Exif from IFD0 at ifd in data, whose offsets count
// from base, in order. ok is false for a block with nothing read.
func embeddedExif(data []byte, ifd, base int, order binary.ByteOrder) (Exif, bool) {
	if base < 0 || ifd < base || ifd+2 > len(data) {
		return Exif{}, false
	}
	t := tiffReader{data: data[base:], order: order, headerless: true}
	return usefulExif(t.exif(t.ifd, uint32(ifd-base)))
}

// usefulExif is x, and whether it holds anything a movie takes from it.
func usefulExif(x Exif) (Exif, bool) {
	ok := !x.DateTimeOriginal.IsZero() || !x.DateTimeDigitized.IsZero() || x.GPS != nil ||
		x.Camera.Make != "" || x.Camera.Model != "" || x.Camera.LensModel != ""
	return x, ok
}

// udtaExif reads the Exif of a user data box of type typ (its data b).
func udtaExif(typ string, b []byte) (Exif, bool) {
	switch typ {
	case "QVMI":
		return embeddedExif(b, 10, 0, binary.BigEndian)
	case "MVTG":
		return embeddedExif(b, 16, 16, binary.LittleEndian)
	case "RMKN", "SIGM":
		return usefulExif(parseExif(b))
	}
	return Exif{}, false
}

// aviStreamExif reads the Exif of an AVI strd chunk's data b.
func aviStreamExif(b []byte) (Exif, bool) {
	if !bytes.HasPrefix(b, []byte("AVIF")) {
		return Exif{}, false
	}
	return embeddedExif(b, 8, 8, binary.LittleEndian)
}

// aviJunkExif reads the Exif of an AVI JUNK chunk's data b (Casio's).
func aviJunkExif(b []byte) (Exif, bool) {
	if !bytes.HasPrefix(b, []byte("QVMI")) {
		return Exif{}, false
	}
	return embeddedExif(b, 10, 0, binary.BigEndian)
}

// exifTaken is an embedded Exif's date taken: DateTimeOriginal, else
// DateTimeDigitized; zero without either.
func exifTaken(x Exif) (time.Time, bool) {
	if !x.DateTimeOriginal.IsZero() {
		return x.DateTimeOriginal, x.OffsetKnown
	}
	return x.DateTimeDigitized, x.DigitizedOffsetKnown
}

// makerTaken is exifTaken of x, where there is one.
func makerTaken(x *Exif) (time.Time, bool) {
	if x == nil {
		return time.Time{}, false
	}
	return exifTaken(*x)
}
