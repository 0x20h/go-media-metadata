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
- title, description, keywords and rating

Where each field comes from, in order of preference:

- **Keys metadata** (`com.apple.quicktime.*`, in a QuickTime `meta` box
  or an MP4-style one): the date, place, make, model, title
  (`title`), description (`description`), keywords (`keywords`) and
  rating (`rating.user`).
- **An MP4 item list** (a `meta` box of handler `mdir` in `udta`): `©nam`
  for the title, `©des` or `desc` for the description, `keyw`.
- **User data** (`©day`, `©xyz`, `©mak`, `©mod`, `©nam`, `©des`), and the
  3GPP strings `titl` and `dscp` (TS 26.244, as ExifTool writes an MP4's
  UserData Title and Description). For the title and description, the
  item list comes before the user data, whatever their order in the file,
  as ExifTool's `-Title` and `-Description` take them.
- **3GPP `loci`** in the user data, for the place, as 3GP and Android
  writers put it: longitude, latitude and altitude as 16.16 fixed point,
  after the place's name and role (3GPP TS 26.244).
- **The movie's XMP**, for the date and the place. MP4 files keep it in a
  top-level `uuid` box (`BE7ACFCB-97A9-42E8-9C71-999491E3AFAC`), and
  QuickTime movies in the user data's `XMP_`. The date is the first of
  `exif:DateTimeOriginal`, `photoshop:DateCreated` and `xmp:CreateDate`,
  as `TakenFrom` orders them. Without a time zone, `OffsetKnown` is false.
  The place comes from `exif:GPSLatitude` and `exif:GPSLongitude`. The
  packet is read only when the sources before it have nothing.
  `ReadXMP` returns it too.
- **The movie header**, for the date, as a last resort. That date is in
  UTC.

Locations are ISO 6709 strings and dates ISO 8601. Keywords are split at
commas. A rating is −1 (rejected) to 5, written as text or as an integer.

The orientation comes from the track header's matrix, in the Exif
numbering images use. It covers the four rotations and the four
mirrorings; any other matrix gives 0. `Width` and `Height` are the
track's size as stored; `Video.Shown()` gives the size as shown, swapped
for a quarter turn (orientations 5 to 8), as `ImageSize.Shown` does.

`ReadVideo` reads only box headers and the few small boxes these fields
are in, wherever `moov` sits, and never the sample tables or the media
data. For a 1 MB test movie that is 870 bytes.

`TestVideoCorpusAgreesWithExifTool` checks every field against ExifTool
on `testdata/video`. The files are synthetic: an FFmpeg test pattern,
CC0, made by its `make.sh`. Each holds the metadata in one place:

- Keys (date with offset, GPS, make, model, content identifier);
- the user data (`©day`, `©xyz`, `©mak`, `©mod`);
- 3GPP `loci` in a 3GP;
- XMP only, in an MP4's `uuid` box and in a MOV's `XMP_`;
- tracks turned 90° and 270°.

### Movies cut short or with junk at the end

Some movies have a broken layout that ExifTool reads past:
- a file cut short, so its `mdat` claims more bytes than are there
- junk after the last box: a partial header, stray bytes, or a header
  whose size is impossible

`ReadVideo` (and `ReadAll`, `ReadImageSize`, `ReadOrientation`) read
these too. They keep the boxes before the break, and a last box that runs
past the end is cut to the file. This applies only when the file starts
like a movie (`ftyp`, `moov`, `mdat`, `wide`, `free` or `skip`) and holds
a whole `moov`. A movie whose `moov` itself is cut is still `ErrMalformed`.

A well-formed movie takes the strict path, at the same cost as before.
Writes always read the boxes strictly, so they refuse these files
(`ErrMalformed`) rather than write past what's there.

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

## Writing the date, place, title, description, keywords and rating

`SetVideoMetadata` and `PlanVideoMetadata` take a `VideoEdit`: a recording date
(`Created`), a place (`GPS`), or the place removed (`RemoveGPS`); a
title, description, keywords and rating, or any of them removed (an
empty title, description or keyword list, or `RemoveRating`).

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

