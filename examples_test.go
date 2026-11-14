package mediameta_test

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"time"

	mediameta "github.com/0x20h/go-media-metadata"
)

// Reading a photo's metadata: when it was taken, where, and how it's shown.
func ExampleReadExif() {
	f, err := os.Open("testdata/iphone/grid-gainmap-orientation6.heic")
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		log.Fatal(err)
	}

	x, err := mediameta.ReadExif(f, info.Size())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(x.DateTimeOriginal.Format(time.RFC3339), x.OffsetKnown)
	fmt.Println(x.GPS == nil)

	// The orientation a viewer shows: for HEIF, its irot and imir.
	o, err := mediameta.ReadOrientation(f, info.Size())
	fmt.Println(o, err)
	// Output:
	// 2026-11-11T21:16:28+01:00 true
	// true
	// rotated 90° clockwise <nil>
}

// Editing in place: the plan lists the bytes that change, to write into a
// copy of the file (a copy-on-write clone, say) instead of writing a new
// one. Here the date taken changes and the file keeps its size.
func ExamplePlanMetadata() {
	src, err := os.ReadFile("testdata/iphone/grid-gainmap-orientation6.heic")
	if err != nil {
		log.Fatal(err)
	}

	taken := time.Date(2024, 6, 1, 12, 30, 0, 0, time.FixedZone("", 2*60*60))
	plan, err := mediameta.PlanMetadata(bytes.NewReader(src), int64(len(src)), mediameta.MetadataEdit{
		Exif: &mediameta.ExifEdit{DateTimeOriginal: &taken, OffsetKnown: true},
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(plan.InPlace, len(plan.Patches))

	// Apply writes the patches to anything holding the source's bytes: an
	// *os.File opened on a clone, or here a copy in memory.
	copied := &memFile{b: bytes.Clone(src)}
	if err := plan.Apply(copied); err != nil {
		log.Fatal(err)
	}
	x, _ := mediameta.ReadExif(bytes.NewReader(copied.b), int64(len(copied.b)))
	fmt.Println(x.DateTimeOriginal.Format(time.RFC3339))
	// Output:
	// true 1
	// 2024-06-01T12:30:00+02:00
}

// memFile is an io.WriterAt over a byte slice of fixed size.
type memFile struct{ b []byte }

func (m *memFile) WriteAt(p []byte, off int64) (int, error) {
	if off < 0 || off+int64(len(p)) > int64(len(m.b)) {
		return 0, fmt.Errorf("write past the end")
	}
	return copy(m.b[off:], p), nil
}

// Rotating a photo: the orientation is set, the image data copied as it
// is. The same call takes a JPEG, PNG, WebP, camera RAW or a movie.
func ExampleSetRotation() {
	src, err := os.ReadFile("testdata/imageio/b-orientation6.heic")
	if err != nil {
		log.Fatal(err)
	}
	var out bytes.Buffer
	if err := mediameta.SetRotation(bytes.NewReader(src), int64(len(src)), &out, 180); err != nil {
		log.Fatal(err)
	}
	o, err := mediameta.ReadOrientation(bytes.NewReader(out.Bytes()), int64(out.Len()))
	fmt.Println(o, err)
	// Output:
	// rotated 180° clockwise <nil>
}

// Setting a video's recording date and place. Write to a temporary file
// and rename it over the original only once the call has succeeded.
func ExampleSetVideoMetadata() {
	in, err := os.Open("clip.mov")
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = in.Close() }()
	info, err := in.Stat()
	if err != nil {
		log.Fatal(err)
	}
	out, err := os.CreateTemp(".", "clip-*.mov")
	if err != nil {
		log.Fatal(err)
	}

	recorded := time.Date(2024, 6, 1, 12, 30, 0, 0, time.FixedZone("", 2*60*60))
	err = mediameta.SetVideoMetadata(in, info.Size(), out, mediameta.VideoEdit{
		Created: &recorded,
		GPS:     &mediameta.GPS{Latitude: 52.52, Longitude: 13.405},
	})
	if err != nil {
		log.Fatal(err)
	}
	if err := out.Close(); err != nil {
		log.Fatal(err)
	}
	if err := os.Rename(out.Name(), "clip.mov"); err != nil {
		log.Fatal(err)
	}
}
