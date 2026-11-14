# Camera RAW

Covers TIFF-based RAW files (DNG, CR2, NEF, ARW, ORF, RW2, and plain
TIFF), Canon CR3 and Fujifilm RAF (read only).

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
- **RAF:** a header starting `FUJIFILMCCD-RAW ` gives the offset and
  length of a JPEG (big-endian, at bytes 84 and 88); the Exif and XMP
  are read from that JPEG. RAF isn't written.
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

Three formats keep their previews elsewhere:

- **RW2:** the JPEG in IFD0's JpgFromRaw tag (46), and the thumbnail in
  that JPEG's Exif IFD1.
- **RAF:** the JPEG the header points to, and the thumbnail in its Exif
  IFD1.
- **CR3:** the thumbnail in the metadata `uuid` box's `THMB` box, the
  preview in the `PRVW` box of its own `uuid` box (each after a 16-byte
  header giving the JPEG's length), and the full-size JPEG, which is the
  first sample of a track, found through its `stsz` and `stco`. ExifTool
  reports the first two up to the end of their box, which is a few bytes
  of padding longer.

Two makers keep previews in their maker notes, at offsets that count from
the maker note, not the file:

- **Nikon** (`Nikon\0` version 2): a TIFF header 10 bytes in, from which
  offsets count. Its IFD0's tag 17 points to a PreviewIFD, whose tags 513
  and 514 give the preview.
- **Olympus** (`OLYMPUS\0` with its byte order, the IFD at 12): offsets
  count from the maker note's start. Tag 0x0100's value is a JPEG
  thumbnail; the CameraSettings IFD (tag 0x2020, a pointer or an
  UNDEFINED value holding the IFD) gives the preview's start and length
  (tags 0x0101 and 0x0102). In the old style (`OLYMP\0`, the IFD at 8),
  tags 0x0088 and 0x0089 give the preview and 0x0100 the thumbnail, at
  offsets from the file's TIFF header.

Only the maker note's header and these IFDs are read, never the whole
maker note.

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

### Real files

`testdata/raw-samples.txt` lists real camera files from raw.pixls.us,
each released under CC0, with their SHA-256 sums: DNG from a camera and
two phones, CR2, CR3, NEF, ARW, two ORFs, RAF and RW2. They aren't committed.
`scripts/fetch-raw-samples.sh` downloads them into
`testdata/raw-samples/` (git-ignored), checks the sums and runs the
real-file tests on them. Where exiftool is installed, those tests compare
the date taken, the orientation and the GPS position with ExifTool's,
and check that every JPEG preview ExifTool extracts is one
`ReadPreviews` finds. All of them agree, the maker-note previews of the
NEF (D2H) and an ORF (E-450) included.

## Writing the orientation

`SetOrientation`, `SetRotation` and their plans change only IFD0's
Orientation value (for a CR3, CMT1's), where it sits: a tag with its
value in its entry (SHORT, or LONG as some writers have it). Where the
file's XMP packet has `tiff:Orientation`, as an attribute or an element,
that digit is changed too, so the two agree.

The plan is always in place, and nothing else is written: image data and
maker notes stay byte for byte. A file without the tag is refused
(`ErrUnsupported`) rather than restructured, because maker notes hold
offsets into the file that a rewrite would break.
