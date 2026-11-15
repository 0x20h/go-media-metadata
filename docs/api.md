# API: reading, writing, plans

How the package's functions behave in every format. The format pages
([heif](heif.md), [jpeg](jpeg.md), [png-webp](png-webp.md),
[raw](raw.md)) say where each value is read from and written to;
[exif](exif.md) and [xmp](xmp.md) cover the metadata itself.

## Reading

Every function takes an `io.ReaderAt` and the file's size, and reads only
what it needs: marker segments, boxes, chunks or IFDs up to the metadata,
never the image data.

| Function | Returns |
|---|---|
| `ReadOrientation` | how the image is shown, 1 to 8 |
| `ReadImageSize` | the size as stored, and with `Shown()` as shown |
| `ReadExif` | orientation, date taken (with fraction and offset), GPS |
| `ReadXMP` | the XMP packet's bytes, nil without one |
| `ParseXMP` | title, description, keywords and rating of a packet |

The format is recognised from the file's first bytes. HEIF is tried
first, so reading a HEIF file costs the same as it did before the other
formats existed.

## Image size: ReadImageSize

`ReadImageSize` returns the image's width and height as stored, and the
orientation `ReadOrientation` gives. `Shown()` is the size as the image is
displayed: swapped for orientations 5 to 8, which turn it a quarter. Each
format's size comes from the image itself, not from metadata about it:

| Format | Size from |
|---|---|
| JPEG | the frame header (SOF), not Exif's PixelXDimension |
| PNG | IHDR |
| WebP | VP8X's canvas, else the VP8 or VP8L frame |
| HEIF | the primary item's `ispe` (a grid's output size), before `clap` |
| JPEG XL | the codestream's SizeHeader |
| Camera RAW | the full-size image; see [raw](raw.md) |
| QuickTime, MP4 | the first video track's size, its matrix as the orientation |

The tests check every corpus file and the RAW samples against ExifTool's
composite `ImageSize`. An orientation write leaves the stored size as it
was, which the dispatch fuzz target checks.

## Image data hash: HashImageData

`HashImageData(r, size, w)` writes the file's image data, without its
metadata, to `w`. With `w` a hash, the sum is what ExifTool gives as
ImageDataHash with the same `-api ImageHashType` (MD5, SHA256, SHA512):

```go
h := sha256.New()
_, err := mediameta.HashImageData(f, size, h)
// ExifTool: -ImageDataHash -api ImageHashType=SHA256
```

A metadata write leaves the sum as it was, so comparing it before and
after a write shows the image is unchanged. Two files with the same sum
hold the same image whatever their metadata.

