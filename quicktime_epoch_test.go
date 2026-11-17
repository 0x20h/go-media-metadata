package mediameta

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/0x20h/go-media-metadata/internal/bmff/qtbuild"
)

// Movie header times: since 1904 as the format has them, and since 1970
// as some writers wrongly put them (read from 1970, as ExifTool does).
var epochCases = []struct {
	secs uint64
	want time.Time
}{
	{3845750400, time.Date(2025, 11, 12, 0, 0, 0, 0, time.UTC)}, // 1904-based
	{1420070400, time.Date(2015, 1, 1, 0, 0, 0, 0, time.UTC)},   // 1970-based
	{1, time.Unix(1, 0).UTC()},
}

func epochMovie(secs uint64) []byte {
	o := qtbuild.Default()
	o.Keys = nil // no Keys date: the movie header's is the movie's
	o.Created = secs
	file, _, _ := qtbuild.Build(o)
	return file
}

func TestMovieHeaderEpoch(t *testing.T) {
	for _, c := range epochCases {
		file := epochMovie(c.secs)
		v, err := ReadVideo(bytes.NewReader(file), int64(len(file)))
		if err != nil || !v.MovieCreated.Equal(c.want) || !v.Created.Equal(c.want) {
			t.Errorf("%d: %v / %v (%v), want %v", c.secs, v.MovieCreated, v.Created, err, c.want)
		}
	}
	tool, err := exec.LookPath("exiftool")
	if err != nil {
		t.Skip("exiftool not installed")
	}
	dir := t.TempDir()
	for i, c := range epochCases {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("e%d.mov", i)), epochMovie(c.secs), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Without QuickTimeUTC (with it, ExifTool always counts from 1904),
	// in UTC so that its dates read as written.
	cmd := exec.Command(tool, "-j", "-q", "-d", "%Y-%m-%dT%H:%M:%SZ", "-QuickTime:CreateDate", dir)
	cmd.Env = append(os.Environ(), "TZ=UTC")
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	var got []map[string]any
	if err := json.Unmarshal(out, &got); err != nil || len(got) != len(epochCases) {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, e := range got {
		var i int
		if _, err := fmt.Sscanf(filepath.Base(e["SourceFile"].(string)), "e%d.mov", &i); err != nil {
			t.Fatal(err)
		}
		at, err := time.Parse(time.RFC3339, fmt.Sprint(e["CreateDate"]))
		if err != nil || !at.Equal(epochCases[i].want) {
			t.Errorf("%d: ExifTool %v, want %v", epochCases[i].secs, e["CreateDate"], epochCases[i].want)
		}
	}
}
