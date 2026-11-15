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

# A position and dates in XMP only: a latitude in DD,MM.mmmm form, an
# altitude below sea level, a date with a zone and one without.
et xmp-where-when.jpg -XMP-exif:GPSLatitude=-33.865 -XMP-exif:GPSLongitude=151.2094 \
  -XMP-exif:GPSAltitude=12 -XMP-exif:GPSAltitudeRef#=1 \
  "-XMP-photoshop:DateCreated=2019:12:31 23:59:59.5-05:00" "-XMP-xmp:CreateDate=2019:12:31 23:59"

# Camera, lens and exposure, in each byte order; camera-mm.jpg's ISO is
# past 65535, so it's in RecommendedExposureIndex.
et camera-ii.jpg -ExifByteOrder#=II -EXIF:Make=Canon "-EXIF:Model=Canon EOS R6" -EXIF:LensMake=Canon \
  "-EXIF:LensModel=RF70-200mm F2.8 L IS USM" -EXIF:ISO=400 -EXIF:FNumber=2.8 -EXIF:ExposureTime=1/250 \
  -EXIF:FocalLength=135 -EXIF:FocalLengthIn35mmFormat=135
et camera-mm.jpg -ExifByteOrder#=MM -EXIF:Make=Apple "-EXIF:Model=iPhone 12 Pro" \
  "-EXIF:LensModel=iPhone 12 Pro back triple camera 4.2mm f/1.6" -EXIF:ISO=65535 -EXIF:SensitivityType#=2 \
  -EXIF:RecommendedExposureIndex=102400 -EXIF:FNumber=1.6 -EXIF:ExposureTime=0.0588235294 -EXIF:FocalLength=4.2 \
  -EXIF:FocalLengthIn35mmFormat=26

# The date taken in one place each: Exif DateTimeDigitized (CreateDate)
# only, IPTC Date Created and Time Created only, XMP exif:DateTimeOriginal
# only.
et date-digitized.jpg "-EXIF:CreateDate=2023:03:04 05:06:07" -EXIF:SubSecTimeDigitized=5 -EXIF:OffsetTimeDigitized=-03:00
et date-iptc.jpg -IPTC:DateCreated=2022:01:02 -IPTC:TimeCreated=10:20:30+05:30
et date-xmp.jpg "-XMP-exif:DateTimeOriginal=2021:06:07 08:09:10+01:00"

# Regions with every MWG field: a rotated face with a description and
# Extensions, a focus circle in pixels, a bar code.
et regions-full.jpg '-XMP-mwg-rs:RegionInfo={AppliedToDimensions={W=64,H=48,Unit=pixel},RegionList=[{Area={X=0.25,Y=0.33,W=0.25,H=0.33,Unit=normalized},Name=Ada,Type=Face,Description=left,Rotation=0.5,Extensions={XMP-dc:Title=detector 7}},{Area={X=40,Y=30,D=8,Unit=pixel},Type=Focus,FocusUsage#=EvaluatedUsed},{Area={X=0.9,Y=0.1,W=0.1,H=0.1,Unit=normalized},Type=BarCode,BarCodeValue=4006381333931}]}'

# Everything at once, as an editor that writes all three would.
et all.jpg -ExifByteOrder#=II -EXIF:Orientation#=3 "-EXIF:DateTimeOriginal=2024:08:15 08:30:00" -EXIF:OffsetTimeOriginal=+01:00 \
  -GPSLatitude=48.137 -GPSLatitudeRef=N -GPSLongitude=11.575 -GPSLongitudeRef=E \
  -XMP-dc:Title=Morning "-XMP-dc:Description=Breakfast outside" -XMP-dc:Subject=summer -XMP-xmp:Rating=5 \
  -charset iptc=UTF8 -IPTC:CodedCharacterSet=UTF8 -IPTC:ObjectName=Morning -IPTC:Keywords=summer \
  "-IPTC:Caption-Abstract=Breakfast outside" -IPTCDigest=new

rm -f "$ARGS"
