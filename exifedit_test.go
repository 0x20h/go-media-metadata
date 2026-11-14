package mediameta

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
	"time"
)

// Editing the Exif of every layout and sample file: the edit reads back,
// the orientation is as it was, and the plan's invariants hold.
func TestPlanExifEdit(t *testing.T) {
	d := time.Date(2031, 7, 8, 9, 10, 11, 120_000_000, time.FixedZone("", -7*3600))
	for _, f := range planFiles(t) {
		for _, c := range []struct {
			name string
			edit ExifEdit
		}{
			{"date", ExifEdit{DateTimeOriginal: &d, OffsetKnown: true}},
			{"gps", ExifEdit{GPS: &GPS{Latitude: 48.8584, Longitude: 2.2945, Altitude: 35, HasAltitude: true}}},
			{"remove gps", ExifEdit{RemoveGPS: true}},
		} {
			t.Run(f.name+"/"+c.name, func(t *testing.T) {
				p, err := planExifEdit(bytes.NewReader(f.data), int64(len(f.data)), c.edit)
				if err != nil {
					t.Fatal(err)
				}
				checkPlan(t, f.data, p)
				out := written(t, p)
				checkEdited(t, readExifTIFF(t, out), c.edit)
				if got, want := orientationOfFile(t, out), orientationOfFile(t, f.data); got != want {
					t.Errorf("orientation %d, was %d", got, want)
				}
			})
		}
	}
}

// readExifTIFF is the TIFF data of a HEIF file's Exif item.
func readExifTIFF(t *testing.T, file []byte) []byte {
	t.Helper()
	f, err := parseHEIF(bytes.NewReader(file), int64(len(file)))
	if err != nil {
		t.Fatal(err)
	}
	id, found, err := f.metadataItem(kindExif)
	if err != nil || !found {
		t.Fatalf("no Exif item: %v", err)
	}
	data, err := f.itemBytes(bytes.NewReader(file), int64(len(file)), id)
	if err != nil {
		t.Fatal(err)
	}
	return data[4+binary.BigEndian.Uint32(data):]
}

// A removed GPS position isn't left in the file.
func TestPlanExifEditRemovesGPSBytes(t *testing.T) {
	for _, f := range planFiles(t) {
		with := written(t, planMetadata(t, f.data, richTIFF(binary.BigEndian), nil))
		lat := binary.BigEndian.AppendUint32(binary.BigEndian.AppendUint32(nil, 2659), 100)
		if !bytes.Contains(with, lat) {
			t.Fatalf("%s: the fixture's latitude isn't there", f.name)
		}
		p, err := planExifEdit(bytes.NewReader(with), int64(len(with)), ExifEdit{RemoveGPS: true})
		if err != nil {
			t.Fatal(err)
		}
		if !p.InPlace {
			t.Errorf("%s: removing GPS isn't in place", f.name)
		}
		out := written(t, p)
		if bytes.Contains(out, lat) {
			t.Errorf("%s: the removed latitude is still in the file", f.name)
		}
		if x := parseExif(readExifTIFF(t, out)); x.GPS != nil {
			t.Errorf("%s: GPS %+v after removing it", f.name, x.GPS)
		}
	}
}

func TestPlanExifEditRefusesOrientation(t *testing.T) {
	file := planFiles(t)[0].data
	if _, err := planExifEdit(bytes.NewReader(file), int64(len(file)), ExifEdit{Orientation: 3}); !errors.Is(err, ErrOrientationInExif) {
		t.Errorf("an Orientation edit: %v", err)
	}
}

// A new date where the item has one of the same shape (date, offset and
// a two-digit fraction) is in place, on every layout, in mdat and idat.
func TestPlanExifEditDateInPlace(t *testing.T) {
	d := time.Date(2031, 7, 8, 9, 10, 11, 120_000_000, time.FixedZone("", -7*3600))
	for _, f := range planFiles(t) {
		with := written(t, planMetadata(t, f.data, photoTIFF(binary.LittleEndian), nil))
		p, err := planExifEdit(bytes.NewReader(with), int64(len(with)), ExifEdit{DateTimeOriginal: &d, OffsetKnown: true})
		if err != nil {
			t.Fatal(err)
		}
		if !p.InPlace {
			t.Errorf("%s: not in place (%d → %d bytes)", f.name, p.Size, p.NewSize)
		}
		checkPlan(t, with, p)
		checkEdited(t, readExifTIFF(t, written(t, p)), ExifEdit{DateTimeOriginal: &d, OffsetKnown: true})
	}
}
