# HEIF / HEIC / AVIF

ISO/IEC 23008-12 on the ISO base media file format (ISO/IEC 14496-12).
A file is HEIF when its first box is `ftyp` with one of the HEIF, AVIF or
MIAF brands.

## Orientation

A HEIF image is shown by the transformative properties of its primary
item, `irot` (rotation, anticlockwise quarter turns) and `imir` (mirror),
applied in the order they are associated (6.5.10, 6.5.12).
`ReadOrientation` returns them as one of the eight Exif orientations, or
1 when the image has neither.

**`imir`'s axis.** Axis 0 mirrors top-bottom (a vertical flip) and axis 1
mirrors left-right (a horizontal flip). That is how Apple's ImageIO
writes and shows it, and how libheif reads it: Apple writes orientation 2
as `irot` 0 with `imir` 1, and orientation 4 as `irot` 0 with `imir` 0.
Where libheif is installed, a test decodes every orientation and compares
the pixels.

**`irot`/`imir` and the Exif tag are never applied together.** Files
written by Apple carry both the properties and a matching Exif
Orientation. HEIF readers show the image by the properties, so this
package reads the orientation only from them. A HEIF file never takes
the JPEG path, which reads the Exif tag.

### Writing

- `SetRotation(degrees)` sets the rotation and keeps any mirror. The
  mirror comes first, as in the Exif numbering: orientation 2 turned 90°
  clockwise is 7.
- `SetOrientation(o)` sets any of the eight orientations. An `imir` the
  image already has is kept for a mirrored `o` (with the rotation chosen
  for its axis) and dropped for one that isn't mirrored. A mirror the
  image needs uses an existing `imir` of the right axis, else a new one.
- A new `irot` goes before the item's `imir`, and a new `imir` after its
  `irot`. A new property is appended to `ipco` and associated as
  essential after the descriptive properties, where ImageIO puts it.
- **Minimal writes.** Where the image has an `irot` that only the rotated
  images use, its single angle byte changes and nothing moves, so the
  plan is in place. An appended property or a widened `ipma` (15-bit
  indices past 127 properties) changes the size of `meta`. Item offsets
  past it are then moved. A movie box (`moov`), whose chunk offsets this
  package doesn't rewrite, makes such an edit `ErrUnsupported`.
- **Related images turn with it.** Thumbnails (`thmb`) and auxiliary
  images (`auxl`: an HDR gain map, alpha, depth) that showed the primary
  image's orientation show the new one. When they share one `irot` with
  the primary, as iPhone files do, that `irot` changes in place.
- **The Exif Orientation tag is kept in step.** ImageIO leaves a HEIC's
  Exif Orientation as it was when it rotates an image, so a file can say
  6 in Exif and 180° in `irot`, and tools that read Exif show it wrongly.
  Here an existing tag is set to the orientation the file now shows. A
  file without the tag doesn't get one.

## Exif and XMP items

- **Reading.** The Exif item is the one linked to the primary image by
  `cdsc`, else the one with the lowest item id, and it must hold TIFF
  data. Its first 4 bytes give the offset to the TIFF header (A.2.1). The
  XMP item is a `mime` item of type `application/rdf+xml`.
- **Writing** (`SetExif`, `SetXMP`, `EditHEIFExif` and their plans):
  - Data that fits in the item's single extent (in `mdat` or `idat`) is
    written there, and the item's length is patched. That is in place.
  - Longer data goes in a new `mdat` box at the end of the file, and the
    old bytes are overwritten with zeros.
  - A missing item is added to `iinf` with a `cdsc` reference to the
    primary image, and the item offsets that the `meta` box's growth
    moves are moved.
  - Items are capped at 16 MiB.
  - A last box whose header says "to the end of the file" (size 0) gets
    its real size before data is appended after it.
  - Refused rather than written (`ErrUnsupported`):
    - a file where an image's data overlaps the `meta` box, which an
      edit rewrites;
    - a file where another item's data runs to or past the end of the
      file, because appended data would join it;
    - a file where a new item wouldn't read back from the rewritten
      `meta` box.
  - A fuzz target writes Exif and XMP items into mutated files: added,
    replaced in place, and moved. Every write that succeeds reads back
    with the new value and the same orientation, leaves the other items'
    data byte for byte, and is in place when written again. ExifTool's
    `-validate` finds no warning in the written files that the source
    didn't already have.
- **`EditHEIFExif` doesn't set the orientation.** It applies an
  `ExifEdit` to the item, but refuses one that sets the orientation
  (`ErrOrientationInExif`), because the tag alone would disagree with
  `irot`/`imir`. `SetOrientation` sets both.
