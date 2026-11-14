package mediameta

import (
	"errors"

	"github.com/0x20h/go-media-metadata/internal/bmff"
)

// The errors this package's functions return wrap one of these, with
// context: test for them with errors.Is.
var (
	// ErrUnknownFormat is returned for a file of no format the function
	// takes: one this package doesn't read, or, for a function that takes
	// one format (ReadVideo, ReadPreviews, ReadEmbedded), another one.
	ErrUnknownFormat = errors.New("mediameta: not a file of a format this function takes")

	// ErrUnsupported is returned for a file this package can't read or
	// change safely: a structure or a value it doesn't handle. The file
	// is left as it is rather than risk a wrong result.
	ErrUnsupported = errors.New("mediameta: unsupported file structure")

	// ErrMalformed is returned for input that breaks its format's
	// structure: a length past its container, a truncated header, too
	// many entries.
	ErrMalformed = bmff.ErrMalformed
)

// formatError is a file that isn't of one format; it is ErrUnknownFormat.
type formatError string

func (e formatError) Error() string { return "mediameta: not a " + string(e) }
func (e formatError) Unwrap() error { return ErrUnknownFormat }

// The formats' own errors, for telling them apart inside the package.
const (
	errNotHEIF      = formatError("HEIF image file")
	errNotJPEG      = formatError("JPEG file")
	errNotPNG       = formatError("PNG file")
	errNotWebP      = formatError("WebP file")
	errNotJXL       = formatError("JPEG XL file")
	errNotRAW       = formatError("camera RAW or TIFF file")
	errNotQuickTime = formatError("QuickTime or MP4 file")
)
