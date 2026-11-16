# MPEG-2 transport streams: TS, MTS, M2TS

ISO/IEC 13818-1 transport streams: `.ts`, and AVCHD's `.mts` and `.m2ts`
from camcorders and some cameras, whose 192-byte packets carry a 4-byte
timestamp before the 188-byte packet. Read only.

## Reading: ReadVideo

`ReadVideo` takes a transport stream as it takes a movie. It finds the
first program (PAT, then PMT) and its first H.264 video stream (stream
type 0x1B), and reads:

- **The recording date, GPS position, make and model** from the stream's
  MDPM. A camcorder writes it as an H.264 SEI message of unregistered
  user data (UUID `17ee8c60-f84d-11d9-8cd6-0800200c9a66`, then `MDPM`):
  a count, then entries of a tag byte and 4 bytes each.
  - Tags 0x18 and 0x19 hold the date and time in BCD, after a time zone
    byte. In that byte, 0x20 means west of UTC, bits 0x1E are the hours,
    0x01 adds half an hour, and 0x40 marks daylight saving time.
  - `Created` is in that offset with `OffsetKnown` true. The daylight
    saving flag is a flag beside the offset, not added to it; ExifTool
    shows it the same way.
  - Tags 0xB1 to 0xBA hold GPS, as Exif's fields with 16-bit rationals.
  - Tag 0xE0 holds the make, as a code: Panasonic, Sony, Canon or JVC.
  - A Sony writes its model name in tags 0xE4 to 0xE6; other makes have
    no model in MDPM.
- **The size** comes from the sequence parameter set (H.264 7.3.2.1.1),
  with its cropping applied.
- **The duration** is the span from the first program clock reference to
  the last. The last is searched for in the final 512 KB of the file.

MDPM comes with the first picture, ahead of its first slice. A stream
whose first slice has gone by without one has none, and reading stops
there. At most the first 16 MB are read from the start.

A transport stream has no orientation: `Orientation` is 1. Writes
(`PlanOrientation`, `PlanRotation`, `PlanVideoMetadata`, …) return an
error that is `ErrUnsupported`. The metadata is inside the coded video,
and changing it would mean re-packetizing the stream.

## Checks

`TestTSAgreesWithExifTool` compares the date, GPS, model and duration
with ExifTool's, and the size with ffprobe's (ExifTool gives none for
these files). It runs on `testdata/m2ts`: synthetic test patterns made by
`make.sh` with ffmpeg, with an MDPM spliced in by `splice.go.txt`.
One file is at +02:00, one at −03:30 with daylight saving time, and a
plain TS has no MDPM. FuzzReadVideo starts from them too.