| Format | Image data |
|---|---|
| JPEG | the scans, from the first SOS marker to EOI, leaving out the segments between scans. A scan's data after its last RST marker or stuffed 0xFF is left out when a segment, not the next scan, follows it. ExifTool does the same. |
| PNG | the IDAT chunks' data (and a JNG's JDAT, JDAA) |
| WebP | the VP8, VP8L, ALPH, ANIM and ANMF chunks' data, with padding |
| HEIF | every coded image item (av01, avc1, hvc1, lhv1, hvt1), tiles, thumbnails and auxiliary images included, by item id |
| JPEG XL | the jxlc and jxlp payloads (jxlp's with its part index); a bare codestream is the whole file |
| TIFF-based RAW | the strips and tiles of each IFD and SubIFD, a SubIFD's before its parent's. Previews are left out: a CR2's IFD0, a DNG's reduced JPEG, the thumbnail in IFD1, an ARW's preview in IFD0. A Panasonic RW2's raw data, to the end of the file. |
| RAF | the raw data blocks the header points to |
| QuickTime, MP4, CR3 | each video and sound track's chunks, in chunk-offset order |

ExifTool gives no ImageDataHash for an Olympus ORF, whose header isn't
TIFF's. This package hashes the ORF's strips. The tests compare the sum
with ExifTool's on every corpus file, the RAW samples, every JPEG XL
layout, and progressive and restart-marker JPEGs, WebP files and movies
made with jpegtran, cwebp and ffmpeg. The fuzz targets check that every
metadata, orientation and rotation write leaves it as it was.

## Writing

One naming scheme covers every write:

- **`Plan…` functions** (`PlanRotation`, `PlanOrientation`, `PlanExif`,
  `PlanXMP`, `PlanIPTC`, `PlanMetadata`, `PlanVideoMetadata`) compute an
  edit of a file without writing it, as a `Plan`.
- **`Set…` functions** with the same names (`SetRotation`, …,
  `SetVideoMetadata`) stream that edit to an `io.Writer`: the same bytes
  as the plan's `WriteTo`.
- **`Edit…` functions** (`EditExif`, `EditXMP`, `EditIPTC`) change
  metadata held in memory (TIFF data, an XMP packet, IPTC-IIM data), not
  a file.

Edits of a file's Exif, XMP and IPTC fields go through `PlanMetadata` and
`SetMetadata`, with only the parts needed. The orientation and rotation
functions take every format the package writes (HEIF, JPEG, PNG, WebP,
camera RAW, QuickTime and MP4) and look at the file to tell which.

Neither ever writes to the source. On an error the writer may hold part
of a file, so write to a temporary file and rename it over the original
only once the call has succeeded.

### Plan

```go
type Plan struct {
	InPlace bool    // the edit keeps the file's size
	Patches []Patch // every byte that changes, when InPlace
	Size, NewSize int64
}
func (p *Plan) WriteTo(dst io.Writer) (int64, error)
func (p *Plan) Apply(w io.WriterAt) error
```

- `WriteTo` streams the edited file: `NewSize` bytes, the same output as
  the matching `Set…` function. The source must not change between
  planning and writing.
- When `InPlace` is true, `Patches` hold every byte that changes. They
  are sorted, don't overlap, and lie within the file. `Apply` writes them
  to a copy of the source (a clone, say), and the result is byte for byte
  what `WriteTo` writes. A clone edited in place keeps its unchanged
  blocks shared, and the write takes time in proportion to the patches,
  not the file.
- When `InPlace` is false, `Apply` returns `ErrNotInPlace` and writes
  nothing.

An edit that changes nothing gives an in-place plan with no patches.

### Several standards at once: PlanMetadata

```go
type MetadataEdit struct {
	Exif *ExifEdit
	XMP  *XMPEdit
	IPTC *IPTCEdit // JPEG only
}
func PlanMetadata(src io.ReaderAt, size int64, edit MetadataEdit) (*Plan, error)
func SetMetadata(src io.ReaderAt, size int64, dst io.Writer, edit MetadataEdit) error
```

`PlanMetadata` plans one edit of a file in every standard it names, so a
caller writes the file once, all or nothing:

- **Parts run in order:** the Exif part (the file's Exif through
  `EditExif`), then the IPTC part (its IPTC-IIM data through `EditIPTC`),
  then the XMP part (its packet through `EditXMP`). Each part is planned on the file the parts before it
  make, and the result is byte for byte what writing them one after
  another gives.
- **Parts are optional:** a nil part, or one with every field left at its
  zero value, leaves its standard as it is. An edit with no part changes
  nothing: an in-place plan without patches.
- **In place:** the plan is in place when every part is. Its patches then
  hold every byte any part changes, as the last part leaves it.
- **Memory:** a part that changes the file's size is held in memory, the
  file as it makes it, for the parts after it and for `WriteTo`. A part
  in place is read through its patches, with no copy.
- **IPTC outside JPEG:** refused with `ErrUnsupported`.

### Old values don't stay

A write never leaves an old value in the file:

- A new value that fits where the old one was is written over it, and
  the rest of that space is zeroed (Exif, PNG/WebP chunks, HEIF items)
  or filled with whitespace (XMP padding).
- A segment or chunk that is replaced is not copied to the output.
- HEIF item data that moves to the end of the file has its old bytes
  overwritten with zeros.
- Inside Exif, a removed GPS position and IFDs that move are zeroed (see
  [exif](exif.md)).

## Errors

| Error | Meaning |
|---|---|
| `ErrUnknownFormat` | the file is of no format the function takes: one this package doesn't read, or, for a function that takes one format (`ReadVideo`, `ReadPreviews`, `ReadEmbedded`), another |
| `ErrMalformed` | the file breaks its own structure: a length past its container, a truncated header, too many segments, XML that doesn't parse |
| `ErrUnsupported` | a structure this package can't change safely. The file is left as it is rather than risk a wrong result. |
| `ErrNotInPlace` | `Plan.Apply` on a plan that changes the file's size |
| `ErrOrientationInExif` | a `MetadataEdit` whose Exif part sets a HEIF image's orientation; use `SetOrientation` (see [heif](heif.md)) |

Every error wraps one of these, with context, and its text starts
"mediameta: ". Test for them with `errors.Is`.

## Hostile input

Every parser treats its input as untrusted and is fuzzed in CI. Lengths
are checked against their container, counts against the bytes that are
left, and nesting and sizes are capped. A malformed file gives an error,
never a panic or a hang. A fuzz input that takes more than 5 s fails.

## Performance

CI benchmarks each pull request against its base on the same runner,
with the runs interleaved. Any increase in allocations or bytes read per
operation fails the check, as does a clear slowdown.
