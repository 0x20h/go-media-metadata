#!/bin/sh
# Writes the ExifTool corpus: testdata/exiftool/*.jpg from base.jpg.
# Usage: mkcorpus.sh <exiftool> <base.jpg> <outdir>
# The tag values go through UTF-8 argument files (-@), so that non-ASCII
# text reaches ExifTool as written, whatever the console's code page.
set -e
ET="$1"; BASE="$2"; OUT="$3"
ARGS="$OUT/.args"
et() { # et <file> <args…>: one argument per line
  f="$1"; shift
  : > "$ARGS"
  for a in "$@"; do printf '%s\n' "$a" >> "$ARGS"; done
  cp "$BASE" "$OUT/$f"
  "$ET" -q -overwrite_original -charset UTF8 -@ "$ARGS" "$OUT/$f"
}

et exif-ii.jpg -ExifByteOrder#=II -EXIF:Orientation#=6 \
  "-EXIF:DateTimeOriginal=2025:05:04 13:14:15" -EXIF:SubSecTimeOriginal=25 -EXIF:OffsetTimeOriginal=+02:00 \
  -GPSLatitude=51.5 -GPSLatitudeRef=N -GPSLongitude=0.125 -GPSLongitudeRef=W -GPSAltitude=35.5 -GPSAltitudeRef#=0

et exif-mm.jpg -ExifByteOrder#=MM -EXIF:Orientation#=8 \
  "-EXIF:DateTimeOriginal=2019:12:31 23:59:59" -EXIF:OffsetTimeOriginal=-05:00 \
  -GPSLatitude=33.865 -GPSLatitudeRef=S -GPSLongitude=151.2094 -GPSLongitudeRef=E -GPSAltitude=12 -GPSAltitudeRef#=1

et xmp.jpg -XMP-dc:Title=Lake -XMP-dc:Title-de=See "-XMP-dc:Description=A lake in the mountains" \
  -XMP-dc:Subject=lake -XMP-dc:Subject=snow -XMP-xmp:Rating=4 -XMP-iptcExt:PersonInImage=Ada \
  '-XMP-mwg-rs:RegionInfo={AppliedToDimensions={W=64,H=48,Unit=pixel},RegionList=[{Area={X=0.25,Y=0.33,W=0.25,H=0.33,Unit=normalized},Name=Ada,Type=Face}]}'

et iptc-utf8.jpg -charset iptc=UTF8 -IPTC:CodedCharacterSet=UTF8 -IPTC:ObjectName=Grün -IPTC:Keywords=Brücke -IPTC:Keywords=See \
  "-IPTC:Caption-Abstract=Am See" -IPTC:By-line=Fotograf -IPTCDigest=new

et iptc-latin1.jpg -charset iptc=Latin -IPTC:ObjectName=Grün -IPTC:Keywords=Brücke "-IPTC:Caption-Abstract=Am See"

# Everything at once, as an editor that writes all three would.
et all.jpg -ExifByteOrder#=II -EXIF:Orientation#=3 "-EXIF:DateTimeOriginal=2024:08:15 08:30:00" -EXIF:OffsetTimeOriginal=+01:00 \
  -GPSLatitude=48.137 -GPSLatitudeRef=N -GPSLongitude=11.575 -GPSLongitudeRef=E \
  -XMP-dc:Title=Morning "-XMP-dc:Description=Breakfast outside" -XMP-dc:Subject=summer -XMP-xmp:Rating=5 \
  -charset iptc=UTF8 -IPTC:CodedCharacterSet=UTF8 -IPTC:ObjectName=Morning -IPTC:Keywords=summer \
  "-IPTC:Caption-Abstract=Breakfast outside" -IPTCDigest=new

rm -f "$ARGS"
