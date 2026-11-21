package mediameta

import (
	"encoding/json"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// mpPacket wraps Microsoft Photo RegionInfo XML (the rdf:li items) in a
// packet as ExifTool writes it, with mwg-rs regions alongside when mwg
// isn't "".
func mpPacket(lis, mwg string) string {
	return `<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">
<rdf:Description rdf:about="" xmlns:MP="http://ns.microsoft.com/photo/1.2/"
 xmlns:MPRI="http://ns.microsoft.com/photo/1.2/t/RegionInfo#" xmlns:MPReg="http://ns.microsoft.com/photo/1.2/t/Region#">
<MP:RegionInfo rdf:parseType="Resource"><MPRI:Regions><rdf:Bag>` + lis + `</rdf:Bag></MPRI:Regions></MP:RegionInfo>
</rdf:Description>` + mwg + `</rdf:RDF></x:xmpmeta>`
}

func TestMicrosoftRegions(t *testing.T) {
	for _, c := range []struct {
		name, lis, mwg string
		want           []MicrosoftRegion
	}{
		{"name and box, name only", `<rdf:li rdf:parseType="Resource"><MPReg:PersonDisplayName>Ann Lee</MPReg:PersonDisplayName><MPReg:Rectangle>0.1, 0.2, 0.3, 0.4</MPReg:Rectangle></rdf:li>
<rdf:li rdf:parseType="Resource"><MPReg:PersonDisplayName>Bob</MPReg:PersonDisplayName></rdf:li>`, "",
			[]MicrosoftRegion{{Name: "Ann Lee", HasRect: true, X: 0.1, Y: 0.2, W: 0.3, H: 0.4}, {Name: "Bob"}}},
		{"attributes, as Windows writes them", `<rdf:li MPReg:PersonDisplayName="Cleo" MPReg:Rectangle="0, 0, 1, 1"/>`, "",
			[]MicrosoftRegion{{Name: "Cleo", HasRect: true, W: 1, H: 1}}},
		{"a box without a name", `<rdf:li MPReg:Rectangle="0.5, 0.5, 0.25, 0.25"/>`, "",
			[]MicrosoftRegion{{HasRect: true, X: 0.5, Y: 0.5, W: 0.25, H: 0.25}}},
		{"boxes not taken, names kept", `<rdf:li MPReg:PersonDisplayName="Dan" MPReg:Rectangle="0.1, 0.2, 0.3"/>
<rdf:li MPReg:PersonDisplayName="Eve" MPReg:Rectangle="0.8, 0.1, 0.5, 0.1"/>
<rdf:li MPReg:PersonDisplayName="Fay" MPReg:Rectangle="-0.1, 0, 0.5, 0.5"/>
<rdf:li MPReg:PersonDisplayName="Gus" MPReg:Rectangle="a, b, c, d"/>
<rdf:li MPReg:Rectangle="2, 2, 1, 1"/>`, "",
			[]MicrosoftRegion{{Name: "Dan"}, {Name: "Eve"}, {Name: "Fay"}, {Name: "Gus"}}},
		{"alongside MWG regions", `<rdf:li MPReg:PersonDisplayName="Hal"/>`, `<rdf:Description rdf:about="" xmlns:mwg-rs="http://www.metadataworkinggroup.com/schemas/regions/"
 xmlns:stArea="http://ns.adobe.com/xmp/sType/Area#"><mwg-rs:Regions rdf:parseType="Resource"><mwg-rs:RegionList><rdf:Bag>
<rdf:li rdf:parseType="Resource"><mwg-rs:Name>Ivy</mwg-rs:Name><mwg-rs:Area stArea:x="0.5" stArea:y="0.5" stArea:w="0.1" stArea:h="0.1"/></rdf:li>
</rdf:Bag></mwg-rs:RegionList></mwg-rs:Regions></rdf:Description>`,
			[]MicrosoftRegion{{Name: "Hal"}}},
	} {
		x, err := ParseXMP([]byte(mpPacket(c.lis, c.mwg)))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if !reflect.DeepEqual(x.MicrosoftRegions, c.want) {
			t.Errorf("%s: %+v, want %+v", c.name, x.MicrosoftRegions, c.want)
		}
		if c.mwg != "" && (x.Regions == nil || len(x.Regions.List) != 1 || x.Regions.List[0].Name != "Ivy") {
			t.Errorf("%s: MWG regions %+v", c.name, x.Regions)
		}
	}
	if x, _ := ParseXMP([]byte(mpPacket("", ""))); x.MicrosoftRegions != nil {
		t.Errorf("no regions: %+v", x.MicrosoftRegions)
	}
}

// Packets ExifTool writes read as ExifTool reads them.
func TestMicrosoftRegionsAgreeWithExifTool(t *testing.T) {
	tool, err := exec.LookPath("exiftool")
	if err != nil {
		t.Skip("exiftool not installed")
	}
	for i, arg := range []string{
		"{Regions=[{PersonDisplayName=Ann Lee,Rectangle=0.1|, 0.2|, 0.3|, 0.4},{PersonDisplayName=Bob}]}",
		"{Regions=[{PersonDisplayName=Zoë Ångström,Rectangle=0.25|, 0.125|, 0.5|, 0.5}]}",
		"{Regions=[{Rectangle=0|, 0|, 1|, 1},{PersonDisplayName=Cleo,Rectangle=0.6|, 0.6|, 0.2|, 0.3}]}",
	} {
		p := filepath.Join(t.TempDir(), strconv.Itoa(i)+".xmp")
		if out, err := exec.Command(tool, "-q", "-o", p, "-RegionInfoMP="+arg).CombinedOutput(); err != nil {
			t.Fatalf("exiftool: %v %s", err, out)
		}
		out, err := exec.Command(tool, "-j", "-struct", "-XMP-MP:RegionInfoMP", p).Output()
		if err != nil {
			t.Fatal(err)
		}
		var got []struct {
			RegionInfoMP struct {
				Regions []struct{ PersonDisplayName, Rectangle string }
			}
		}
		if err := json.Unmarshal(out, &got); err != nil || len(got) != 1 {
			t.Fatalf("%v %s", err, out)
		}
		packet, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		x, err := ParseXMP(packet)
		if err != nil {
			t.Fatal(err)
		}
		want := got[0].RegionInfoMP.Regions
		if len(x.MicrosoftRegions) != len(want) {
			t.Fatalf("%s: %+v, ExifTool %+v", arg, x.MicrosoftRegions, want)
		}
		for j, w := range want {
			g := x.MicrosoftRegions[j]
			if g.Name != w.PersonDisplayName || g.HasRect != (w.Rectangle != "") {
				t.Errorf("%s %d: %+v, ExifTool %+v", arg, j, g, w)
				continue
			}
			if g.HasRect {
				f := strings.Split(w.Rectangle, ",")
				for k, v := range []float64{g.X, g.Y, g.W, g.H} {
					e, _ := strconv.ParseFloat(strings.TrimSpace(f[k]), 64)
					if math.Abs(e-v) > 1e-9 {
						t.Errorf("%s %d: rectangle %+v, ExifTool %q", arg, j, g, w.Rectangle)
					}
				}
			}
		}
	}
}
