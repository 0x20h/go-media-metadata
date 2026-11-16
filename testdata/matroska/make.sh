#!/bin/sh
# Writes the Matroska and WebM test files: a synthetic test pattern (no
# recording), one second at 10 frames a second.
# Usage: make.sh <outdir>
set -e
OUT="$1"
SRC="-f lavfi -i testsrc=size=70x50:rate=10 -t 1 -fflags +bitexact -flags +bitexact"
# H.264 in Matroska with a title, a recording time, and a 16:9 display size.
ffmpeg -loglevel error -y $SRC -c:v libx264 -preset ultrafast -aspect 16:9 \
  -metadata title="Harbour at dusk" -metadata creation_time=2025-05-04T11:14:15.250Z \
  -f matroska "$OUT/title-date.mkv"
# VP9 in WebM with a title.
ffmpeg -loglevel error -y $SRC -c:v libvpx-vp9 -deadline realtime -metadata title="Boats" \
  -f webm "$OUT/vp9.webm"
# VP8 in WebM written to a pipe, as a live recording is: the segment's
# size unknown, no duration, no seek head, no tags.
ffmpeg -loglevel error -y $SRC -c:v libvpx -deadline realtime -f webm - > "$OUT/streamed.webm"