The title, description, keywords and rating:

- **Keys:** `com.apple.quicktime.title`, `.description`, `.keywords` and
  `.rating.user`, each as UTF-8 text, as ExifTool writes them.
  Keywords are one comma-separated value, because ExifTool reads only one
  value of an entry, so a keyword can't contain a comma. A rating is
  written as text, −1 to 5.
- **An MP4 item list** (`udta/meta/ilst`, as ffmpeg and ExifTool write
  it): `©nam` for the title and `©des` for the description, as ExifTool
  writes ItemList Title and Description. They're added to an item list
  the movie has, so players that read only the item list show the new
  values. `desc` and `keyw` are updated where they are. A movie without an
  item list doesn't get one; Apple's Keys hold the values there.
- **User data:** QuickTime text `©nam` and `©des`, and the 3GPP strings
  `titl` and `dscp` (as ExifTool writes an MP4's UserData Title and
  Description; their language is kept). These are updated where the movie
  has them.

Every place is set to the same value, or removed with it, so no reader
finds an old title or description.

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
- FFmpeg-made movies with a title (and in the MP4, a description), MOV
  and MP4, `moov` first and last: setting and removing the title,
  description, keywords and rating (`TestVideoTextEncoded`). ExifTool
  must read the new values from Keys and from the entries the movie had,
  and none after removal. The decoded frames must be the same, and the
  validation must find nothing new.
- A fuzz target, `FuzzVideoEdit`, with the text edits among its edits.

## Writing the movie's XMP: people and face regions

`VideoEdit.XMP` (an `XMPEdit`) edits the movie's XMP packet with
`EditXMP`. Use it for what Keys and user data don't hold: people
(`PersonInImage`), face regions, and the XMP mirror of the other fields.
`PlanMetadata`'s XMP part does the same for a movie.

The packet is edited where it is:
- **A top-level `uuid` box** (`BE7ACFCB-…`, as MP4 files have it): in
  place, padded with spaces, when the new packet fits.
- **When it doesn't fit,** the box becomes a `free` box of the same size,
  zeroed, and the packet moves to the user data's `XMP_`. That's inside
  `moov`, which grows as for the other fields (into free space after it,
  or with the chunk offsets moved). Nothing else in the file moves.
- **The user data's `XMP_`** (QuickTime): edited there. A movie with both
  gets both edited, so neither keeps old values.
- **A movie without a packet** gets one in `XMP_`.

The media data is copied byte for byte. A movie has no Exif or IPTC to
edit: `PlanMetadata`'s Exif part is refused with `ErrUnsupported`, and the
date and place are `VideoEdit`'s.

## AVI and MPEG program streams

Read only, through `ReadVideo` (and `ReadAll`, `ReadImageSize`,
`ReadOrientation`, which give orientation 1). Writes give an error that is
`ErrUnsupported`.

- **AVI** (RIFF, as older cameras recorded it, often MJPEG):
  - The duration and size come from the main header (`avih`): frames ×
    microseconds per frame.
  - The recording date is the `IDIT` chunk (asctime or Exif layout), else
    the INFO list's `ICRD` when it holds a whole date. A year alone isn't
    taken: it would read as 1 January.
  - The title is `INAM`, the description `ICMT` (else `ISBJ`), and the
    keywords `IKEY`, split at `;` or `,`.
- **MPEG program streams** (`.mpg`, `.vob`):
  - The size comes from the first video sequence header.
  - The duration runs from the first video PES packet's PTS to the last's,
    plus one frame at the sequence header's rate. Without PTS it's the span
    of the packs' system clock references.
  - ExifTool gives the size, and no duration for a variable bitrate. The
    tests check the duration against ffprobe.

Both are checked against ExifTool on `testdata/avi` and `testdata/mpegps`,
synthetic clips made by those folders' `make.sh`.
