// Package mediameta reads and writes the metadata of image and video files
// in pure Go, losslessly: a write changes only the metadata it's asked to,
// and copies the image and media data byte for byte. Nothing is decoded or
// re-encoded.
//
// # Formats
//
// HEIF (HEIC, AVIF), JPEG, PNG, WebP and JPEG XL images; camera RAW files
// (DNG, CR2, CR3, NEF, ARW, ORF, RW2 and, read only, RAF); QuickTime and
// MP4 movies. A file's format is told from its first bytes, never its
// name. The README's table and the docs folder say what each format has
// read and written.
//
// # Reading
//
// ReadOrientation, ReadExif and ReadVideo return parsed fields. ReadXMP
// and ReadIPTC return a file's XMP packet and IPTC-IIM data as they are,
// for ParseXMP and ParseIPTC to parse: XMP and IPTC are often kept or
// passed on whole, Exif rarely. ReadPreviews and ReadEmbedded say where a
// RAW's previews, and a JPEG's Motion Photo video and gain map, are.
// Readers read only the metadata, never the image or media data.
//
// # Writing
//
// A Plan… function computes an edit of a file without writing it; the Set…
// function of the same name streams that edit to an io.Writer. A Plan
// whose edit keeps the file's size (InPlace) can instead be applied to a
// copy of the file, such as a copy-on-write clone, writing only the bytes
// that change. Edit… functions change metadata held in memory (TIFF data,
// an XMP packet, IPTC-IIM data), not a file. No function writes to its
// source: write to a temporary file, or apply the plan to a copy, and
// replace the original only once the call has succeeded.
//
// The …Edit structs say what to change; a field left at its zero value
// (nil for a pointer) leaves that part as it is, so a zero edit changes
// nothing.
//
// # Errors
//
// Every error wraps ErrUnknownFormat, ErrMalformed or ErrUnsupported (or,
// for Plan.Apply, ErrNotInPlace), with context; test for them with
// errors.Is. A file this package can't change safely is refused rather
// than risk a wrong result.
//
// # Concurrency
//
// The package keeps no state: its functions can be called from many
// goroutines at once, also on the same source, as long as the source's
// ReadAt can be (*os.File's and *bytes.Reader's can). A Plan holds its
// source and is read only.
//
// # Hostile input
//
// Every parser treats its input as untrusted and is fuzzed: lengths are
// checked against their container, counts against the bytes left, and
// nesting and sizes are capped, so a hostile file gives an error, never a
// panic or a hang.
package mediameta
