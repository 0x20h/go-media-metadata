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
# Cameras' Exif blocks (see ../video/camexif.go.txt; made-up values): a
# strd chunk starting "AVIF" in the stream's list, and Casio's QVMI in the
# top-level JUNK chunk.
cp "$DIR/../video/camexif.go.txt" "$TMP/camexif.go"
(cd "$TMP" && go run camexif.go strd base.avi "$OUT/avif.avi" "KONICA MINOLTA" "DiMAGE Z5" "" "2005:07:08 09:10:11" 51.5007 -0.1246)
(cd "$TMP" && go run camexif.go junk base.avi "$OUT/casio-junk.avi" CASIO EX-S600 "" "2006:09:10 11:12:13" 40.6892 -74.0445)
rm -rf "$TMP"
