#!/bin/sh
# Writes the video test corpus: a synthetic test pattern (ffmpeg's
# testsrc, no recording), 64x48, 1 second, encoded by ffmpeg, with
# metadata in each place a movie keeps it, written by ExifTool or ffmpeg.
# Usage: make.sh <outdir>
set -e
OUT="$1"; TMP=$(mktemp -d)
ff() { ffmpeg -v error -y "$@"; }
et() { f="$1"; shift; exiftool -q -overwrite_original "$@" "$f"; }
ff -f lavfi -i testsrc=duration=1:size=64x48:rate=5 -c:v mpeg4 -q:v 10 "$TMP/base.mov"
ff -f lavfi -i testsrc=duration=1:size=64x48:rate=5 -f lavfi -i sine=duration=1:frequency=440 \
  -c:v mpeg4 -q:v 10 -c:a aac -b:a 16k -shortest "$TMP/base.mp4"

# Keys (as iPhones write them): a date with its offset, ISO 6709 GPS,
# make, model, a Live Photo content identifier.
cp "$TMP/base.mov" "$OUT/keys.mov"
et "$OUT/keys.mov" "-Keys:CreationDate=2025:05:04 13:14:15+02:00" "-Keys:GPSCoordinates#=51.5 -0.125 35.5" \
  -Keys:Make=Apple "-Keys:Model=iPhone 12 Pro" -Keys:ContentIdentifier=5A7E21BB-FFC6-4275-957A-A864EDD15D84

# The movie header's date (2024-06-01 10:00 UTC, set by ffmpeg) and a
# Keys date that differs from it.
ff -i "$TMP/base.mov" -c copy -metadata creation_time=2024-06-01T10:00:00Z "$OUT/keys-mvhd.mov"
et "$OUT/keys-mvhd.mov" "-Keys:CreationDate=2025:05:04 13:14:15+02:00"

# QuickTime user data: ©day, ©xyz, ©mak, ©mod.
cp "$TMP/base.mov" "$OUT/udta.mov"
et "$OUT/udta.mov" "-UserData:ContentCreateDate=2019:12:31 23:59:59-05:00" \
  "-UserData:GPSCoordinates#=-33.865 151.2094 12" -UserData:Make=Canon "-UserData:Model=Canon EOS R6"

# 3GPP loci, as 3GP and Android writers put the place.
ff -i "$TMP/base.mp4" -c copy -metadata "location=+48.1370+011.5750+520.000/" -f 3gp "$OUT/loci.3gp"

# XMP only: an MP4's uuid box, a QuickTime movie's XMP_ in udta.
cp "$TMP/base.mp4" "$OUT/xmp.mp4"
et "$OUT/xmp.mp4" "-XMP-photoshop:DateCreated=2021:06:07 08:09:10+01:00" -XMP-exif:GPSLatitude=40.4168 \
  -XMP-exif:GPSLongitude=-3.7038 -XMP-dc:Title=Madrid
cp "$TMP/base.mov" "$OUT/xmp.mov"
et "$OUT/xmp.mov" "-XMP-xmp:CreateDate=2022:02:03 04:05:06" -XMP-exif:GPSLatitude=-22.9068 -XMP-exif:GPSLongitude=-43.1729

# Title and description in the item list (©nam, ©des), in user data
# (an MP4's 3GPP titl/dscp boxes), and in all three places with Keys,
# each different, for the order they're read in.
cp "$TMP/base.mp4" "$OUT/itemlist.mp4"
et "$OUT/itemlist.mp4" "-ItemList:Title=Item title" "-ItemList:Description=Item description"
cp "$TMP/base.mp4" "$OUT/userdata.mp4"
et "$OUT/userdata.mp4" "-UserData:Title=User title" "-UserData:Description=User description"
cp "$TMP/base.mp4" "$OUT/titles.mp4"
et "$OUT/titles.mp4" "-UserData:Title=User title" "-UserData:Description=User description" \
  "-ItemList:Title=Item title" "-ItemList:Description=Item description" "-Keys:Title=Keys title"

# A position in the item list (©xyz), as Android cameras write it.
cp "$TMP/base.mp4" "$OUT/itemlist-gps.mp4"
et "$OUT/itemlist-gps.mp4" "-ItemList:GPSCoordinates=-33.8568, 151.2153, 5"

# Turned: the track matrix (ffmpeg's display_rotation counts
# anticlockwise; these are 90 and 270 clockwise).
ff -display_rotation 270 -i "$TMP/base.mp4" -c copy "$OUT/rotate90.mp4"
ff -display_rotation 90 -i "$TMP/base.mov" -c copy "$OUT/rotate270.mov"

# Canon's uuid box in moov (see canon.go.txt): a thumbnail whose Exif
# has the make, model and date taken (a clock time, no zone).
DIR=$(cd "$(dirname "$0")" && pwd)
ff -f lavfi -i testsrc=size=160x120 -frames:v 1 -q:v 10 "$TMP/thumb.jpg"
et "$TMP/thumb.jpg" -Make=Canon "-Model=Canon PowerShot S1" "-DateTimeOriginal=2014:03:04 05:06:07" \
  "-CreateDate=2014:03:04 05:06:07" -Orientation#=1
cp "$DIR/canon.go.txt" "$TMP/canon.go"
(cd "$TMP" && go run canon.go base.mp4 thumb.jpg canon.mp4)
cp "$TMP/canon.mp4" "$OUT/canon.mp4"

rm -rf "$TMP"
