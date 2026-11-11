package mediameta_test

import (
	"bytes"
	"fmt"

	mediameta "github.com/0x20h/go-media-metadata"
	"github.com/0x20h/go-media-metadata/internal/bmff/testbuild"
)

// A HEIC shown rotated 90° clockwise, turned to 180°: the angle changes,
// nothing else.
func Example() {
	file, _ := testbuild.Build(testbuild.Default())

	o, err := mediameta.ReadOrientation(bytes.NewReader(file), int64(len(file)))
	fmt.Println(o, err)

	var rotated bytes.Buffer
	err = mediameta.SetRotation(bytes.NewReader(file), int64(len(file)), &rotated, 180)
	fmt.Println(err)

	o, err = mediameta.ReadOrientation(bytes.NewReader(rotated.Bytes()), int64(rotated.Len()))
	fmt.Println(o, err, rotated.Len() == len(file))
	// Output:
	// rotated 90° clockwise <nil>
	// <nil>
	// rotated 180° clockwise <nil> true
}
