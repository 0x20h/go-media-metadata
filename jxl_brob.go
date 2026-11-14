package mediameta

import (
	"bytes"
	"fmt"
	"io"

	"github.com/andybalholm/brotli"
)

// unbrob decompresses a brob box's Brotli stream (after its 4-byte box
// type), up to maxJXLMetadata bytes. It is the package's one use of
// Brotli: a file without brob boxes never reaches it.
func unbrob(stream []byte) ([]byte, error) {
	out, err := io.ReadAll(io.LimitReader(brotli.NewReader(bytes.NewReader(stream)), maxJXLMetadata+1))
	if err != nil {
		return nil, fmt.Errorf("%w: a brob box that doesn't decompress: %v", ErrMalformed, err)
	}
	if len(out) > maxJXLMetadata {
		return nil, fmt.Errorf("%w: a brob box past %d bytes", ErrUnsupported, maxJXLMetadata)
	}
	return out, nil
}
