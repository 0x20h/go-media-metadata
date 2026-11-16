#!/bin/sh
# Writes the AVI test files: a synthetic test pattern (ffmpeg's testsrc,
# no recording), MJPEG as old cameras recorded, muxed by ffmpeg, with an
# IDIT date spliced in where a camera writes one.
# Usage: make.sh <outdir>
set -e
OUT="$1"; DIR=$(cd "$(dirname "$0")" && pwd); TMP=$(mktemp -d)
ff() { ffmpeg -v error -y -f lavfi -i testsrc=duration=2:size=64x48:rate=10 -c:v mjpeg -q:v 15 "$@"; }
ff -metadata title="Lake" -metadata comment="Morning walk" "$TMP/base.avi"
cp "$DIR/idit.go.txt" "$TMP/idit.go"
(cd "$TMP" && go run idit.go base.avi idit.avi "Tue Jun 07 08:09:10 2005")
cp "$TMP/idit.avi" "$OUT/idit.avi"
ff -metadata date="2019-12-31" "$OUT/icrd.avi"
ff "$OUT/plain.avi"
rm -rf "$TMP"
