package mediameta

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRealVideos reads movies outside the repository: set
// MEDIAMETA_VIDEO_DIR to a folder of .mov/.mp4/.m4v files. Each must read
// without an error, and with a valid orientation where it has a video
// track. The fields are logged for comparing with other tools.
func TestRealVideos(t *testing.T) {
	dir := os.Getenv("MEDIAMETA_VIDEO_DIR")
	if dir == "" {
		t.Skip("set MEDIAMETA_VIDEO_DIR to test real movies")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		switch strings.ToLower(filepath.Ext(e.Name())) {
		case ".mov", ".mp4", ".m4v", ".qt", ".3gp":
		default:
			continue
		}
		t.Run(e.Name(), func(t *testing.T) {
			f, err := os.Open(filepath.Join(dir, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = f.Close() }()
			st, err := f.Stat()
			if err != nil {
				t.Fatal(err)
			}
			cr := &countingReader{r: f}
			v, err := ReadVideo(cr, st.Size())
			if err != nil {
				t.Fatal(err)
			}
			if v.Width > 0 && !v.Orientation.Valid() {
				t.Errorf("orientation %d", v.Orientation)
			}
			t.Logf("%+v GPS %+v; read %d bytes of %d", v, v.GPS, cr.bytes, st.Size())
		})
	}
}
