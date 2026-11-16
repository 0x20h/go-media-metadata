# Matroska and WebM

Matroska (`.mkv`) and WebM (`.webm`, a subset of Matroska) are EBML
documents (RFC 8794, RFC 9559): elements of an ID, a size and a body,
nested. Read only.

## Reading: ReadVideo

`ReadVideo` takes a Matroska or WebM file as it takes a movie. The EBML
header's DocType must be `matroska` or `webm` (Matroska without one). It
reads:

- **Duration**: Info's `Duration` × `TimestampScale` (1 ms by default).
  A file written live (a browser's recording, a stream) may have none:
  `Duration` is 0 then.
- **Created**: Info's `DateUTC`, nanoseconds since 2001-01-01 UTC, in UTC
  with `OffsetKnown` true. ffmpeg writes it from `creation_time`.
- **Title**: Info's `Title`, else the `TITLE` of a tag about the whole
  file (no track, edition, chapter or attachment UID, target type 50).
- **Width and Height**: the first video track's `PixelWidth` and
  `PixelHeight`. Its `DisplayWidth` and `DisplayHeight` are not read:
  they are often an aspect ratio (16 × 9) rather than a size.
- **Orientation**: from the video track's `Projection`. A rectangular
  projection without yaw or pitch, rolled by a multiple of 90°, gives
  orientations 1, 6, 3 or 8. `ProjectionPoseRoll` turns counter-clockwise:
  ffmpeg writes a roll of 90 where an MP4 gets a 270° matrix
  (orientation 8). Any other projection gives 0; no projection, 1.

`ReadImageSize` and `ReadOrientation` take the same files.

## What is read

Only the Segment's SeekHead, Info, Tracks and Tags, each read whole up to
a limit (64 KB, 1 MB for Tracks and Tags). The Segment's children are
walked from its start to the first Cluster; Info and Tracks come before it
in every muxer's output. Tags often come after the Clusters, at the end of
the file: they, and anything else the walk didn't reach, are found
through the SeekHead (and a second SeekHead it points to). A seek entry
that doesn't lead to its element is skipped. Clusters, the media data,
are never read.

A Segment of unknown size (a file written to a pipe) runs to the end of
the file. A Segment without Info is `ErrMalformed`.

Writes (`PlanOrientation`, `PlanRotation`, `PlanVideoMetadata`, …) return
an error that is `ErrUnsupported`, and so does `HashImageData`, for which
ExifTool gives no value either.

## Checks

`TestMatroskaAgreesWithExifTool` compares the duration, date, title and
size with ExifTool's. It runs on `testdata/matroska`: synthetic test
patterns made by `make.sh` with ffmpeg, an H.264 `.mkv` with a title and a
recording time, a VP9 `.webm` with a title, and a VP8 `.webm` written to a
pipe. Built elements test the SeekHead, tags, projections and malformed
input, and `FuzzReadMatroska` starts from all of them.
