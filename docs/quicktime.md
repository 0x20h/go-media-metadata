# QuickTime (MOV) and MP4

Apple's QuickTime File Format, and MP4 on the ISO base media file format
(ISO/IEC 14496-12).

## Reading: ReadVideo

`ReadVideo` reads a movie's:

- recording date
- duration
- GPS position
- orientation and size of the first video track
- make and model of the recording device
- content identifier that pairs a Live Photo's video with its still
  image

Where each field comes from, in order of preference:

- **Keys metadata** (`com.apple.quicktime.*`, in a QuickTime `meta` box
  or an MP4-style one): the date, place, make and model.
- **User data** (`©day`, `©xyz`, `©mak`, `©mod`) otherwise.
- **The movie header**, for the date, as a last resort. That date is in
  UTC.

Locations are ISO 6709 strings and dates ISO 8601.

The orientation comes from the track header's matrix, in the Exif
numbering images use. It covers the four rotations and the four
mirrorings; any other matrix gives 0.

`ReadVideo` reads only box headers and the few small boxes these fields
are in, wherever `moov` sits, and never the sample tables or the media
data. For a 1 MB test movie that is 870 bytes. Its results are checked
against ExifTool on the test movies.

## Writing the orientation

`SetVideoRotation`, `SetVideoOrientation` and their plans set a movie's
orientation through each video track's matrix: 36 bytes at a fixed place
in its track header (`tkhd`).

- **The translation** that keeps the turned picture in view is written
  with the matrix, as cameras write it.
- **The plan is always in place.** It patches nothing else, so the media
  data and the chunk offsets stay valid.
- **A track already shown that way** isn't touched.
- **A rotation keeps a mirroring,** as `SetRotation` does for images.
- **A movie whose header (`mvhd`) has a matrix of its own** is refused
  (`ErrUnsupported`): the two would combine.

ExifTool and FFmpeg read the rotations written.

Writing the date and place comes next.
