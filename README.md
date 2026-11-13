# go-media-metadata

go-media-metadata reads and writes metadata of image and video files in
pure Go, with no cgo and no external tools. Writes are lossless: only the
metadata asked for changes, and the encoded image data is copied byte for
byte.

The module is `github.com/0x20h/go-media-metadata`; its package is named
`mediameta`.

## Install

```sh
go get github.com/0x20h/go-media-metadata
```

## Use

```go
import mediameta "github.com/0x20h/go-media-metadata"

in, err := os.Open("photo.heic")
// ...
info, _ := in.Stat()

o, err := mediameta.ReadOrientation(in, info.Size())
fmt.Println(o) // e.g. "rotated 90° clockwise"

// Exif of a JPEG or HEIF file: orientation, when it was taken, where.
x, err := mediameta.ReadExif(in, info.Size())
fmt.Println(x.DateTimeOriginal, x.OffsetKnown) // the offset, when the camera wrote one
if x.GPS != nil {
	fmt.Println(x.GPS.Latitude, x.GPS.Longitude)
}

// Rotate losslessly: write to a temporary file, then rename it over the
// original once SetRotation has succeeded.
tmp, err := os.CreateTemp(filepath.Dir("photo.heic"), "photo-*.heic")
// ...
err = mediameta.SetRotation(in, info.Size(), tmp, 180)

// Or plan the edit, and where it keeps the file's size, patch a copy of
// the file in place (e.g. a clone of it): a few bytes, whatever its size.
plan, err := mediameta.PlanRotation(in, info.Size(), 180)
// ...
if plan.InPlace {
	err = plan.Apply(clone) // an io.WriterAt holding the source's bytes
} else {
	_, err = plan.WriteTo(tmp)
}
```

## Status

Early. HEIF/HEIC orientation, and reading Exif from JPEG and HEIF:

- `ReadOrientation` returns an image's orientation (Exif numbering 1–8):
  for HEIF from its `irot` and `imir` properties, for JPEG from its Exif
  Orientation tag.
- `ReadExif` reads a JPEG's Exif APP1 segment or a HEIF file's Exif item:
  the Orientation tag, DateTimeOriginal with its fraction of a second and
  its offset (OffsetTimeOriginal), and the GPS position and altitude.
  Its results are checked against ExifTool on the test files.
- Camera RAW: `ReadExif`, `ReadOrientation` and `ReadXMP` also read
  TIFF-based RAW files (DNG, CR2, NEF, ARW, ORF, RW2; an RW2's Exif from
  its embedded JPEG) and CR3 (its CMT boxes and XMP box), reading the
  IFDs from the file as needed, never the image data.
  `SetRAWOrientation` and `PlanRAWOrientation` change only the
  Orientation value where it sits in IFD0 (CR3: CMT1), and the
  `tiff:Orientation` digit of an XMP packet, in place. Nothing else is
  written, so maker notes and image data stay byte for byte. A file
  without the tag is refused rather than restructured.
- `EditHEIFExif` and `PlanExifEdit` apply an `ExifEdit` to a HEIF
  file's Exif item (or add one). A date or GPS change that fits is a
  patch in place. The orientation is left to `SetOrientation`, since
  HEIF readers show `irot` and `imir`, not the Exif tag.
- PNG and WebP: `ReadExif`, `ReadOrientation` and `ReadXMP` read a
  PNG's `eXIf` chunk and XMP `iTXt` chunk (compressed or not) and a
  WebP's `EXIF` and `XMP ` chunks; `PlanExif`, `PlanXMP` and
  `PlanExifEdit` (and the Set functions) write them, the image data
  untouched. A value that fits its chunk is written there, padded, with
  the PNG chunk's CRC rewritten: in place. A longer one, or a new one,
  rewrites the file: a new PNG chunk goes right after `IHDR`, a new WebP
  chunk after the image, with the `VP8X` flags and RIFF size updated (a
  simple WebP gets the `VP8X` header metadata needs). Old values never
  stay in the file. In these formats the Exif Orientation is how the
  image is shown, so `PlanExifEdit` sets it too.
