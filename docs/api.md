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
| `ReadExif` | orientation, date taken (with fraction and offset), GPS |
| `ReadXMP` | the XMP packet's bytes, nil without one |
| `ParseXMP` | title, description, keywords and rating of a packet |

The format is recognised from the file's first bytes. HEIF is tried
first, so reading a HEIF file costs the same as it did before the other
formats existed.

## Writing

There are two ways to write an edit:

- **`Set…` functions** (`SetRotation`, `SetOrientation`, `SetExif`,
  `SetXMP`, `EditHEIFExif`, `EditFileXMP`, `SetMetadata`,
  `SetRAWOrientation`) stream the edited file to an `io.Writer`.
- **`Plan…` functions** (`PlanRotation`, `PlanOrientation`, `PlanExif`,
  `PlanXMP`, `PlanExifEdit`, `PlanXMPEdit`, `PlanMetadata`,
  `PlanRAWOrientation`) compute the same edit without writing it, as a
  `Plan`.

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

- **Parts run in order:** the Exif part as `PlanExifEdit`, then the IPTC
  part (the file's IPTC-IIM data through `EditIPTC`), then the XMP part as
  `PlanXMPEdit`. Each part is planned on the file the parts before it
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
| `ErrNotHEIF`, `ErrNotJPEG`, `ErrNotPNG`, `ErrNotWebP`, `ErrNotRAW` | the file isn't that format. A file that is no supported format at all gives an error that is both `ErrNotJPEG` and `ErrNotHEIF`. |
| `ErrMalformed` | the file breaks its own structure: a length past its container, a truncated header, too many segments, XML that doesn't parse |
| `ErrUnsupported` | a structure this package can't change safely. The file is left as it is rather than risk a wrong result. |
| `ErrNotInPlace` | `Plan.Apply` on a plan that changes the file's size |
| `ErrOrientationInExif` | an `ExifEdit` that sets a HEIF image's orientation; use `SetOrientation` (see [heif](heif.md)) |

## Hostile input

Every parser treats its input as untrusted and is fuzzed in CI. Lengths
are checked against their container, counts against the bytes that are
left, and nesting and sizes are capped. A malformed file gives an error,
never a panic or a hang. A fuzz input that takes more than 5 s fails.

## Performance

CI benchmarks each pull request against its base on the same runner,
with the runs interleaved. Any increase in allocations or bytes read per
operation fails the check, as does a clear slowdown.
