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
