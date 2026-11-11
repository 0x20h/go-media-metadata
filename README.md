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
