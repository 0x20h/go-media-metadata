# Camera RAW

Covers TIFF-based RAW files (DNG, CR2, NEF, ARW, ORF, RW2, and plain
TIFF) and Canon CR3.

## Reading

`ReadExif`, `ReadOrientation` and `ReadXMP` read IFDs from the file as
needed, one IFD and its short values at a time. They never read image
data, strips or maker notes. A value past 64 KB is left out, and an XMP
packet is capped at 4 MiB.

- **TIFF headers:** TIFF's own, and the variants Olympus ORF (`IIRO`,
  `IIRS`, `MMOR`) and Panasonic RW2 (`IIU\0`) use.
- **RW2:** IFD0 points to no Exif IFD; the Exif is read from the JPEG
  the file embeds.
- **XMP:** IFD0's XMLPacket tag (700).
- **CR3:** a `uuid` box in `moov` holds the TIFF metadata: CMT1 (IFD0),
  CMT2 (the Exif IFD) and CMT4 (the GPS IFD), each a TIFF structure of
  its own. A top-level `uuid` box holds the XMP. The file's `ftyp` brand
  is `crx `.

## Previews

`ReadPreviews` finds the JPEG previews a TIFF-based RAW embeds, largest
first, with each one's offset, length, width and height. It looks in
IFD0, the IFDs chained after it, and the SubIFDs of each, for:

- **a JPEGInterchangeFormat and its length** (tags 513 and 514): CR2's
  thumbnail, NEF's JpgFromRaw, ARW's preview and thumbnail;
- **a single strip compressed as JPEG** (Compression 6, 7 or 34892):
  DNG's previews in SubIFDs, CR2's full-size JPEG in IFD0.

A candidate counts only when it's a baseline or progressive JPEG inside
the file. Its frame header gives the size, read from the candidate's
first 4 KB, or up to 128 KB when big marker segments come first. That
also leaves out raw image data stored as lossless JPEG (DNG, CR2)
without rules per make. Only IFDs and candidate heads are read: 4 KB of
a 90 KB preview in the tests.

Tests build a DNG (an uncompressed thumbnail, lossless raw data and a
JPEG preview in SubIFDs), a CR2, a NEF and an ARW in their makers'
layouts. Where exiftool is installed, every JPEG preview it extracts
(`PreviewImage`, `JpgFromRaw`, `ThumbnailImage`, `OtherImage`) is one
`ReadPreviews` finds, byte for byte. `FuzzReadPreviews` checks that
what's found lies in the file.

## Writing the orientation

`SetRAWOrientation` and `PlanRAWOrientation` change only IFD0's
Orientation value (for a CR3, CMT1's), where it sits: a tag with its
value in its entry (SHORT, or LONG as some writers have it). Where the
file's XMP packet has `tiff:Orientation`, as an attribute or an element,
that digit is changed too, so the two agree.

The plan is always in place, and nothing else is written: image data and
maker notes stay byte for byte. A file without the tag is refused
(`ErrUnsupported`) rather than restructured, because maker notes hold
offsets into the file that a rewrite would break.
