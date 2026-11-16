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
- `camera-ii.jpg`, `camera-mm.jpg`: make, model, lens, ISO, f-number,
  exposure time and focal length in each byte order. `camera-mm.jpg`'s
  ISO is 65535 with the real value in RecommendedExposureIndex. Written
  with ExifTool 13.55.
- `date-digitized.jpg`, `date-iptc.jpg`, `date-xmp.jpg`: a date taken
  in one place each: Exif DateTimeDigitized with its fraction and
  offset, IPTC Date and Time Created with an offset, XMP
  `exif:DateTimeOriginal`. Written with ExifTool 13.55.
- `date-modify.jpg`: only IFD0's DateTime (ModifyDate) with SubSecTime
  and OffsetTime, as a messenger leaves a photo. Written with ExifTool
  13.55.
- `regions-full.jpg`: MWG regions with every field. A rotated face with
  a description and Extensions, a focus circle in pixels, and a bar
  code. Written with ExifTool 13.55.
- `xp-tags.jpg`: Windows' XP tags in IFD0 (title, comment, author,
  keywords, subject), UTF-16LE, non-ASCII text among them. Written with
  ExifTool 13.55.
- `all.jpg`: Exif, XMP and IPTC together.
- `apple-makernote.jpg`: a synthetic Apple maker note (as an iPhone writes
  it: "Apple iOS", version 1, a big-endian IFD whose offsets count from the
  note's start) with a ContentIdentifier, and a date with its offset.
  Written with ExifTool 13.55 (`-MakerNoteApple<=`, from bytes `make.sh`
  prints).

`expected.json` holds the values written, as this package's types
read them.
