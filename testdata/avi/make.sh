#!/bin/sh
# Writes the AVI test files: a synthetic test pattern (ffmpeg's testsrc,
# no recording), MJPEG as old cameras recorded, muxed by ffmpeg, with an
# IDIT date spliced in where a camera writes one, and a Nikon ncdt list.
# Usage: make.sh <outdir>
set -e
OUT="$1"; DIR=$(cd "$(dirname "$0")" && pwd); TMP=$(mktemp -d)
ff() { ffmpeg -v error -y -f lavfi -i testsrc=duration=2:size=64x48:rate=10 -c:v mjpeg -q:v 15 "$@"; }
ff -metadata title="Lake" -metadata comment="Morning walk" "$TMP/base.avi"
cp "$DIR/idit.go.txt" "$TMP/idit.go"
(cd "$TMP" && go run idit.go base.avi idit.avi "Tue Jun 07 08:09:10 2005")
cp "$TMP/idit.avi" "$OUT/idit.avi"
ff -metadata date="2019-12-31" "$OUT/icrd.avi"
# Nikon's ncdt list, as Nikon compact cameras write it (see ncdt.go.txt),
# with a thumbnail of the pattern.
ffmpeg -v error -y -f lavfi -i testsrc=size=16x12 -frames:v 1 -q:v 31 "$TMP/thumb.jpg"
cp "$DIR/ncdt.go.txt" "$TMP/ncdt.go"
(cd "$TMP" && go run ncdt.go base.avi thumb.jpg nikon.avi)
cp "$TMP/nikon.avi" "$OUT/nikon.avi"
ff "$OUT/plain.avi"
rm -rf "$TMP"
