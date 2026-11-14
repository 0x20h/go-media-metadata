# PNG and WebP

## PNG

Chunks (PNG specification, Third Edition) are walked up to `IEND`, at
most 65536 of them.

- **Exif** is the `eXIf` chunk. Its payload is TIFF data; a leading
  `Exif\0\0`, which some writers add, is accepted and kept.
- **XMP** is an `iTXt` chunk with the keyword `XML:com.adobe.xmp` (XMP
  Part 3, 1.1.5), compressed or not. The first chunk of each kind is the
  one read.

**Writing.** A payload no longer than the old one fills its chunk,
padded with zeros after TIFF data or spaces after XMP, and the chunk's
CRC is rewritten. That is in place. A longer payload replaces the chunk.
A new chunk goes right after `IHDR`, before the image data, where the
specification puts `eXIf`. Further chunks of the same kind are removed,
so no old value stays in the file.

## WebP

RIFF chunks (WebP container specification) are walked, at most 65536 of
them. Data past the RIFF size isn't the WebP's and is copied as it is.

- **Exif** is the `EXIF` chunk, and **XMP** the `XMP ` chunk.

**Writing.** A payload that fits is written in place, as for PNG. A
longer one replaces the chunk. A new chunk goes after the last chunk,
after the image, which is the container's order. Bytes after the last
chunk that are too few for a chunk stay after the new one, at the end,
where readers pass over them; a last chunk missing its pad byte would run
into a new chunk, and is refused (`ErrMalformed`). A simple WebP
(`VP8` or `VP8L` alone) gets the `VP8X` header that metadata needs, with
the canvas size from the bitstream's header and the alpha flag from a
`VP8L` image. The `VP8X` flag for the chunk is set, and the RIFF size
follows. Further chunks of the same kind are removed.

## Orientation

In both formats the Exif Orientation tag is how the image is shown, so
`ReadOrientation` reads it, and `PlanOrientation` sets it (as does `PlanMetadata`'s Exif part) along with the
other fields.

## XMP content

The functions above take and return the packet's bytes. `ParseXMP` and
`EditXMP` read and change the fields inside a packet (see [xmp](xmp.md)).
