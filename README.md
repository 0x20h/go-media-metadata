# go-media-metadata

`mediameta` reads and writes metadata of image and video files in pure Go,
with no cgo and no external tools. Writes are lossless: only the metadata
asked for changes, and the encoded image data is copied byte for byte.

## Status

Early. The first feature is HEIF/HEIC orientation:

- `ReadOrientation` returns an image's orientation (EXIF numbering 1–8)
  from its `irot` and `imir` properties.
- `SetRotation` rotates an image losslessly by setting the `irot` property
  of its primary item, moving item offsets where the metadata grows or
  shrinks, and keeping the EXIF Orientation tag in step where one exists.

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
- **No partial writes from this package:** a write goes to the `io.Writer`
  you pass; write to a temporary file and rename it to replace the
  original.

## Specifications

- ISO/IEC 14496-12, ISO base media file format
- ISO/IEC 23008-12, High Efficiency Image File Format (HEIF)
- CIPA DC-008 (Exif) for the Orientation tag

## Licence

MIT, see [LICENSE](LICENSE). Test data: see [NOTICE](NOTICE).
