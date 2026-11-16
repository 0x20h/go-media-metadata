#!/bin/sh
# Writes the MPEG program stream test files: a synthetic test pattern
# (ffmpeg's testsrc, no recording), MPEG-2 and MPEG-1 video, muxed by ffmpeg.
# Usage: make.sh <outdir>
set -e
OUT="$1"
ffmpeg -v error -y -f lavfi -i testsrc=duration=3:size=64x48:rate=25 -c:v mpeg2video -q:v 20 -f vob "$OUT/mpeg2.mpg"
ffmpeg -v error -y -f lavfi -i testsrc=duration=3:size=80x48:rate=25 -c:v mpeg1video -q:v 20 -f mpeg "$OUT/mpeg1.mpg"
