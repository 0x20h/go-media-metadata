# Changelog

## v0.1.0 (unreleased)

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
- QuickTime and MP4: date, place, orientation, size, make, model and the
  Live Photo content identifier read; orientation, date and place
  written.

### Dependencies

Go 1.22, and `github.com/andybalholm/brotli` (pure Go, MIT) for reading
Brotli-compressed JPEG XL boxes, used in `jxl_brob.go` only.
