package mediameta

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

// regionsXMP has regions as Lightroom and Picasa-style writers lay them
// out: the struct fields as attributes, nested rdf:Descriptions, an extra
// field, a pet, a region in pixels and people named in Iptc4xmpExt:PersonInImage.
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
		{Name: "Ada", Type: "Face", X: 0.25, Y: 0.4, W: 0.1, H: 0.15, Rotation: 0.1},
		{Name: "Rex", Type: "Pet", X: 0.7, Y: 0.6, W: 0.2, H: 0.2},
		{Name: "Pixels", X: 100, Y: 100, W: 10, H: 10, Unit: "pixel"},
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
		{Name: "Grace", Type: "Face", X: 0.5, Y: 0.5, W: 0.2, H: 0.3, Rotation: -0.25, Description: "left & front"},
		{Name: "Rex", Type: "Pet", X: 0.1, Y: 0.9, W: 0.05, H: 0.05},
		{Type: "Focus", X: 2000, Y: 1500, D: 120, Unit: "pixel", FocusUsage: "EvaluatedUsed"},
		{Type: "BarCode", X: 0.9, Y: 0.1, W: 0.1, H: 0.1, BarCodeValue: "4006381333931"},
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
				Name, Type, Description, FocusUsage string
				BarCodeValue                        any // a number, to ExifTool's -n, when it reads as one
				Rotation                            float64
				Area                                struct {
					X, Y, W, H, D float64
					Unit          string
				}
			}
		}
		PersonInImage any
	}
	dec := json.NewDecoder(bytes.NewReader(out))
	dec.UseNumber()
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, e := range got {
		name := filepath.Base(e.SourceFile)
		x := parseXMP(t, files[name])
		et := Regions{AppliedTo: Dimensions{W: e.RegionInfo.AppliedToDimensions.W, H: e.RegionInfo.AppliedToDimensions.H, Unit: e.RegionInfo.AppliedToDimensions.Unit}}
		for _, g := range e.RegionInfo.RegionList {
			if g.BarCodeValue == nil {
				g.BarCodeValue = ""
			}
			et.List = append(et.List, Region{Name: g.Name, Type: g.Type, Description: g.Description, FocusUsage: g.FocusUsage,
				BarCodeValue: fmt.Sprint(g.BarCodeValue), Rotation: g.Rotation, X: g.Area.X, Y: g.Area.Y, W: g.Area.W, H: g.Area.H, D: g.Area.D, Unit: g.Area.Unit})
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

// fullRegionsXMP has regions with every field MWG defines and some it
// doesn't: Extensions with a nested struct in its own namespace, SeeAlso,
// a field as an attribute, unknown fields in the area, the dimensions and
// the Regions struct, and a namespace declared on the region itself.
const fullRegionsXMP = `<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">
 <rdf:Description rdf:about=""
   xmlns:mwg-rs="http://www.metadataworkinggroup.com/schemas/regions/"
   xmlns:stDim="http://ns.adobe.com/xap/1.0/sType/Dimensions#"
   xmlns:stArea="http://ns.adobe.com/xmp/sType/Area#"
   xmlns:ext="http://example.com/ns/ext/"
   xmlns:dc="http://purl.org/dc/elements/1.1/">
  <mwg-rs:Regions rdf:parseType="Resource">
   <mwg-rs:AppliedToDimensions rdf:parseType="Resource"><stDim:w>640</stDim:w><stDim:h>480</stDim:h><stDim:unit>pixel</stDim:unit><ext:dimsNote>scaled</ext:dimsNote></mwg-rs:AppliedToDimensions>
   <ext:regionsNote>from a scan</ext:regionsNote>
   <mwg-rs:RegionList>
    <rdf:Bag>
     <rdf:li rdf:parseType="Resource" xmlns:face="http://example.com/ns/face/">
      <mwg-rs:Area rdf:parseType="Resource"><stArea:x>0.5</stArea:x><stArea:y>0.4</stArea:y><stArea:w>0.2</stArea:w><stArea:h>0.25</stArea:h><stArea:unit>normalized</stArea:unit><ext:confidence>0.98</ext:confidence></mwg-rs:Area>
      <mwg-rs:Name>Ada</mwg-rs:Name>
      <mwg-rs:Type>Face</mwg-rs:Type>
      <mwg-rs:Description>on the left</mwg-rs:Description>
      <mwg-rs:Rotation>0.3</mwg-rs:Rotation>
      <mwg-rs:Extensions rdf:parseType="Resource">
       <face:Detector>model 7 &amp; co</face:Detector>
       <face:Landmarks><rdf:Seq><rdf:li>0.45,0.35</rdf:li><rdf:li>0.55,0.35</rdf:li></rdf:Seq></face:Landmarks>
      </mwg-rs:Extensions>
      <mwg-rs:SeeAlso><rdf:Bag><rdf:li>person:42</rdf:li></rdf:Bag></mwg-rs:SeeAlso>
     </rdf:li>
     <rdf:li ext:source="manual">
      <rdf:Description mwg-rs:Name="Rex" mwg-rs:Type="Pet">
       <mwg-rs:Area stArea:x="320" stArea:y="300" stArea:w="80" stArea:h="60" stArea:unit="pixel"/>
      </rdf:Description>
     </rdf:li>
    </rdf:Bag>
   </mwg-rs:RegionList>
  </mwg-rs:Regions>
  <dc:title><rdf:Alt><rdf:li xml:lang="x-default">Two</rdf:li></rdf:Alt></dc:title>
 </rdf:Description>
</rdf:RDF></x:xmpmeta>`

// A regions rewrite keeps every field it doesn't change: ExifTool reads
// the whole structure (-struct, every namespace) as before but for the
// change. Regions renamed, reordered, and made anew with the same areas
// each keep their regions' fields; on fullRegionsXMP, and on an ExifTool
// corpus file whose regions have every MWG field.
func TestRegionsKeptOnRewrite(t *testing.T) {
	tool, err := exec.LookPath("exiftool")
	if err != nil {
		t.Skip("exiftool not installed")
	}
	corpusFile := readFile(t, "testdata/exiftool/regions-full.jpg")
	corpusPacket, err := ReadXMP(bytes.NewReader(corpusFile), int64(len(corpusFile)))
	if err != nil {
		t.Fatal(err)
	}
	for source, src := range map[string][]byte{"hand-written": []byte(fullRegionsXMP), "ExifTool-written": corpusPacket} {
		t.Run(source, func(t *testing.T) { checkRegionsKept(t, tool, src) })
	}
}

func checkRegionsKept(t *testing.T, tool string, src []byte) {
	cur := parseXMP(t, src).Regions
	if cur == nil || len(cur.List) < 2 {
		t.Fatalf("read %+v", cur)
	}
	last := len(cur.List) - 1
	renamed := *cur
	renamed.List = slices.Clone(cur.List)
	renamed.List[0].Name = "Ada Lovelace"
	reordered := *cur
	reordered.List = slices.Clone(cur.List)
	slices.Reverse(reordered.List)
	fresh := Regions{AppliedTo: cur.AppliedTo}
	for _, g := range cur.List {
		g.kept, g.keptArea = nil, nil // as a caller would make it
		fresh.List = append(fresh.List, g)
	}
	fresh.List[last].Name = "Rex the dog"
	dir := t.TempDir()
	files := map[string][]byte{"source.xmp": src}
	for name, r := range map[string]*Regions{"renamed.xmp": &renamed, "reordered.xmp": &reordered, "fresh.xmp": &fresh} {
		files[name] = editXMP(t, src, XMPEdit{Regions: r})
	}
	for name, b := range files {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out, err := exec.Command(tool, "-j", "-n", "-q", "-struct", "-XMP:all", "-validate", "-warning", dir).Output()
	if err != nil {
		t.Fatal(err)
	}
	var got []map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	read := map[string]map[string]any{}
	for _, e := range got {
		name := filepath.Base(e["SourceFile"].(string))
		delete(e, "SourceFile")
		read[name] = e
	}
	asJSON := func(v any) string { b, _ := json.MarshalIndent(v, "", " "); return string(b) }
	before := read["source.xmp"]
	if _, ok := before["RegionInfo"]; !ok {
		t.Fatalf("ExifTool read no regions from the source: %s", asJSON(before))
	}
	for name, e := range read {
		if name == "source.xmp" {
			continue
		}
		if e["Warning"] != before["Warning"] {
			t.Errorf("%s: ExifTool warns %v, the source %v", name, e["Warning"], before["Warning"])
		}
		var want map[string]any
		if err := json.Unmarshal([]byte(asJSON(before)), &want); err != nil {
			t.Fatal(err)
		}
		ws := want["RegionInfo"].(map[string]any)["RegionList"].([]any)
		switch name {
		case "renamed.xmp":
			ws[0].(map[string]any)["Name"] = "Ada Lovelace"
		case "reordered.xmp":
			slices.Reverse(ws)
		case "fresh.xmp":
			ws[len(ws)-1].(map[string]any)["Name"] = "Rex the dog"
		}
		if asJSON(e) != asJSON(want) {
			t.Errorf("%s: ExifTool reads\n%s\nwant\n%s\n%s", name, asJSON(e), asJSON(want), files[name])
		}
	}
	// What ParseXMP models reads back too.
	if r := parseXMP(t, files["renamed.xmp"]).Regions; r == nil || !sameRegions(r, &renamed) {
		t.Errorf("renamed: %+v", r)
	}
}
