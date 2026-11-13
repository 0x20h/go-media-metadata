package mediameta

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

// regionsXMP has regions as Lightroom and Picasa-style writers lay them
// out: the struct fields as attributes, nested rdf:Descriptions, an extra
// field this package doesn't model, a pet, a region in pixels (left out)
// and people named in Iptc4xmpExt:PersonInImage.
const regionsXMP = `<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">
 <rdf:Description rdf:about=""
   xmlns:mwg-rs="http://www.metadataworkinggroup.com/schemas/regions/"
   xmlns:stDim="http://ns.adobe.com/xap/1.0/sType/Dimensions#"
   xmlns:stArea="http://ns.adobe.com/xmp/sType/Area#"
   xmlns:Iptc4xmpExt="http://iptc.org/std/Iptc4xmpExt/2008-02-29/"
   xmlns:dc="http://purl.org/dc/elements/1.1/">
  <dc:title><rdf:Alt><rdf:li xml:lang="x-default">Family</rdf:li></rdf:Alt></dc:title>
  <mwg-rs:Regions>
   <rdf:Description>
    <mwg-rs:AppliedToDimensions stDim:w="4000" stDim:h="3000" stDim:unit="pixel"/>
    <mwg-rs:RegionList>
     <rdf:Bag>
      <rdf:li>
       <rdf:Description mwg-rs:Name="Ada" mwg-rs:Type="Face">
        <mwg-rs:Area stArea:x="0.25" stArea:y="0.4" stArea:w="0.1" stArea:h="0.15" stArea:unit="normalized"/>
        <mwg-rs:Rotation>0.1</mwg-rs:Rotation>
       </rdf:Description>
      </rdf:li>
      <rdf:li rdf:parseType="Resource">
       <mwg-rs:Name>Rex</mwg-rs:Name>
       <mwg-rs:Type>Pet</mwg-rs:Type>
       <mwg-rs:Area rdf:parseType="Resource"><stArea:x>0.7</stArea:x><stArea:y>0.6</stArea:y><stArea:w>0.2</stArea:w><stArea:h>0.2</stArea:h><stArea:unit>normalized</stArea:unit></mwg-rs:Area>
      </rdf:li>
      <rdf:li mwg-rs:Name="Pixels"><mwg-rs:Area stArea:x="100" stArea:y="100" stArea:w="10" stArea:h="10" stArea:unit="pixel"/></rdf:li>
     </rdf:Bag>
    </mwg-rs:RegionList>
   </rdf:Description>
  </mwg-rs:Regions>
  <Iptc4xmpExt:PersonInImage><rdf:Bag><rdf:li>Ada</rdf:li></rdf:Bag></Iptc4xmpExt:PersonInImage>
 </rdf:Description>
</rdf:RDF></x:xmpmeta>`

func TestParseXMPRegions(t *testing.T) {
	x := parseXMP(t, []byte(regionsXMP))
	if x.Regions == nil {
		t.Fatal("no regions")
	}
	want := Regions{AppliedTo: Dimensions{W: 4000, H: 3000, Unit: "pixel"}, List: []Region{
		{Name: "Ada", Type: "Face", X: 0.25, Y: 0.4, W: 0.1, H: 0.15},
		{Name: "Rex", Type: "Pet", X: 0.7, Y: 0.6, W: 0.2, H: 0.2},
	}}
	if !sameRegions(x.Regions, &want) {
		t.Errorf("got %+v, want %+v", *x.Regions, want)
	}
	if !slices.Equal(x.PersonInImage, []string{"Ada"}) || x.Title != "Family" {
		t.Errorf("people %v, title %q", x.PersonInImage, x.Title)
	}
	if parseXMP(t, []byte(lightroomXMP)).Regions != nil {
		t.Error("regions read from a packet without any")
	}
}

func TestEditXMPRegions(t *testing.T) {
	r := Regions{AppliedTo: Dimensions{W: 4032, H: 3024}, List: []Region{
		{Name: "Grace", X: 0.5, Y: 0.5, W: 0.2, H: 0.3},
		{Name: "Ada & <Bob>", Type: "Face", X: 0.1, Y: 0.9, W: 0.05, H: 0.05},
	}}
	for name, src := range map[string]string{"new": "", "replacing": regionsXMP, "lightroom": lightroomXMP} {
		out := editXMP(t, []byte(src), XMPEdit{Regions: &r, PersonInImage: &[]string{"Grace", "Ada & <Bob>"}})
		x := parseXMP(t, out)
		if x.Regions == nil || !sameRegions(x.Regions, &r) {
			t.Fatalf("%s: got %+v", name, x.Regions)
		}
		if x.Regions.AppliedTo.Unit != "pixel" || x.Regions.List[0].Type != "Face" {
			t.Errorf("%s: unit %q, type %q: want the defaults written", name, x.Regions.AppliedTo.Unit, x.Regions.List[0].Type)
		}
		if !slices.Equal(x.PersonInImage, []string{"Grace", "Ada & <Bob>"}) {
			t.Errorf("%s: people %v", name, x.PersonInImage)
		}
		if name == "replacing" && (bytes.Contains(out, []byte("Rex")) || x.Title != "Family") {
			t.Errorf("%s: old region kept or title lost", name)
		}
		// Setting the same again changes nothing.
		if again := editXMP(t, out, XMPEdit{Regions: &r, PersonInImage: &[]string{"Grace", "Ada & <Bob>"}}); !bytes.Equal(again, out) {
			t.Errorf("%s: a second identical edit changed the packet", name)
		}
	}
	// No regions, no people: both removed.
	out := editXMP(t, []byte(regionsXMP), XMPEdit{Regions: &Regions{}, PersonInImage: &[]string{}})
	if x := parseXMP(t, out); x.Regions != nil || x.PersonInImage != nil || x.Title != "Family" {
		t.Errorf("got %+v", x)
	}
}

