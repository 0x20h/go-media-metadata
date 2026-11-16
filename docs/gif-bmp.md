# GIF and BMP

Read only: nothing is written to either.

## GIF

GIF87a and GIF89a. The file is walked block by block to its trailer, in one
buffered pass: frames' image data is skipped sub-block by sub-block, never
decoded.

- **Size** (`ReadImageSize`): the logical screen descriptor's width and
  height, as ExifTool's ImageWidth and ImageHeight give them. Frames may be
  smaller and placed inside it.
- **Comment** (`ReadComment`): the comment extensions' text (label 0xFE),
  joined by a line break when there are several, "" without one. GIF has
  no other place for a description, so a caller can take it as one.
- **XMP** (`ReadXMP`): the application extension `XMP Data` with the code
  `XMP` (XMP Specification Part 3). Its packet is stored as bytes, not as
  sub-blocks, and is followed by the "magic trailer" (0x01, then 0xFF down
  to 0x00, then the terminator), which is what ends it. Only the first such
  extension is read. Other application extensions (NETSCAPE2.0's loop
  count, say), graphic control and plain text extensions are skipped.

A block of an unknown kind, a chain without its terminator, XMP without its
trailer or a file cut before its trailer is `ErrMalformed`. Comments and
XMP together are capped at 16 MiB.

## BMP

`ReadImageSize` reads the info header's width and height:
- the 12-byte `BITMAPCOREHEADER` of OS/2 1.x and Windows 2: unsigned
  16-bit values;
- the 16- and 64-byte OS/2 2.x headers and the 40- to 124-byte Windows
  ones (`BITMAPINFOHEADER` to `BITMAPV5HEADER`): signed 32-bit values.

A negative height is a top-down image: the size is its absolute value, as
ExifTool gives it. A file counts as a BMP when it starts with `BM` and its
info header has one of those sizes and fits in the file; `BM` alone is too
common a start to go by. BMP has no metadata of its own that this package
reads.

## Checks

- `TestGIFBMPAgreeWithExifTool`: ExifTool reads the size, comment and XMP
  of the files in `testdata/gif-bmp` as this package does.
- `TestGIFMalformed`, `TestBMPMalformed`: a GIF cut at every length, broken
  blocks, an XMP trailer that doesn't end; a BMP of width 0, an info header
  of no known size.
- `FuzzReadGIF` and `FuzzReadBMP`: any input ends in a size, comment or
  packet, or an error, never a panic or a hang.
