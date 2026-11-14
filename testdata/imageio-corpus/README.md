JPEG and HEIC files whose metadata Apple's ImageIO wrote, for the tests in
`imageio_corpus_test.go`. The image is synthetic: a 64×48 gradient with a
red square, no photograph, drawn with Core Graphics.

`make.swift` writes them on macOS (here macOS 26.6), reproducibly:

    swift testdata/imageio-corpus/make.swift testdata/imageio-corpus

- `exif.jpg`, `exif.heic`: the orientation (6), DateTimeOriginal with
  SubSecTimeOriginal and OffsetTimeOriginal, and a GPS position and
  altitude, as ImageIO's property dictionaries.
- `iptc.jpg`: IPTC title, caption and keywords. ImageIO also writes them
  to XMP.
- `xmp.jpg`, `xmp.heic`: XMP title, description, keywords and rating,
  merged in with `CGImageDestinationCopyImageSource`, ImageIO's lossless
  metadata update.
- `all.jpg`, `all.heic`: the Exif, GPS and IPTC properties, then the XMP
  merged in.

How ImageIO writes them, which `expected.json` follows:

- Merging XMP rewrites a JPEG's IPTC from the XMP: `all.jpg`'s IPTC title
  is the XMP's, not the one first written, and it gains DateCreated and
  TimeCreated.
- `xmp.jpg` gets an empty IPTC block with the IPTC digest of no data,
  followed by a second one holding the values. Like ExifTool, this
  package reads the first, so the file reads as having no IPTC.
- The Exif date and GPS position are copied to XMP (`exif:`,
  `photoshop:DateCreated`) in `all.*`, and a HEIC always gets an XMP
  item, `exif.heic` one with only `photoshop:DateCreated`.
- Writing IPTC into `xmp.jpg` puts it in one standard block, so
  ExifTool's warning about the misplaced second block goes away. The
  values only that second block held (the XMP's, mirrored) aren't
  carried over.

`expected.json` holds the values as this package's types read them.