func TestEditXMPRegionsErrors(t *testing.T) {
	for name, r := range map[string]Regions{
		"no dimensions":  {List: []Region{{X: 0.5, Y: 0.5, W: 0.1, H: 0.1}}},
		"outside":        {AppliedTo: Dimensions{W: 10, H: 10}, List: []Region{{X: 1.5, Y: 0.5, W: 0.1, H: 0.1}}},
		"negative":       {AppliedTo: Dimensions{W: 10, H: 10}, List: []Region{{X: 0.5, Y: 0.5, W: -0.1, H: 0.1}}},
		"NaN":            {AppliedTo: Dimensions{W: 10, H: 10}, List: []Region{{X: math.NaN(), Y: 0.5, W: 0.1, H: 0.1}}},
		"infinite size":  {AppliedTo: Dimensions{W: math.Inf(1), H: 10}, List: []Region{{X: 0.5, Y: 0.5, W: 0.1, H: 0.1}}},
		"name not UTF-8": {AppliedTo: Dimensions{W: 10, H: 10}, List: []Region{{Name: "\xff", X: 0.5, Y: 0.5, W: 0.1, H: 0.1}}},
	} {
		if _, err := EditXMP(nil, XMPEdit{Regions: &r}); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

// ExifTool reads the regions EditXMP writes as ParseXMP does, and the
// regions of the sample packet the same way.
func TestXMPRegionsAgreeWithExifTool(t *testing.T) {
	tool, err := exec.LookPath("exiftool")
	if err != nil {
		t.Skip("exiftool not installed")
	}
	r := Regions{AppliedTo: Dimensions{W: 4032, H: 3024, Unit: "pixel"}, List: []Region{
		{Name: "Grace", Type: "Face", X: 0.5, Y: 0.5, W: 0.2, H: 0.3},
		{Name: "Rex", Type: "Pet", X: 0.1, Y: 0.9, W: 0.05, H: 0.05},
	}}
	dir := t.TempDir()
	files := map[string][]byte{
		"sample.xmp":  []byte(regionsXMP),
		"written.xmp": editXMP(t, []byte(lightroomXMP), XMPEdit{Regions: &r, PersonInImage: &[]string{"Grace"}}),
	}
	for name, b := range files {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out, err := exec.Command(tool, "-j", "-n", "-q", "-struct", "-XMP-mwg-rs:RegionInfo", "-XMP-iptcExt:PersonInImage", dir).Output()
	if err != nil {
		t.Fatal(err)
	}
	var got []struct {
		SourceFile string
		RegionInfo struct {
			AppliedToDimensions struct {
				W, H float64
				Unit string
			}
			RegionList []struct {
				Name, Type string
				Area       struct {
					X, Y, W, H float64
					Unit       string
				}
			}
		}
		PersonInImage any
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, e := range got {
		name := filepath.Base(e.SourceFile)
		x := parseXMP(t, files[name])
		et := Regions{AppliedTo: Dimensions{W: e.RegionInfo.AppliedToDimensions.W, H: e.RegionInfo.AppliedToDimensions.H, Unit: e.RegionInfo.AppliedToDimensions.Unit}}
		for _, g := range e.RegionInfo.RegionList {
			if g.Area.Unit != "normalized" {
				continue // ParseXMP leaves regions in other units out
			}
			et.List = append(et.List, Region{Name: g.Name, Type: g.Type, X: g.Area.X, Y: g.Area.Y, W: g.Area.W, H: g.Area.H})
		}
		if x.Regions == nil || !sameRegions(x.Regions, &et) {
			t.Errorf("%s: ExifTool %+v, ParseXMP %+v", name, et, x.Regions)
		}
		people := []string{}
		switch v := e.PersonInImage.(type) {
		case string:
			people = []string{v}
		case []any:
			for _, p := range v {
				people = append(people, p.(string))
			}
		}
		if !slices.Equal(people, x.PersonInImage) {
			t.Errorf("%s: ExifTool people %v, ParseXMP %v", name, people, x.PersonInImage)
		}
	}
}
