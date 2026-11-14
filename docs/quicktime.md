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

`SetRotation`, `SetOrientation` and their plans set a movie's
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

## Writing the date and place

`SetVideoMetadata` and `PlanVideoMetadata` take a `VideoEdit`: a recording date
(`Created`), a place (`GPS`), or the place removed (`RemoveGPS`).

Where each value is written:

- **Keys metadata:** `com.apple.quicktime.creationdate`, with the
  time zone's offset, and `com.apple.quicktime.location.ISO6709`, with
  six decimals and the altitude where there is one. A movie without
  Keys metadata gets a QuickTime-style `meta` box in `moov`, which
  ExifTool and FFmpeg read in MOV and MP4 files alike.
- **User data** `©day` and `©xyz`, where the movie has them, so they
  agree. Missing entries aren't added.
- **The movie header's creation time** (`mvhd`), in UTC, where its
  field can hold it.

Removing the place removes the Keys entry and `©xyz`; the Keys entries
after it are renumbered.

### In place where it can be

The plan is in place when `moov` keeps its size:

- a value of the same length;
- a `moov` that shrinks by 8 bytes or more, padded with a `free` box
  inside it;
- a `moov` that grows into a `free` or `skip` box right after it, as
  writers leave room to grow into.

Otherwise `moov` is rewritten. When it comes before the media data,
every chunk offset (`stco`, `co64`) that points past it moves with it.
An edit that would have to move other absolute offsets is refused
(`ErrUnsupported`) rather than leave them wrong: a fragmented movie's
`moof`, `sidx` or `mfra` after `moov`, sample auxiliary information
offsets (`saio`), a top-level `meta`, or a chunk offset that would
outgrow its 32-bit field. A `moov` over 64 MB is refused too.

### Checked

- Every test layout (`moov` first and last, 64-bit headers, `co64`,
  MP4 and QuickTime `meta`, Keys in `udta`, no `ftyp`, user data only,
  no metadata) with every edit: the values read back, the other fields
  stay, every chunk offset still points at its media data, and a
  second identical edit changes nothing.
- FFmpeg-encoded movies, MOV and MP4, with `moov` first and last:
  every decoded frame is the same after the edit (`framemd5`), and
  FFmpeg and ExifTool read the values written. ExifTool's validation
  finds nothing new in the files written.
- A fuzz target, `FuzzVideoEdit`.
