#!/bin/sh
# Writes the ASF test files: a synthetic test pattern and tone (ffmpeg's
# testsrc and sine, no recording), WMV2 video and WMA2 audio muxed by
# ffmpeg, and one dated only by WM/EncodingTime (see encodingtime.go.txt).
# Usage: make.sh <outdir>
set -e
OUT="$1"; DIR=$(cd "$(dirname "$0")" && pwd); TMP=$(mktemp -d)
V="-f lavfi -i testsrc=size=70x50:rate=10 -t 1 -fflags +bitexact -flags +bitexact -c:v wmv2"
ffmpeg -loglevel error -y $V -metadata title="Harbour at dusk" -metadata comment="Morning, by the water" \
  -metadata creation_time=2025-05-04T11:14:15Z -f asf "$OUT/video.wmv"
ffmpeg -loglevel error -y -f lavfi -i sine=frequency=440:duration=1 -fflags +bitexact -c:a wmav2 \
  -metadata title="Tone" -metadata creation_time=2025-05-04T11:14:15Z -f asf "$OUT/audio.wma"
ffmpeg -loglevel error -y $V -metadata title="Encoded" -f asf "$TMP/base.wmv"
cp "$DIR/encodingtime.go.txt" "$TMP/encodingtime.go"
(cd "$TMP" && go run encodingtime.go base.wmv encodingtime.wmv 2006-07-08T09:10:11Z)
cp "$TMP/encodingtime.wmv" "$OUT/encodingtime.wmv"
rm -rf "$TMP"
