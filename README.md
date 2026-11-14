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

x, err := mediameta.ReadExif(in, info.Size())
fmt.Println(x.DateTimeOriginal, x.OffsetKnown, x.GPS)

// Rotate losslessly: write to a temporary file, then rename it over the
// original once SetRotation has succeeded.
tmp, err := os.CreateTemp(filepath.Dir("photo.heic"), "photo-*.heic")
// ...
err = mediameta.SetRotation(in, info.Size(), tmp, 180)

// Or plan the edit, and where it keeps the file's size, patch a copy of
// the file in place: a few bytes, whatever the file's size.
plan, err := mediameta.PlanRotation(in, info.Size(), 180)
// ...
if plan.InPlace {
	err = plan.Apply(clone) // an io.WriterAt holding the source's bytes
} else {
	_, err = plan.WriteTo(tmp)
}
```

## Status

Early, and growing format by format.

| Format | Orientation | Exif | XMP |
|---|---|---|---|
| HEIF / HEIC / AVIF | read, write (`irot`, `imir`) | read, write, edit | read, write, edit |
| JPEG | read, write (Exif tag) | read, write, edit | read, write, edit, extended XMP |
| PNG, WebP | read, write (Exif tag) | read, write, edit | read, write, edit |
| Camera RAW (DNG, CR2, CR3, NEF, ARW, ORF, RW2; RAF read only) | read, write in place | read | read |
| JPEG XL | read (codestream) | read, write, edit | read, write, edit |
| QuickTime (MOV), MP4 | read, write (track matrix) | | |

`ReadPreviews` finds the JPEG previews a camera RAW embeds, with their
offsets and sizes.

`ReadEmbedded` finds the media a JPEG carries after its image (a Motion
Photo's video, an Ultra HDR gain map), and every JPEG write keeps it
intact.

`ReadVideo` reads a movie's recording date, duration, GPS position,
orientation and size, make and model, and Live Photo content identifier;
`SetRotation` and `SetOrientation` set its orientation in place;
`SetVideoMetadata` and `PlanVideoMetadata` set its recording date and place.

`ParseXMP` and `EditXMP` read and change an XMP packet's title,
description, keywords, rating, face regions (MWG) and the people shown,
whatever file holds the packet.
In a JPEG, `ReadIPTC`, `ParseIPTC`, `EditIPTC` and `SetIPTC` read and
change the IPTC-IIM title, keywords and description that mirror them.

## Documentation

- [API](docs/api.md): reading, `Set…` and `Plan…`, in-place writes,
  errors
- Formats, covering where each value lives and how it's written:
  [HEIF](docs/heif.md), [JPEG](docs/jpeg.md),
  [PNG and WebP](docs/png-webp.md), [JPEG XL](docs/jxl.md),
  [Camera RAW](docs/raw.md), [QuickTime and MP4](docs/quicktime.md)
- [Exif](docs/exif.md): the fields, writers' quirks compared with
  ExifTool, and `EditExif`
- [XMP](docs/xmp.md): `ParseXMP` and `EditXMP`
- [IPTC](docs/iptc.md): `ParseIPTC`, `EditIPTC`, and a JPEG's IPTC-IIM
- [Benchmarks](docs/benchmarks.md): against ExifTool, and how to repeat them

## Guarantees

- **Lossless:** image data is never decoded or re-encoded. Tests check
  that it is byte-identical after every write.
- **Safe on hostile input:** every parser is fuzzed. Lengths are checked,
  counts are bounded by the bytes available, and nesting is capped.
  Malformed input gives an error, never a panic or an endless loop.
- **Never the source:** a write goes to the `io.Writer` you pass, or, for
  `Plan.Apply`, to the copy you pass. On an error that writer may hold
  part of a file, so rename it over the original only on success.
- **No old values left behind:** replaced data is zeroed or not copied,
  so a removed value (a GPS position, say) doesn't stay in the file.

## Specifications

- ISO/IEC 14496-12 (ISO base media file format), ISO/IEC 23008-12 (HEIF)
- ITU-T T.81 (JPEG), PNG (Third Edition), the WebP container
- TIFF 6.0, CIPA DC-008 (Exif 2.32, plus Exif 3.0's GPS altitude
  references)
- ISO 16684-1 and the Adobe XMP Specification, Parts 1 to 3
- Apple QuickTime File Format: movie and track headers, the Keys
  metadata (`keys`, `ilst`) and user data text entries; ISO 6709 for
  locations, ISO 8601 for dates

## Licence

MIT, see [LICENSE](LICENSE). Test data: see [NOTICE](NOTICE).
