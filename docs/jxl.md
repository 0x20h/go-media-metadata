# JPEG XL

A JPEG XL file (ISO/IEC 18181) is a bare codestream (starting `FF 0A`) or
a container of ISO base media boxes (18181-2), starting with the
signature box `JXL `. Metadata lives only in a container.

## Reading

- **Exif:** the `Exif` box. Its payload starts with a 4-byte offset to
  the TIFF header, which this package reads from there.
- **XMP:** the `xml ` box, the packet as it is.
- **Compressed boxes:** a `brob` box holds another box's content,
  Brotli-compressed, after that box's type. `brob` boxes holding `Exif`
  or `xml ` are read like the plain ones, decompressed up to 16 MB.
- **Orientation:** JPEG XL decoders show the orientation in the
  codestream's image header, not the Exif tag. `ReadOrientation` (and
  `ReadExif`'s `Orientation`) read it there: after the SizeHeader, the
  ImageMetadata's orientation, 1 when its fields are all default. The
  codestream is read from the `jxlc` box, the first `jxlp` box (after
  its 4-byte index), or the bare file. Only its first 32 bytes are read.

## Writing

`PlanExif`, `PlanXMP`, `PlanExifEdit`, `PlanXMPEdit`, `PlanMetadata` and
their `Set` forms write the Exif and XMP boxes. The codestream is copied
byte for byte.

- **In place** when the new data fits a plain box: padded with zeros
  after the TIFF data, with whitespace after the XMP packet.
- **Otherwise** the box is replaced by a plain one, a `brob` box too
  (writes never compress), and further boxes of the kind are removed.
- **A file without the box** gets one before its codestream.
- **A bare codestream** gets the container around it: the signature,
  `ftyp`, the new box, and a `jxlc` box holding the codestream.
- **JPEG reconstruction data** (`jbrd`) records the original JPEG's
  segment sizes, so an edit that changes a box's size is refused
  (`ErrUnsupported`). One that keeps it is written in place.

The orientation isn't written yet: it's three bits in the codestream
header, and a codestream with default metadata has none to change.

## Brotli

`brob` boxes are decompressed with `github.com/andybalholm/brotli`
(pure Go), the package's one dependency, used only in `jxl_brob.go`.

## Tests

The tests build JPEG XL files around a codestream whose header gives
the orientation:

- plain and `brob` boxes;
- no metadata;
- a bare codestream;
- `jxlp` parts;
- JPEG reconstruction data.

Every writer, on every layout, keeps the codestream byte for byte, and
the values read back. Where exiftool is installed, ExifTool reads the
values written, and its validation finds nothing new in the files.
`FuzzReadJXL` (the box parser, reads and a write) and `FuzzUnbrob`
(Brotli streams) are in the CI fuzz matrix.
