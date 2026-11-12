# go-media-metadata

go-media-metadata reads and writes metadata of image and video files in
pure Go, with no cgo and no external tools. Writes are lossless: only the
metadata asked for changes, and the encoded image data is copied byte for
byte.

The module is `github.com/0x20h/go-media-metadata`; its package is named
`mediameta`.

## Install

```sh
go get github.com/0x20h/go-media-metadata
```

## Use

```go
import mediameta "github.com/0x20h/go-media-metadata"

in, err := os.Open("photo.heic")
// ...
info, _ := in.Stat()

o, err := mediameta.ReadOrientation(in, info.Size())
fmt.Println(o) // e.g. "rotated 90° clockwise"

// Rotate losslessly: write to a temporary file, then rename it over the
// original once SetRotation has succeeded.
tmp, err := os.CreateTemp(filepath.Dir("photo.heic"), "photo-*.heic")
// ...
err = mediameta.SetRotation(in, info.Size(), tmp, 180)

// Or plan the edit, and where it keeps the file's size, patch a copy of
// the file in place (e.g. a clone of it): a few bytes, whatever its size.
plan, err := mediameta.PlanRotation(in, info.Size(), 180)
// ...
if plan.InPlace {
	err = plan.Apply(clone) // an io.WriterAt holding the source's bytes
} else {
	_, err = plan.WriteTo(tmp)
}
```

## Status

Early. The first feature is HEIF/HEIC orientation:

- `ReadOrientation` returns an image's orientation (Exif numbering 1–8)
  from its `irot` and `imir` properties.
- `SetRotation` rotates an image losslessly by setting the `irot` property
  of its primary item, moving item offsets where the metadata grows or
  shrinks, and keeping the Exif Orientation tag in step where one exists.
- `SetOrientation` sets any of the eight orientations, mirrored ones
  included, through `irot` and `imir` in the order ISO/IEC 23008-12
  applies them (rotation, then mirror). On a mirrored image `SetRotation`
  keeps the mirror first, as the Exif numbering does: orientation 2 turned
  90° clockwise is 7.
- `PlanRotation` and `PlanOrientation` compute the same edits without
  writing them: a `Plan` that
  streams the new file (`WriteTo`) or, when the file keeps its size, lists
  the bytes that change as patches to apply to a copy (`Apply`). Keeping
  the mirroring (or its absence) is in place where the image has an `irot`
  of its own; adding or removing a mirror changes the file's size.

### Compared with other tools

- **Minimal writes.** Where an image already has an `irot` property of its
  own, `SetRotation` changes its one angle byte and nothing else moves. A
  new `irot` is appended to the property container and associated as an
  essential property after the descriptive ones (and before any `imir`),
  the position Apple's ImageIO uses; item offsets are moved only when the
  `meta` box grows ahead of the image data. No brands, items or properties
  are added or reordered otherwise.
- **The Exif Orientation tag is kept in step, deliberately.** Apple's
  ImageIO leaves a HEIC's Exif Orientation as it was when it rotates the
  image (so a file can say 6 in Exif and 180° in `irot`), which makes
  tools that read Exif rather than `irot` show it wrongly. `SetRotation`
  sets an existing tag to the orientation the file now shows; a file
  without the tag doesn't get one.

## Guarantees

- **Lossless:** image data is never decoded or re-encoded; tests check it
  is byte-identical after every write.
- **Safe on hostile input:** every parser is fuzzed. Lengths are checked,
  counts are bounded by the bytes available, nesting is capped. Malformed
  input gives an error, never a panic or an endless loop.
- **Never the source:** a write reads the source and writes a new file to
  the `io.Writer` you pass. On an error that writer may hold part of a
  file, so write to a temporary file and rename it over the original only
  once the call has succeeded. `Plan.Apply` writes only to the copy you
  pass, and only when the plan is in place: the patches then hold every
  byte that changes, and applied to a copy they give byte for byte what
  `WriteTo` writes.

## Specifications

- ISO/IEC 14496-12, ISO base media file format
- ISO/IEC 23008-12, High Efficiency Image File Format (HEIF)
- CIPA DC-008 (Exif) for the Orientation tag

## Licence

MIT, see [LICENSE](LICENSE). Test data: see [NOTICE](NOTICE).
