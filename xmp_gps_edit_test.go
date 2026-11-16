package mediameta

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

const gpsBase = `<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
	`<rdf:Description rdf:about="" xmlns:exif="http://ns.adobe.com/exif/1.0/" exif:GPSLatitude="10,30N" exif:GPSLongitude="20,15E"` +
	` exif:GPSAltitude="100/1" exif:GPSAltitudeRef="0"/></rdf:RDF></x:xmpmeta>`

// editPositions are the positions TestEditXMPGPS writes: each quadrant,
// with and without an altitude, below sea level, the poles and the
// antimeridian, a coordinate that rounds up to the next degree.
var editPositions = []GPS{
	{Latitude: 52.520008, Longitude: 13.404954, Altitude: 34, HasAltitude: true},
	{Latitude: -33.865143, Longitude: 151.2099, Altitude: 12.5, HasAltitude: true},
	{Latitude: 40.416775, Longitude: -3.70379},
	{Latitude: -22.906847, Longitude: -43.172897, Altitude: -2.25, HasAltitude: true},
	{Latitude: 31.5590, Longitude: 35.4732, Altitude: -430.5, HasAltitude: true},
	{Latitude: 90, Longitude: 180},
	{Latitude: -90, Longitude: -180, Altitude: 0, HasAltitude: true},
	{Latitude: 0, Longitude: 0},
	{Latitude: 47.99999999999, Longitude: 8.000000000001},
}

func samePosition(a, b *GPS) bool {
	if a == nil || b == nil {
		return a == b
	}
	return math.Abs(a.Latitude-b.Latitude) < 1e-6 && math.Abs(a.Longitude-b.Longitude) < 1e-6 &&
		a.HasAltitude == b.HasAltitude && (!a.HasAltitude || math.Abs(a.Altitude-b.Altitude) < 1e-3)
}

func TestEditXMPGPS(t *testing.T) {
	for _, want := range editPositions {
		g := want
		out, err := EditXMP([]byte(gpsBase), XMPEdit{GPS: &g})
		if err != nil {
			t.Fatalf("%+v: %v", want, err)
		}
		if got := parseXMP(t, out).GPS; !samePosition(got, &want) {
			t.Errorf("wrote %+v, read %+v\n%s", want, got, out)
		}
		// Into a packet without one.
		out, err = EditXMP([]byte(datesBaseForGPS), XMPEdit{GPS: &g})
		if err != nil || !samePosition(parseXMP(t, out).GPS, &want) {
			t.Errorf("new %+v: %v\n%s", want, err, out)
		}
	}
	out, err := EditXMP([]byte(gpsBase), XMPEdit{RemoveGPS: true})
	if x := parseXMP(t, out); err != nil || x.GPS != nil || bytes.Contains(out, []byte("GPS")) {
		t.Errorf("removal: %v\n%s", err, out)
	}
	same := GPS{Latitude: 10.5, Longitude: 20.25, Altitude: 100, HasAltitude: true}
	if out, err := EditXMP([]byte(gpsBase), XMPEdit{GPS: &same}); err != nil || !samePosition(parseXMP(t, out).GPS, &same) {
		t.Errorf("same position: %v\n%s", err, out)
	}
	for _, bad := range []XMPEdit{
		{GPS: &GPS{Latitude: 91}},
		{GPS: &GPS{Longitude: -181}},
		{GPS: &GPS{Latitude: math.NaN()}},
		{GPS: &GPS{}, RemoveGPS: true},
	} {
		if _, err := EditXMP([]byte(gpsBase), bad); err == nil {
			t.Errorf("%+v written", bad)
		}
	}
}

const datesBaseForGPS = `<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
	`<rdf:Description rdf:about=""/></rdf:RDF></x:xmpmeta>`

// ExifTool reads every position EditXMP writes as written.
func TestEditXMPGPSAgreesWithExifTool(t *testing.T) {
	tool, err := exec.LookPath("exiftool")
	if err != nil {
		t.Skip("exiftool not installed")
	}
	dir := t.TempDir()
	for i, g := range editPositions {
		g := g
		out, err := EditXMP([]byte(gpsBase), XMPEdit{GPS: &g})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("g%d.xmp", i)), out, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out, err := exec.Command(tool, "-j", "-n", "-q", "-validate", "-warning", "-XMP:GPSLatitude", "-XMP:GPSLongitude",
		"-XMP:GPSAltitude", "-XMP:GPSAltitudeRef", dir).Output()
	if err != nil {
		t.Fatal(err)
	}
	var got []map[string]any
	if err := json.Unmarshal(out, &got); err != nil || len(got) != len(editPositions) {
		t.Fatalf("%v (%d files)\n%s", err, len(got), out)
	}
	for _, e := range got {
		name := filepath.Base(e["SourceFile"].(string))
		var i int
		if _, err := fmt.Sscanf(name, "g%d.xmp", &i); err != nil || i >= len(editPositions) {
			t.Fatalf("%s: %v", name, err)
		}
		if w, ok := e["Warning"]; ok {
			t.Errorf("%s: ExifTool warns %v", name, w)
		}
		lat, _ := e["GPSLatitude"].(float64)
		lon, _ := e["GPSLongitude"].(float64)
		alt, hasAlt := e["GPSAltitude"].(float64)
		if ref, _ := e["GPSAltitudeRef"].(float64); ref == 1 {
			alt = -alt
		}
		g := GPS{Latitude: lat, Longitude: lon, Altitude: alt, HasAltitude: hasAlt}
		if !samePosition(&g, &editPositions[i]) {
			t.Errorf("%s: ExifTool %+v, wrote %+v", name, g, editPositions[i])
		}
	}
}