- `EditExif` writes those fields back into TIFF data, for a JPEG's Exif
  segment or a HEIF Exif item: orientation, the date taken and the date
  digitized (ExifTool's CreateDate), each with fraction and offset, GPS
  position and altitude, or the position removed. Every other
  byte keeps its offset, so unknown tags, the thumbnail and maker notes
  stay valid. A value that fits is written in place; a longer value, or
  an IFD that needs a new entry, goes at the end. Old values and moved
  IFDs are zeroed, so a removed GPS position doesn't stay in the data.
- For JPEG, `PlanExifEdit`, `PlanExif` and `SetExif` write the first Exif
  segment: in place when the new data fits in it (padded with zeros), a
  replacement segment otherwise (the old one isn't copied), a new segment
  after the JFIF one where the file has none. Exif past a segment's 64 KB
  is refused with `ErrUnsupported`. The scan data is copied byte for byte.
- `SetRotation` rotates an image losslessly by setting the `irot` property
  of its primary item, moving item offsets where the metadata grows or
  shrinks, and keeping the Exif Orientation tag in step where one exists.
- For JPEG, `SetRotation`, `SetOrientation` and their plans set the Exif
  Orientation tag: in place where the first Exif segment has it (only its
  bytes change, the plan is one patch), in a new Exif segment after the
  JFIF one where the file has no Exif. The scan data is copied byte for
  byte; ExifTool reads and validates the files written.
- `SetOrientation` sets any of the eight orientations, mirrored ones
  included, through `irot` and `imir` in the order ISO/IEC 23008-12
  applies them (rotation, then mirror). On a mirrored image `SetRotation`
  keeps the mirror first, as the Exif numbering does: orientation 2 turned
  90° clockwise is 7.
  `imir`'s axis is read and written as Apple's ImageIO and libheif have
  it: axis 1 mirrors left-right, axis 0 top-bottom. A test decodes every
  orientation with libheif and compares the pixels, where it's installed.
- `SetExif` and `SetXMP` replace or add the Exif and XMP items with data
  the caller encoded (TIFF data; an XMP packet), and `ReadXMP` reads the
  XMP item. A value that fits the item's data is patched there, in place;
  a longer one goes in a new `mdat` box at the end. A missing item is
  listed in `iinf` with a `cdsc` reference to the primary image, and the
  item offsets the `meta` box's growth moves are moved. Image data is
  copied byte for byte. `PlanExif` and `PlanXMP` are the same edits as
  plans.
- `PlanRotation` and `PlanOrientation` compute the same edits without
  writing them: a `Plan` that
  streams the new file (`WriteTo`) or, when the file keeps its size, lists
  the bytes that change as patches to apply to a copy (`Apply`). Keeping
  the mirroring (or its absence) is in place where the image has an `irot`
  of its own; adding or removing a mirror changes the file's size.
- QuickTime (MOV) and MP4: `ReadVideo` reads a movie's recording date,
  duration, GPS position, the first video track's orientation and size,
  the recording device's make and model, and the content identifier
  that pairs a Live Photo's video with its still image. It takes the
  date, place, make and model from the Keys metadata
  (`com.apple.quicktime.*`, in a QuickTime or an MP4-style `meta`
  box), else the user data (`©day`, `©xyz`, `©mak`, `©mod`), and the
  date last from the movie header (UTC). The orientation comes from the
  track header's matrix, in the Exif numbering images use: the four
  rotations and four mirrorings, 0 for any other matrix. Only box
  headers and the few small boxes these fields are in are read, never
  the sample tables or the media data, wherever `moov` sits: 870 bytes
  of a 1 MB test movie. Results are checked against ExifTool on the
  test movies. Writing is next: the rotation (a same-size patch of the
  track matrix), then date and place.

### Compared with other tools

- **Minimal writes.** Where an image already has an `irot` property of its
  own, `SetRotation` changes its one angle byte and nothing else moves. A
  new `irot` is appended to the property container and associated as an
  essential property after the descriptive ones (and before any `imir`),
  the position Apple's ImageIO uses; item offsets are moved only when the
  `meta` box grows ahead of the image data. No brands, items or properties
  are added or reordered otherwise.
- **The Exif Orientation tag is kept in step, deliberately.** Apple's
  ImageIO leaves a HEIC's Exif Orientation as it was when it rotates the
  image (so a file can say 6 in Exif and 180° in `irot`), which makes
  tools that read Exif rather than `irot` show it wrongly. `SetRotation`
  sets an existing tag to the orientation the file now shows; a file
  without the tag doesn't get one.

## Guarantees

- **Lossless:** image data is never decoded or re-encoded; tests check it
  is byte-identical after every write.
- **Safe on hostile input:** every parser is fuzzed. Lengths are checked,
  counts are bounded by the bytes available, nesting is capped. Malformed
  input gives an error, never a panic or an endless loop.
- **Never the source:** a write reads the source and writes a new file to
  the `io.Writer` you pass. On an error that writer may hold part of a
  file, so write to a temporary file and rename it over the original only
  once the call has succeeded. `Plan.Apply` writes only to the copy you
  pass, and only when the plan is in place: the patches then hold every
  byte that changes, and applied to a copy they give byte for byte what
  `WriteTo` writes.

## Specifications

- ISO/IEC 14496-12, ISO base media file format
- ISO/IEC 23008-12, High Efficiency Image File Format (HEIF)
- ITU-T T.81 (JPEG) for the marker segments
- CIPA DC-008 (Exif 2.32): the Orientation, date and time, and GPS tags
- Apple QuickTime File Format: movie and track headers, the Keys
  metadata (`keys`, `ilst`) and user data text entries
- ISO 6709 for the location strings, ISO 8601 for the dates

## Licence

MIT, see [LICENSE](LICENSE). Test data: see [NOTICE](NOTICE).
