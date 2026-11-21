package mediameta

import "strings"

// Microsoft Photo's people regions: Windows Photo Gallery's and Windows
// Live Photo Gallery's people tags, as ExifTool's Microsoft.pm and XMP.pm
// define them. MP:RegionInfo is a struct (MPRI) with a Bag of Regions,
// each a struct (MPReg) with PersonDisplayName and Rectangle, "x, y, w,
// h" as fractions of the image, (0, 0) its top-left corner. A region may
// have a name and no rectangle: a person tagged without a box.
const (
	nsMP    = "http://ns.microsoft.com/photo/1.2/"
	nsMPRI  = "http://ns.microsoft.com/photo/1.2/t/RegionInfo#"
	nsMPReg = "http://ns.microsoft.com/photo/1.2/t/Region#"
)

func init() {
	preferredPrefix[nsMP] = "MP"
	preferredPrefix[nsMPRI] = "MPRI"
	preferredPrefix[nsMPReg] = "MPReg"
}

// MicrosoftRegion is one of Microsoft Photo's people regions
// (MPReg:PersonDisplayName and MPReg:Rectangle).
type MicrosoftRegion struct {
	// Name is MPReg:PersonDisplayName, "" without one.
	Name string
	// HasRect is whether the region has a rectangle this package takes:
	// four numbers, each a fraction from 0 to 1, the box within the image.
	// X and Y are its top-left corner, W and H its size, as fractions of
	// the image's width and height, (0, 0) the image's top-left corner as
	// stored.
	HasRect    bool
	X, Y, W, H float64
}

// microsoftRegions reads MP:RegionInfo's regions; nil without any. A
// region without a name or a rectangle this package takes is left out; a
// rectangle that isn't four fractions within the image is ignored, the
// name kept.
func (d *xmpDoc) microsoftRegions() []MicrosoftRegion {
	ps := d.props(nsMP, "RegionInfo")
	if len(ps) == 0 || ps[0].elem == nil {
		return nil
	}
	el, in := ps[0].elem, ps[0].in.enter(ps[0].elem)
	_, regions, regionsIn, ok := structField(el, in, nsMPRI, "Regions")
	if !ok || regions == nil {
		return nil
	}
	var out []MicrosoftRegion
	for _, c := range elements(regions) {
		cin := regionsIn.enter(c)
		if uri, _ := cin.ns(c.name, false); uri != nsRDF || (c.name.Local != "Bag" && c.name.Local != "Seq") {
			continue
		}
		for _, li := range items(c) {
			lin := cin.enter(li)
			var g MicrosoftRegion
			g.Name, _, _, _ = structField(li, lin, nsMPReg, "PersonDisplayName")
			if s, _, _, ok := structField(li, lin, nsMPReg, "Rectangle"); ok {
				g.X, g.Y, g.W, g.H, g.HasRect = parseMPRectangle(s)
			}
			if g.Name != "" || g.HasRect {
				out = append(out, g)
			}
		}
		break
	}
	return out
}

// parseMPRectangle reads "x, y, w, h": four numbers, each from 0 to 1,
// the box within the image.
func parseMPRectangle(s string) (x, y, w, h float64, ok bool) {
	parts := strings.Split(s, ",")
	if len(parts) != 4 {
		return 0, 0, 0, 0, false
	}
	var v [4]float64
	for i, p := range parts {
		f, good := number(p)
		if !good || f < 0 || f > 1 {
			return 0, 0, 0, 0, false
		}
		v[i] = f
	}
	const eps = 1e-6
	if v[0]+v[2] > 1+eps || v[1]+v[3] > 1+eps {
		return 0, 0, 0, 0, false
	}
	return v[0], v[1], v[2], v[3], true
}
