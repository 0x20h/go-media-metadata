#!/bin/sh
# Writes the M2TS test files: a synthetic test pattern (no recording)
# encoded by ffmpeg's libx264, with the MDPM an AVCHD camcorder writes
# spliced in, muxed by ffmpeg as M2TS (192-byte packets).
# Usage: make.sh <outdir>
set -e
OUT="$1"; DIR=$(cd "$(dirname "$0")" && pwd); TMP=$(mktemp -d)
cp "$DIR/splice.go.txt" "$TMP/splice.go"
# 70x50 isn't a multiple of 16: the SPS crops it.
ffmpeg -loglevel error -f lavfi -i testsrc=size=70x50:rate=25 -t 2 -c:v libx264 -preset ultrafast \
  -bsf:v h264_mp4toannexb -f h264 "$TMP/in.h264"
# +02:00 (0x04); and -03:30 with daylight saving time (0x40 | 0x20 | 3<<1 | 1).
(cd "$TMP" && go run splice.go in.h264 a.h264 0x04 20250504131415 && go run splice.go in.h264 b.h264 0x67 20191231235959)
for f in a b; do
  ffmpeg -loglevel error -f h264 -framerate 25 -i "$TMP/$f.h264" -c copy -f mpegts -mpegts_m2ts_mode 1 "$TMP/$f.m2ts"
done
cp "$TMP/a.m2ts" "$OUT/mdpm.m2ts"; cp "$TMP/b.m2ts" "$OUT/mdpm-west.m2ts"
ffmpeg -loglevel error -f h264 -framerate 25 -i "$TMP/in.h264" -c copy -f mpegts "$OUT/plain.ts"
rm -rf "$TMP"
