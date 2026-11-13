# JPEG

ITU-T T.81 marker segments, with Exif (CIPA DC-008) and XMP (XMP
Specification Part 3) in APP1 segments. A file is JPEG when it starts
with SOI and another marker (`FF D8 FF`).

## Segment walk

The walk reads marker segments up to the first SOS (start of scan) and
never reads the compressed image data after it. It accepts fill bytes
before a marker and RST and TEM markers, which have no length. It
rejects a length below 2, a segment past the end of the file, a second
SOI, and EOI before the scan (`ErrMalformed`). At most 4096 segments are
walked.

Every write copies the scan data byte for byte. Tests check that the
image still decodes to the same size and that everything from SOS on is
unchanged.

## Exif (APP1 `Exif\0\0`)

- **Reading.** The first APP1 segment that starts with `Exif\0` and holds
  TIFF data counts. The pad byte after `Exif\0` may be 0xFF, as some
  writers put it. A segment without TIFF data is skipped for the next
  one, as ExifTool does. The fields are read as [exif](exif.md)
  describes.
- **Writing** (`SetExif`, `PlanExif`, `PlanExifEdit`):
  - The first Exif segment is written in place when the new TIFF data
    fits, with the rest padded with zeros. The segment's own header bytes
    stay as they were.
  - Otherwise a replacement segment is written, and the old one is not
    copied.
  - A JPEG without Exif gets a segment after SOI and any JFIF (APP0)
    segments.
  - Exif past a segment's 64 KB is refused (`ErrUnsupported`).

## Orientation

A JPEG is shown by its Exif Orientation tag. `ReadOrientation` returns 1
without a valid one (1 to 8), as readers do.

`SetRotation`, `SetOrientation` and their plans set the tag, keeping a
mirror as for HEIF:

- **The tag exists** in the first Exif segment's IFD0: only its value
  changes, in the width of its type (BYTE, SHORT or LONG). The file
  keeps its size, and the plan is a single patch.
- **No Exif at all:** a new APP1 goes after SOI and JFIF. It holds
  Orientation and the YCbCrPositioning tag Exif requires of a JPEG (1,
  centred, as JFIF specifies), so ExifTool's validation passes the file.
  Orientation 1 adds nothing.
- **Exif without the tag:** `ErrUnsupported`, unless nothing changes,
  because adding the tag means rewriting IFD0. `PlanExifEdit` with an
  `ExifEdit` that sets the orientation does that rewrite through
  `EditExif` (see [exif](exif.md)).

ExifTool reads the orientation written, and its `-validate` reports no
warning that the source didn't already have.

## XMP (APP1 `http://ns.adobe.com/xap/1.0/\0`)

**Reading.** `ReadXMP` returns the first XMP segment's packet, with any
NUL padding trimmed.

**Extended XMP** (Part 3, 1.1.3.1). A packet past one segment's 64 KB is
split in two. The standard part names the extended part in
`xmpNote:HasExtendedXMP`, by the MD5 of its serialization (32 uppercase
hex digits). The extended part follows in APP1 segments
(`http://ns.adobe.com/xmp/extension/\0`), each holding the GUID, the full
length, its chunk's offset and the chunk.

- `ReadXMP` assembles the chunks with the standard part's GUID, checks
  that their lengths agree and that they cover the whole part (16 MiB at
  most), and merges it in. The caller gets one packet, without
  `HasExtendedXMP`.
- An extended part that is incomplete, or named by a different GUID, is
  ignored, as ExifTool does.

**Writing** (`SetXMP`, `PlanXMP`):

- **Fits one segment:** a packet that fits the old segment is written
  there in place, padded with whitespace before `<?xpacket end`, so the
  plan's patches stay inside that segment. A longer packet replaces the
  segment. A JPEG without XMP gets the segment after its Exif segment, or
  after JFIF.
- **Too big for one segment:** the largest top-level properties move to
  an extended part until the rest fits. Its chunks (65400 bytes each) go
  right after the standard segment.
- **Old extended segments** are always removed.

ExifTool reads a 180 KB split packet as one, with no warning.
