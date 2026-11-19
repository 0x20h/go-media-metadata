# PNG and WebP

## PNG

Chunks (PNG specification, Third Edition) are walked up to `IEND`, at
most 65536 of them.

- **Exif** is the `eXIf` chunk. Its payload is TIFF data; a leading
  `Exif\0\0`, which some writers add, is accepted and kept.
- **Exif in a raw profile:** ImageMagick writes Exif into a `tEXt` or
  `zTXt` chunk with the keyword `Raw profile type exif` (or `... APP1`),
  as hex. Without an `eXIf` chunk, that Exif is read, as ExifTool reads
  it. An Exif edit then writes an `eXIf` chunk, starting from that Exif,
  and removes the raw profile, so no old value stays in it.
- **IPTC** is a `zTXt` (or `tEXt`) chunk with the keyword `Raw profile
  type iptc`, as ImageMagick and ExifTool write it: hex of Photoshop image
  resources whose 0x0404 holds the IIM data, or of the IIM data alone.
  `ReadIPTC` and `ReadAll` read it. `MetadataEdit.IPTC` edits it with
  `EditIPTC`, as for JPEG:
  - The chunk is replaced, and other image resources in it are kept.
  - It's added before the image data (as a `zTXt`, as ExifTool writes it)
    where there's none.
  - It's removed when no IIM data is left.
- **XMP** is an `iTXt` chunk with the keyword `XML:com.adobe.xmp` (XMP
  Part 3, 1.1.5), compressed or not. The first chunk of each kind is the
  one read.
- **PNG text** (`ReadPNGText`, and `ReadAll`'s `PNG`), as ExifTool's PNG
  group reads it:
  - The title and description come from the text chunks with the
    keywords `Title` and `Description` (`tEXt` and `zTXt` in Latin-1,
    `iTXt` in UTF-8, compressed or not), the first of each.
  - `Created` is the `Creation Time` text chunk (ExifTool's PNG
    CreationTime). PNG leaves its form free and recommends RFC 1123
    (`Mon, 04 Mar 2014 05:06:07 GMT`). That, RFC 822's layout with a
    numeric zone, ISO 8601 and the Exif layout (`2014:03:04 05:06:07`)
    are read, with the precision and offset they give. Free text
    (`March 4, 2014`) isn't a date.
  - A keyword matches as ExifTool matches it: each word's first letter
    in either case, the rest as written. `title` and `creation time`
    are Title and Creation Time; `TITLE` is another keyword.
  - `Modified` is the `tIME` chunk, the last modification in UTC
    (ExifTool's PNG ModifyDate). It isn't a capture date.
  - `ReadAll`'s `Title` and `Description` fall back to these after XMP,
    IPTC and Exif. Its `Taken` falls back to `Created`
    (`SourcePNGCreationTime`) where no Exif, XMP or IPTC capture or
    creation date is found, ahead of Exif's DateTime.

**Writing.** A payload no longer than the old one fills its chunk,
padded with zeros after TIFF data or spaces after XMP, and the chunk's
CRC is rewritten. That is in place. A longer payload replaces the chunk.
A new chunk goes right after `IHDR`, before the image data, where the
specification puts `eXIf`. Further chunks of the same kind are removed,
so no old value stays in the file.

**PNG text.** A PNG can also hold its title and description in text
chunks with the keywords `Title` and `Description` (`tEXt`, `zTXt` or
`iTXt`). ExifTool's `-Title` writes those (PNG:Title), and image viewers
read them. An XMP edit that sets a title or description also sets these
chunks where the PNG has them, so no reader finds the old text:
- A `tEXt` stays `tEXt` when the text is Latin-1, and becomes an
  uncompressed `iTXt` (UTF-8) when it isn't.
- An `iTXt` keeps its language and translated keyword.
- A `zTXt` is replaced uncompressed, so no old text stays in a compressed
  stream.
- An empty value removes the chunks.

The XMP holds the value, so no text chunk is added to a PNG without one.

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
