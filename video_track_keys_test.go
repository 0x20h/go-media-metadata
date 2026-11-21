package mediameta

import (
	"bytes"
	"testing"
)

// The lens in the video track's Keys (ExifTool's VideoKeys, as iPhones
// write it) is read where the movie's Keys have none. (ExifTool can't
// write the movie-level lens, so the case of both isn't made here; the
// movie's comes first.)
func TestVideoTrackKeysLens(t *testing.T) {
	file := readFile(t, "testdata/video/keys-track-lens.mov")
	v, err := ReadVideo(bytes.NewReader(file), int64(len(file)))
	const lens = "iPhone 12 Pro back camera 4.2mm f/1.6"
	if err != nil || v.LensModel != lens || v.Make != "Apple" {
		t.Errorf("ReadVideo %q %q, %v", v.Make, v.LensModel, err)
	}
	m, err := ReadAll(bytes.NewReader(file), int64(len(file)))
	if err != nil || m.Camera.LensModel != lens {
		t.Errorf("ReadAll lens %q, %v", m.Camera.LensModel, err)
	}
}
