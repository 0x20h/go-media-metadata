# Changelog

## Unreleased

### Added

- `XMPEdit` writes dates: `DateCreated`, `CreateDate`,
  `DateTimeOriginal` and `ModifyDate` (`photoshop:DateCreated`,
  `xmp:CreateDate`, `exif:DateTimeOriginal`, `xmp:ModifyDate`). Each is
  written to its precision, with its offset when known, or removed. A
  date corrected in Exif can now be corrected in XMP in the same edit. See
  [docs/xmp.md](docs/xmp.md).
- `XMPEdit` writes a position: `GPS` sets `exif:GPSLatitude`,
  `exif:GPSLongitude` and the altitude as ExifTool writes them;
  `RemoveGPS` removes them. A place corrected in Exif can now be
  corrected in XMP in the same edit. See [docs/xmp.md](docs/xmp.md).

### Fixed

- `SetOrientation`, `SetRotation` and their plans set the orientation of
  a TIFF or DNG whose IFD0 has no Orientation tag, which was refused (ErrUnsupported).
  Most scanners' and converters' TIFFs have none. A copy of IFD0 with the
  tag goes to the end of the file and the header points to it, so no
  other byte moves and every offset stays valid. ExifTool validates the
  result as it did the source. See [docs/raw.md](docs/raw.md).

- `ReadVideo` reads a movie's description from an item list's `©des`
  and from the 3GPP `dscp` box in user data (and the title from `titl`),
  where ExifTool writes ItemList and UserData descriptions. Before, only
  `desc` and QuickTime `©des` were read. The item list comes before the
  user data, as in ExifTool. See [docs/quicktime.md](docs/quicktime.md).

## v0.2.1 (2026-11-16)

No breaking changes.

### Added

- `XMP.ModifyDate`: `xmp:ModifyDate`, read like `xmp:CreateDate` and
  merged by `MergeXMP`. It's for an importer that falls back to the
  modification date last, as ExifTool's date order does. `TakenFrom`
  doesn't use it.
- `ReadVideo`, `ReadAll` and the other readers read a movie cut short (an
  `mdat` past the end) or with junk after its last box, as ExifTool does,
  as long as its `moov` is whole. Writes still refuse such a file. See
  [docs/quicktime.md](docs/quicktime.md).
