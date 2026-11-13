# Exif

TIFF IFDs as CIPA DC-008 (Exif 2.32, with Exif 3.0's GPS altitude
references) defines them, in whichever file holds them (see the format
pages).

## Reading

`ReadExif` returns:

- **`Orientation`:** IFD0's Orientation tag, 0 without a valid one
  (1 to 8).
- **`DateTimeOriginal`:** with `SubSecTimeOriginal`'s fraction. With an
  `OffsetTimeOriginal` (`+HH:MM`, `-HH:MM` or `Z`), or without one an
  offset written after the time itself, `OffsetKnown` is true and the time
  is in a fixed zone of that offset. Without either, it is the camera's
  clock time, returned in `time.UTC` with `OffsetKnown` false. That is
  not a UTC time.
- **`GPS`:** latitude and longitude in signed decimal degrees, and the
  altitude when there is one. Nil without a valid position.
- **`ImageDescription`:** IFD0's ImageDescription, its bytes as they are
  up to the NUL. Writers put UTF-8 there, ExifTool among them.
- **`XPTitle`:** IFD0's XPTitle, Windows' title: UTF-16 little-endian,
  up to its NUL.

A field the file lacks, or has in a form this package doesn't accept,
stays at its zero value.

Every offset, count and value size is checked against the data. At most
1000 entries are read per IFD. An Exif or GPS IFD pointer that loops back
to IFD0, or points outside the data, reads as missing.

### Quirks of real writers, compared with ExifTool

Reading was compared with ExifTool 13.44, by its source (`Exif.pm`,
`GPS.pm`) and its output on files that stray from the standard. These
are read as ExifTool reads them:

- Orientation stored as BYTE or LONG, and IFD pointers typed IFD (13).
- GPS references in either case or as words (`S`, `s`, `South`; likewise
  `W`). An empty reference is positive, and a missing one means no
  position.
- Coordinates given as degrees alone, or as degrees and minutes; signed
  rationals; minutes past 60.
- Altitude reference 3 (Exif 3.0, below sea level) reads as below.
- A trailing blank in `OffsetTimeOriginal`, and a first Exif segment
  without TIFF data (see [jpeg](jpeg.md)).

ExifTool passes dates on as text, so how they are read is this package's
own choice:

- Accepted: `-` in the date, `T` before the time, a time without seconds,
  `Z` as an offset, and an offset after the time (`…15+02:00`), which is
  used when there is no `OffsetTimeOriginal`.
- A blank date (`    :  :     :  :  `, the standard's "unknown") or an
  impossible one (24:00, 30 February) is no date.

These differ from ExifTool on purpose, and each is pinned in a test
(`quirks_test.go`):

| Input | Here | ExifTool |
|---|---|---|
| latitude past 90° or longitude past 180° | no position | the number |
| altitude without a reference | above (the standard's default) | no altitude |
| altitude reference 2 (Exif 3.0, above sea level) | above | below |
| `SubSecTimeOriginal` that isn't a number | ignored | its leading digits |
| `TimeZoneOffset` (0x882A, hours) | not read | not combined with the time either |

`TestExifAgreesWithExifTool` compares every field with ExifTool on the
synthetic files, the quirk files where the two agree, and the HEIF
samples. It runs where `exiftool` is installed.

## Writing: EditExif

`EditExif(tiff, ExifEdit)` returns TIFF data with the edit applied. It
can set:

- the orientation;
- the date taken and the date digitized (ExifTool's CreateDate), each with
  fraction and offset;
- the GPS position and altitude, or remove the position;
- `ImageDescription` and `XPTitle`, or remove them (`""`).

The text tags are written as ExifTool writes them:

- **`ImageDescription`:** UTF-8 bytes and a NUL, type ASCII.
- **`XPTitle`:** UTF-16 little-endian and two NULs, type BYTE. A
  character past U+FFFF is a surrogate pair, as Windows reads it.
  ExifTool reads and writes XP tags as UCS-2, so it keeps only such a
  character's low 16 bits; the library's tests compare text within the
  BMP with it.

Text with a NUL in it, or that isn't UTF-8, is refused.

- **Every other byte keeps its offset**, so unknown tags, the thumbnail
  and maker notes, whose own offsets point into the data, stay valid.
- **A value that fits** where the old one was is written there. A longer
  value, or an IFD that needs a new entry, goes at the end of the data,
  and what pointed to it is updated.
- **Old values and moved IFDs are zeroed**, so a removed GPS position
  doesn't stay in the data.
- **With empty data**, `EditExif` makes new TIFF data (big-endian)
  holding the edit.
- **Data that isn't TIFF**, or whose IFDs lie outside it, is refused with
  `ErrMalformed` rather than rewritten.
- **Idempotent:** `EditExif` of its own output with the same edit
  succeeds and returns the data unchanged, byte for byte; a fuzz target
  checks it. To keep that true on malformed data:
  - An IFD a pointer leads to but that doesn't read (its entries run past
    the end) still counts as structure. Nothing that overlaps its count
    field is zeroed or rewritten, which could make it read as an IFD.
  - Values and IFDs that pointers place past the end of the data are
    remembered. An edit that would have to append where one of them lies
    is refused (`ErrUnsupported`), because the appended bytes would
    become that value or IFD.
  - The writer follows an Exif, GPS or interoperability IFD pointer as
    the reader does: one of type BYTE or SHORT, or one stored out of
    line, leads to the same IFD. An IFD found only through such a pointer
    is still protected from being overwritten.
