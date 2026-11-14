JPEG files whose metadata ExifTool wrote, for the tests in
`exiftool_corpus_test.go`. The image is synthetic: a 64×48 gradient with a
red square, no photograph, encoded with Go's `image/jpeg` (`base.go.txt`).

`make.sh` writes the files with ExifTool 13.44 (`make.sh <exiftool>
<base.jpg> testdata/exiftool`):

- `exif-ii.jpg`, `exif-mm.jpg`: Exif in each byte order. Orientation,
  DateTimeOriginal with SubSecTimeOriginal and OffsetTimeOriginal, and a
  GPS position and altitude (south, west and below sea level in one of
  them).
- `xmp.jpg`: XMP title with a German alternative, description, keywords,
  rating, PersonInImage and MWG face regions.
- `iptc-utf8.jpg`: IPTC-IIM in UTF-8 (1:90), with a byline and an IPTC
  digest.
- `iptc-latin1.jpg`: IPTC-IIM in Latin-1, without a character set.
- `xmp-where-when.jpg`: a position and two dates in XMP only:
  `exif:GPSLatitude` and `exif:GPSLongitude` in the `DDD,MM.mmk` form, an
  altitude below sea level, `photoshop:DateCreated` with a fraction and a
  time zone, `xmp:CreateDate` to the minute without one. Written with
  ExifTool 13.55; the other files are unchanged from 13.44.
- `all.jpg`: Exif, XMP and IPTC together.

`expected.json` holds the values written, as this package's types
read them.