- AVI and MPEG program streams (`.mpg`, `.vob`), read only. `ReadVideo`
  reads an AVI's recording date (IDIT, else a whole ICRD date), duration
  and size (avih), title, comment and keywords (INFO). For an MPEG program
  stream it reads the size (the sequence header) and the duration (the
  video's first to last PTS, plus a frame). `ReadAll`, `ReadImageSize` and
  `ReadOrientation` take them too; writes are refused. See
  [docs/quicktime.md](docs/quicktime.md).
- `ParseXMP` reads a packet malformed only by characters XML doesn't
  allow, as ExifTool does: a control character, bytes that aren't UTF-8, a
  bare `&`. It returns the fields with the new `ErrXMPRepaired`, which
  `ReadAll` reports in `Errors.XMP`. Writes still refuse such a packet. See
  [docs/xmp.md](docs/xmp.md).

### Fixed

- A HEIF file with several XMP items is read instead of refused ("two XMP
  items"). The image's XMP is the item that describes the primary image,
  else the lowest id among the items that describe no other image, so not
  a gain map's. Writes change only that item and keep the others byte for
  byte. See [docs/heif.md](docs/heif.md).

## v0.2.0 (2026-11-15)

### Breaking changes

- `XMPDate` is now `Date`, since IPTC dates use it too. It was added
  after v0.1.0, so only code built on a commit after that tag is
  affected.

### Added

- `ReadAll`: everything an importer needs in one call, one parse per
  container for JPEG, HEIF, PNG and GIF, with each part's error kept
  apart (`Metadata.Errors`). It equals the separate calls on every corpus
  file and is 2.5 to 5 times faster than them. See [docs/api.md](docs/api.md).
- `ReadOrientation` reads GIF and BMP as 1 (they keep no orientation), as
  `ReadImageSize` reads their size.
- Matroska and WebM, read only: `ReadVideo` reads the duration, the
  recording date (`DateUTC`), the title (Info's, else the file's `TITLE`
  tag), the first video track's size and its rotation (a projection's
  roll). It reads only the SeekHead, Info, Tracks and Tags, never the
  Clusters. `ReadImageSize` and `ReadOrientation` take them too. See
  [docs/matroska.md](docs/matroska.md).
- Windows' XP tags: `Exif.XPComment`, `XPAuthor`, `XPKeywords` and
  `XPSubject` (UTF-16LE in IFD0), read as `XPTitle` is. Read only.
- `Exif.DateTime` (IFD0's DateTime, ExifTool's ModifyDate) with
  SubSecTime and OffsetTime. It is `ReadTaken`'s last source
  (`SourceExifDateTime`), after IPTC, for files stripped of the others.
- `Video.MovieCreated`: the movie header's creation time on its own
  (ExifTool's QuickTime:CreateDate), next to `Created`, which prefers
  Keys and `©day`.
- GIF and BMP, read only: `ReadImageSize` gives a GIF's logical screen
  size and a BMP's (a top-down image's too); `ReadXMP` reads a GIF's XMP
  application extension; the new `ReadComment` returns a GIF's comment
  extensions. See [docs/gif-bmp.md](docs/gif-bmp.md).
- Video reads: the place from 3GPP `loci`, and the date and place from
  the movie's XMP (an MP4's `uuid` box, a QuickTime movie's `XMP_`) when
  Keys and the user data have none. `Video.Shown()` gives the size as
  shown. `ReadXMP` takes movies. A video corpus (`testdata/video`) is
  checked against ExifTool. See [docs/quicktime.md](docs/quicktime.md).
- Video title, description, keywords and rating. `ReadVideo` reads them
  from Keys, the user data, or an MP4 item list. `VideoEdit` sets or
  removes them: in Keys (`com.apple.quicktime.title`, `.description`,
  `.keywords`, `.rating.user`), and in the user data's `©nam` and `©des`
  and an MP4 item list's `©nam`, `desc` and `keyw` where the movie has
  them. See [docs/quicktime.md](docs/quicktime.md).
- MPEG-2 transport streams (TS, and AVCHD's MTS and M2TS), read only.
  `ReadVideo` reads the recording date with its offset, the GPS position,
  make and model from the H.264 stream's MDPM, the size from its SPS, and
  the duration from the clock references. `ReadImageSize` and
  `ReadOrientation` take them too. See [docs/m2ts.md](docs/m2ts.md).
- Face regions at full fidelity. `Region` gains `Unit` (pixel areas were
  left out before), `D` (circles), `Rotation`, `Description`,
  `FocusUsage` and `BarCodeValue`. Fields it doesn't model
  (`mwg-rs:Extensions`, `SeeAlso`, others) are kept and written back
  with their region. See [docs/xmp.md](docs/xmp.md).
- `HashImageData`: a file's image data without its metadata, written to a
  hash, equal to ExifTool's ImageDataHash. A write that changes only
  metadata leaves it as it was. See [docs/api.md](docs/api.md).
- `ReadImageSize` and `ImageSize.Shown`: an image's width and height as
  stored (from the frame header, IHDR, VP8X/VP8/VP8L, ispe, the JPEG XL
  SizeHeader, a RAW's full-size image or a movie's video track) and as
  shown after its orientation. See [docs/api.md](docs/api.md).
- `Exif.Camera`: make, model, lens make and model, ISO, f-number, exposure
  time and focal length (also in 35 mm terms), read only, from every format
  `ReadExif` takes. See [docs/exif.md](docs/exif.md).
- `MergeXMP`: a file's XMP combined with its sidecar's (`IMG_1.xmp` next
  to a RAW), in the Metadata Working Group's order: each value the
  sidecar has wins. `XMP.Orientation` reads `tiff:Orientation`. See
  [docs/xmp.md](docs/xmp.md).
- `ReadTaken` and `TakenFrom`: the date a photo was taken, from the first
  of Exif DateTimeOriginal, Exif DateTimeDigitized, XMP
  exif:DateTimeOriginal, photoshop:DateCreated, xmp:CreateDate and IPTC
  Date/Time Created, with the source it came from. See
  [docs/exif.md](docs/exif.md).
- `Exif.DateTimeDigitized` and `Exif.DigitizedOffsetKnown` (ExifTool's
  CreateDate, with its sub-seconds and offset), `XMP.DateTimeOriginal`
  (exif:DateTimeOriginal) and `IPTC.DateCreated` (2:55 and 2:60).
- `Exif.ContentIdentifier`: the Apple maker note's ContentIdentifier, the
  still half of a Live Photo's pairing (`Video.ContentIdentifier` is the
  movie's), from HEIC and JPEG. See [docs/exif.md](docs/exif.md).
- `XMP.GPS`: the position in `exif:GPSLatitude`, `exif:GPSLongitude` and
  `exif:GPSAltitude`/`GPSAltitudeRef`, in the `DDD,MM,SSk` and `DDD,MM.mmk`
  forms and as decimal degrees.
- `XMP.DateCreated` (`photoshop:DateCreated`) and `XMP.CreateDate`
  (`xmp:CreateDate`), as the new `Date`: the time, whether a time zone
  was written, and how precise the date is (year through fractional
  seconds). See [docs/xmp.md](docs/xmp.md).

### Fixed and clarified

- A JPEG write is refused (`ErrUnsupported`) when the file's MPF index
  puts an image among the marker segments the write changes, rather than
  leave the index pointing at other bytes. Real Motion Photos and Ultra
  HDR files keep their images after the primary and are written as
  before. v0.1.0 moved such an image's offset off its bytes.
- HEIC orientation: when the Exif Orientation tag disagrees with `irot`
  and `imir`, the properties win, as Apple's ImageIO shows the image
  (checked by a test). `Exif.Orientation` gives the tag as written.
  ExifTool's `Orientation` is that tag; it reports `irot` as
  `QuickTime:Rotation`. See [docs/heif.md](docs/heif.md).
- Benchmarks against ExifTool, re-measured for this release: `ReadAll`
  is 24× faster than ExifTool reading the same tags (69× for HEIC), and
  writes 5× faster. See [docs/benchmarks.md](docs/benchmarks.md).
- CI now runs every test that compares this package with ExifTool,
  against a pinned ExifTool, and keeps a failing fuzz input as an
  artifact.

## v0.1.0 (2026-11-14)

The first tagged release. The API was reviewed before it (#54), and the
changes below break code written against earlier commits.

### Breaking changes

**One naming scheme.** `Plan…` computes an edit of a file, `Set…`
streams the same edit, `Edit…` changes metadata held in memory. Removed
or renamed to fit:

| Before | Now |
|---|---|
| `PlanExifEdit(src, size, edit)` | `PlanMetadata(src, size, MetadataEdit{Exif: &edit})` |
| `EditHEIFExif(src, size, dst, edit)` | `SetMetadata(src, size, dst, MetadataEdit{Exif: &edit})` |
| `PlanXMPEdit(src, size, edit)` | `PlanMetadata(src, size, MetadataEdit{XMP: &edit})` |
| `EditFileXMP(src, size, dst, edit)` | `SetMetadata(src, size, dst, MetadataEdit{XMP: &edit})` |
| `PlanVideoEdit` | `PlanVideoMetadata` |
| `EditVideo` | `SetVideoMetadata` |
| `PlanRAWOrientation`, `SetRAWOrientation` | `PlanOrientation`, `SetOrientation` |
| `PlanVideoOrientation`, `SetVideoOrientation` | `PlanOrientation`, `SetOrientation` |
| `PlanVideoRotation`, `SetVideoRotation` | `PlanRotation`, `SetRotation` |

**Rotation now applies to every supported format.** `ReadOrientation`,
`PlanOrientation`, `SetOrientation`, `PlanRotation` and `SetRotation` tell
the format from the file and take HEIF, JPEG, PNG, WebP, camera RAW
(TIFF-based and CR3), QuickTime and MP4. They used to take HEIF and JPEG
only, and returned `ErrNotHEIF` for anything else. Code that used that
error to mean "not mine, use another tool" must now decide by format
itself before calling. `ReadOrientation` also reads a movie's orientation
(its first video track's matrix) and a JPEG XL codestream's. JPEG XL and
RAF orientations aren't written: `ErrUnsupported`.

**Errors.**

- Added `ErrUnknownFormat`: a file of no format the function takes.
- Removed `ErrNotHEIF`, `ErrNotJPEG`, `ErrNotPNG`, `ErrNotWebP`,
  `ErrNotRAW`, `ErrNotQuickTime` (and, unreleased, `ErrNotJXL`). Each case
  is now an error that is `ErrUnknownFormat` (`errors.Is`), its text
  naming the format. A file of no supported format used to give an error
  that was both `ErrNotJPEG` and `ErrNotHEIF`.
- `ErrUnsupported` is no longer HEIF-specific: "mediameta: unsupported
  file structure", for any format.
- Every error's text starts "mediameta: ". `ErrMalformed`'s is
  "mediameta: malformed file".

### Formats in this release

- HEIF, HEIC, AVIF: orientation (`irot`, `imir`, with thumbnails and gain
  maps), Exif and XMP items, read and written.
- JPEG: orientation, Exif, XMP (with extended XMP) and IPTC-IIM, read and
  written; Motion Photo videos and MPF images (Ultra HDR gain maps)
  located and kept on every write.
- PNG and WebP: orientation, Exif and XMP, read and written.
- JPEG XL: Exif and XMP (plain and Brotli-compressed boxes), read and
  written; the codestream's orientation read.
- Camera RAW (DNG, CR2, CR3, NEF, ARW, ORF, RW2; RAF read only): Exif,
  XMP and orientation read, the orientation written in place; embedded
  JPEG previews located.
- XMP fields, in every format with XMP: title, description, keywords,
  hierarchical keywords (`lr:hierarchicalSubject`, as written: levels
  separated by `|`), rating, people shown and MWG face regions.
- QuickTime and MP4: date, place, orientation, size, make, model and the
  Live Photo content identifier read; orientation, date and place
  written.

### Behaviour worth knowing

- A JPEG's IPTC-IIM is read from its first IPTC resource, and a write
  keeps only that one. Apple ImageIO's XMP-only JPEGs carry an empty
  first resource and a second one mirroring the XMP: after an IPTC write
  the mirror is gone, the XMP keeps the values (see `docs/iptc.md`).

### Dependencies

Go 1.22, and `github.com/andybalholm/brotli` (pure Go, MIT) for reading
Brotli-compressed JPEG XL boxes, used in `jxl_brob.go` only.
