package mediameta

import (
	"bytes"
	"encoding/binary"
	"testing"
	"time"
)

// A Canon MP4's date taken is the Exif DateTimeOriginal of the thumbnail
// in its uuid box, local time as the camera wrote it, ahead of the movie
// header's creation time, which Canon writes in UTC: an hour apart for a
// camera set to UTC+1.
func TestCanonDateBeforeMovieHeader(t *testing.T) {
	file := readFile(t, "testdata/video/canon.mp4")
	i := bytes.Index(file, []byte("mvhd"))
	if i < 0 || file[i+4] != 0 {
		t.Fatal("no version 0 mvhd")
	}
	// The movie header an hour behind the Exif date, as UTC.
	utc := time.Date(2014, 3, 4, 4, 6, 7, 0, time.UTC)
	binary.BigEndian.PutUint32(file[i+8:], uint32(utc.Unix()+2082844800)) // seconds since 1904
	v, err := ReadVideo(bytes.NewReader(file), int64(len(file)))
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2014, 3, 4, 5, 6, 7, 0, time.UTC) // the clock time, offset unknown
	if !v.Created.Equal(want) || v.OffsetKnown {
		t.Errorf("Created %v (offset known %v), want the Exif date %v", v.Created, v.OffsetKnown, want)
	}
	if !v.MovieCreated.Equal(utc) {
		t.Errorf("MovieCreated %v, want %v", v.MovieCreated, utc)
	}
	m, err := ReadAll(bytes.NewReader(file), int64(len(file)))
	if err != nil {
		t.Fatal(err)
	}
	if m.Taken.Source != SourceMovie || !m.Taken.Time.Equal(want) || m.Taken.OffsetKnown {
		t.Errorf("ReadAll %+v", m.Taken)
	}
}
