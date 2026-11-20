#!/bin/sh
# Writes the FLV test files: a synthetic test pattern (ffmpeg's testsrc,
# no recording), Sorensen H.263 muxed by ffmpeg, and one with
# creationdate and metadatadate added to its onMetaData (see
# flvdate.go.txt), as Flash tools wrote them.
# Usage: make.sh <outdir>
set -e
OUT="$1"; DIR=$(cd "$(dirname "$0")" && pwd); TMP=$(mktemp -d)
V="-f lavfi -i testsrc=size=70x50:rate=10 -t 1 -fflags +bitexact -flags +bitexact -c:v flv1"
ffmpeg -loglevel error -y $V -metadata title="Harbour at dusk" -metadata creation_time=2025-05-04T11:14:15Z -f flv "$OUT/ffmpeg.flv"
ffmpeg -loglevel error -y $V -f flv "$TMP/base.flv"
cp "$DIR/flvdate.go.txt" "$TMP/flvdate.go"
(cd "$TMP" && go run flvdate.go base.flv dated.flv "Tue Jun 07 08:09:10 2005" 2005-06-08T01:02:03Z)
cp "$TMP/dated.flv" "$OUT/dated.flv"
rm -rf "$TMP"
