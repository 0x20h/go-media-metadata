// Package mediameta reads and writes metadata of image and video files in
// pure Go, losslessly: a write changes only the metadata it's asked to and
// copies the encoded image data byte for byte.
//
// The first format is HEIF/HEIC (ISO/IEC 23008-12): reading an image's
// orientation from its transformative properties (irot, imir) and
// rotating it by rewriting its irot property, without decoding or
// re-encoding anything.
//
// For JPEG it reads the Exif APP1 segment (ReadExif, ReadOrientation),
// walking the marker segments up to the image data and no further.
//
// Every parser treats its input as untrusted: lengths are checked against
// their container, counts against the bytes left, and nesting and sizes are
// capped, so a hostile file gives an error, never a panic or a hang.
package mediameta
