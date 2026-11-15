package mediameta

import (
	"encoding/xml"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Face regions as the Metadata Working Group's guidelines (2.0, section
// 5.9) put them in XMP, and the people shown as IPTC's Extension schema
// names them.

const (
	nsMWGRS  = "http://www.metadataworkinggroup.com/schemas/regions/"
	nsStDim  = "http://ns.adobe.com/xap/1.0/sType/Dimensions#"
	nsStArea = "http://ns.adobe.com/xmp/sType/Area#"
	nsIPTCX  = "http://iptc.org/std/Iptc4xmpExt/2008-02-29/"
)

func init() {
	preferredPrefix[nsMWGRS] = "mwg-rs"
	preferredPrefix[nsStDim] = "stDim"
	preferredPrefix[nsStArea] = "stArea"
	preferredPrefix[nsIPTCX] = "Iptc4xmpExt"
}

// Regions are an image's mwg-rs:Regions: the regions, and the image size
// their coordinates were made for.
type Regions struct {
	// AppliedTo is the image size the regions were made for
	// (mwg-rs:AppliedToDimensions): a region's position is a fraction of
	// it. Readers compare it with the image's own size and ignore regions
	// made for another one (an image that was cropped or turned since).
	AppliedTo Dimensions
	List      []Region

	// kept are the fields of mwg-rs:Regions and of its
	// AppliedToDimensions this package doesn't model, as read, written
	// back with them.
	kept, keptDims *keptFields
}

// Dimensions are a width and height, in Unit (MWG: "pixel").
type Dimensions struct {
	W, H float64
	Unit string
}

// Region is one area of an image (an mwg-rs:RegionList item).
//
// A Region read by ParseXMP carries the fields it doesn't model (such as
// mwg-rs:Extensions, which applications put their own data in), and
// EditXMP writes them back with it: change a region's name and keep the
// rest, reorder or drop regions and the fields go with them. A Region made
// anew has none, unless its area is that of a region already in the
// packet, whose fields it then keeps.
type Region struct {
	// Name is who or what it shows, e.g. the person's name.
	Name string
	// Type is "Face", "Pet", "Focus" or "BarCode" (MWG); "" reads and
	// writes as "Face".
	Type string
	// Description is mwg-rs:Description, FocusUsage mwg-rs:FocusUsage
	// ("EvaluatedUsed", "EvaluatedNotUsed", "NotEvaluatedNotUsed") and
	// BarCodeValue mwg-rs:BarCodeValue; "" without.
	Description, FocusUsage, BarCodeValue string
	// X and Y are the area's centre, W and H its size, with (0, 0) the
	// top-left corner of the image as it is stored: fractions of the
	// image's width and height when Unit is "normalized" (or ""), pixels
	// when it is "pixel". D is a circle's diameter (stArea:d), 0 for a
	// rectangle; an area with W, H and D 0 is a point.
	X, Y, W, H, D float64
	Unit          string
	// Rotation is mwg-rs:Rotation, the area's rotation about its centre,
	// as written (radians, by the MWG guidelines); 0 without.
	Rotation float64

	// kept are the fields of the region and of its area this package
	// doesn't model, as read.
	kept, keptArea *keptFields
}

// structFields are the nodes a struct value's fields can be in: the
// property element itself (fields as attributes, or as children with
// rdf:parseType="Resource"), and an rdf:Description inside it.
func structFields(el *xmlNode, in scope) ([]*xmlNode, []scope) {
	nodes, scopes := []*xmlNode{el}, []scope{in}
	for _, k := range elements(el) {
		kin := in.enter(k)
		if uri, _ := kin.ns(k.name, false); uri == nsRDF && k.name.Local == "Description" {
			nodes, scopes = append(nodes, k), append(scopes, kin)
		}
	}
	return nodes, scopes
}

// structField finds a struct field: as an attribute (its value) or an element
// (the element and the scope inside it).
func structField(el *xmlNode, in scope, ns, local string) (value string, sub *xmlNode, subIn scope, ok bool) {
	nodes, scopes := structFields(el, in)
	for i, n := range nodes {
		for _, a := range n.attrs {
			if a.Name.Local == local && a.Name.Space != "" && a.Name.Space != nsXMLNS {
				if uri, _ := scopes[i].ns(a.Name, true); uri == ns {
					return a.Value, nil, nil, true
				}
			}
		}
		for _, k := range elements(n) {
			kin := scopes[i].enter(k)
			if uri, _ := kin.ns(k.name, false); uri == ns && k.name.Local == local {
				return strings.TrimSpace(textOf(k)), k, kin, true
			}
		}
	}
	return "", nil, nil, false
}

func number(s string) (float64, bool) {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return f, err == nil && !math.IsNaN(f) && !math.IsInf(f, 0)
}

// regions reads mwg-rs:Regions; nil without one. A region without an area
// this package takes (normalized, within the image; or in pixels, not
// negative) is left out.
func (d *xmpDoc) regions() *Regions {
	ps := d.props(nsMWGRS, "Regions")
	if len(ps) == 0 || ps[0].elem == nil {
		return nil
	}
	el, in := ps[0].elem, ps[0].in.enter(ps[0].elem)
	r := &Regions{kept: keepFields(el, in, knownRegionsField)}
	if _, dims, dimsIn, ok := structField(el, in, nsMWGRS, "AppliedToDimensions"); ok && dims != nil {
		ws, _, _, _ := structField(dims, dimsIn, nsStDim, "w")
		hs, _, _, _ := structField(dims, dimsIn, nsStDim, "h")
		r.AppliedTo.Unit, _, _, _ = structField(dims, dimsIn, nsStDim, "unit")
		r.AppliedTo.W, _ = number(ws)
		r.AppliedTo.H, _ = number(hs)
		r.keptDims = keepFields(dims, dimsIn, knownDimsField)
	}
	_, list, listIn, ok := structField(el, in, nsMWGRS, "RegionList")
	if !ok || list == nil {
		return r
	}
	for _, c := range elements(list) {
		cin := listIn.enter(c)
		if uri, _ := cin.ns(c.name, false); uri != nsRDF || (c.name.Local != "Bag" && c.name.Local != "Seq") {
			continue
		}
		for _, li := range items(c) {
			lin := cin.enter(li)
			var g Region
			g.Name, _, _, _ = structField(li, lin, nsMWGRS, "Name")
			g.Type, _, _, _ = structField(li, lin, nsMWGRS, "Type")
			g.Description, _, _, _ = structField(li, lin, nsMWGRS, "Description")
			g.FocusUsage, _, _, _ = structField(li, lin, nsMWGRS, "FocusUsage")
			g.BarCodeValue, _, _, _ = structField(li, lin, nsMWGRS, "BarCodeValue")
			if s, _, _, ok := structField(li, lin, nsMWGRS, "Rotation"); ok {
				g.Rotation, _ = number(s)
			}
			_, area, areaIn, ok := structField(li, lin, nsMWGRS, "Area")
			if !ok || area == nil {
				continue
			}
			g.Unit, _, _, _ = structField(area, areaIn, nsStArea, "unit")
			var vals [5]float64
			good := true
			for i, k := range []string{"x", "y", "w", "h", "d"} {
				s, _, _, has := structField(area, areaIn, nsStArea, k)
				if !has && k != "x" && k != "y" {
					continue // a circle has no w and h, a rectangle no d, a point neither
				}
				v, ok := number(s)
				if !ok || !areaValueOK(g.Unit, v) {
					good = false
				}
				vals[i] = v
			}
			if !good {
				continue
			}
			g.X, g.Y, g.W, g.H, g.D = vals[0], vals[1], vals[2], vals[3], vals[4]
			g.kept = keepFields(li, lin, knownRegionField)
			g.keptArea = keepFields(area, areaIn, knownAreaField)
			r.List = append(r.List, g)
		}
		break
	}
	return r
}

// stringBag reads an unordered array of text (rdf:Bag or rdf:Seq), or a
// simple value where one was written.
func (d *xmpDoc) stringBag(ns, local string) []string {
	ps := d.props(ns, local)
	if len(ps) == 0 {
		return nil
	}
	var out []string
	if c := ps[0].container(); c != nil {
		for _, li := range items(c) {
			if v := strings.TrimSpace(textOf(li)); v != "" {
				out = append(out, v)
			}
		}
	} else if v := ps[0].value(); v != "" {
		out = append(out, v)
	}
	return out
}

func checkRegions(r *Regions) error {
	if r == nil || len(r.List) == 0 {
		return nil
	}
	if !(r.AppliedTo.W > 0 && r.AppliedTo.H > 0) || math.IsInf(r.AppliedTo.W, 0) || math.IsInf(r.AppliedTo.H, 0) {
		return fmt.Errorf("mediameta: regions without the image size they apply to")
	}
	for i, g := range r.List {
		if g.Unit != "" && g.Unit != "normalized" && g.Unit != "pixel" {
			return fmt.Errorf("mediameta: region %d: unit %q, not normalized or pixel", i, g.Unit)
		}
		for _, v := range []float64{g.X, g.Y, g.W, g.H, g.D} {
			if !areaValueOK(g.Unit, v) {
				return fmt.Errorf("mediameta: region %d: %v, %v, %v, %v, %v: not an area of the image in %q", i, g.X, g.Y, g.W, g.H, g.D, g.Unit)
			}
		}
		if math.IsNaN(g.Rotation) || math.IsInf(g.Rotation, 0) {
			return fmt.Errorf("mediameta: region %d: rotation %v", i, g.Rotation)
		}
		for _, s := range []string{g.Name, g.Type, g.Description, g.FocusUsage, g.BarCodeValue} {
			if !utf8.ValidString(s) {
				return fmt.Errorf("mediameta: region %d: text that isn't UTF-8", i)
			}
		}
	}
	return nil
}

// areaValueOK tells whether v can be an area's coordinate or size: a
// fraction of the image when normalized, a pixel count not negative in
// pixels.
func areaValueOK(unit string, v float64) bool {
	if unit == "pixel" {
		return v >= 0 && !math.IsInf(v, 0)
	}
	return v >= 0 && v <= 1
}

func formatNumber(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

// setRegions writes mwg-rs:Regions as a whole: the regions given, in the
// MWG's layout, each with the fields it carries that this package doesn't
// model (see Region). No regions remove the property.
func (d *xmpDoc) setRegions(r *Regions) bool {
	if r == nil || len(r.List) == 0 {
		return d.remove(nsMWGRS, "Regions")
	}
	cur := d.regions()
	if cur != nil && sameRegions(cur, r) {
		return false
	}
	// What the old packet kept, for what the new regions don't carry: the
	// structure's, and a region's by its area.
	if cur != nil {
		cp := *r
		if cp.kept == nil && cp.keptDims == nil {
			cp.kept, cp.keptDims = cur.kept, cur.keptDims
		}
		cp.List = slices.Clone(r.List)
		for i, g := range cp.List {
			if g.kept != nil || g.keptArea != nil {
				continue
			}
			for _, old := range cur.List {
				if sameArea(old, g) {
					cp.List[i].kept, cp.List[i].keptArea = old.kept, old.keptArea
					break
				}
			}
		}
		r = &cp
	}
	d.replace(nsMWGRS, "Regions", func(p string, in scope) *xmlNode {
		rdf := prefixIn(in, nsRDF)
		attr := func(space, local, v string) xml.Attr {
			return xml.Attr{Name: xml.Name{Space: space, Local: local}, Value: v}
		}
		text := func(space, local, v string) *xmlNode {
			return &xmlNode{kind: elemNode, name: xml.Name{Space: space, Local: local}, kids: []*xmlNode{{kind: textNode, text: []byte(v)}}}
		}
		// The prefixes the new elements use, which kept fields mustn't
		// redeclare.
		ours := map[string]string{p: nsMWGRS, rdf: nsRDF, "stDim": nsStDim, "stArea": nsStArea}
		unit := r.AppliedTo.Unit
		if unit == "" {
			unit = "pixel"
		}
		dims := &xmlNode{kind: elemNode, name: xml.Name{Space: p, Local: "AppliedToDimensions"}, attrs: []xml.Attr{
			attr(rdf, "parseType", "Resource"),
		}, kids: []*xmlNode{
			text("stDim", "w", formatNumber(r.AppliedTo.W)),
			text("stDim", "h", formatNumber(r.AppliedTo.H)),
			text("stDim", "unit", unit),
		}}
		r.keptDims.writeInto(dims, ours)
		bag := &xmlNode{kind: elemNode, name: xml.Name{Space: rdf, Local: "Bag"}}
		for _, g := range r.List {
			typ := g.Type
			if typ == "" {
				typ = "Face"
			}
			unit := g.Unit
			if unit == "" {
				unit = "normalized"
			}
			area := &xmlNode{kind: elemNode, name: xml.Name{Space: p, Local: "Area"}, attrs: []xml.Attr{attr(rdf, "parseType", "Resource")}, kids: []*xmlNode{
				text("stArea", "x", formatNumber(g.X)),
				text("stArea", "y", formatNumber(g.Y)),
			}}
			// A circle (d) and a point have no width and height.
			if g.W != 0 || g.H != 0 {
				area.kids = append(area.kids, text("stArea", "w", formatNumber(g.W)), text("stArea", "h", formatNumber(g.H)))
			}
			if g.D != 0 {
				area.kids = append(area.kids, text("stArea", "d", formatNumber(g.D)))
			}
			area.kids = append(area.kids, text("stArea", "unit", unit))
			g.keptArea.writeInto(area, ours)
			kids := []*xmlNode{area, text(p, "Type", typ)}
			for _, f := range []struct{ local, v string }{
				{"Name", g.Name}, {"Description", g.Description}, {"FocusUsage", g.FocusUsage}, {"BarCodeValue", g.BarCodeValue},
			} {
				if f.v != "" {
					kids = append(kids, text(p, f.local, f.v))
				}
			}
			if g.Rotation != 0 {
				kids = append(kids, text(p, "Rotation", formatNumber(g.Rotation)))
			}
			li := &xmlNode{kind: elemNode, name: xml.Name{Space: rdf, Local: "li"}, attrs: []xml.Attr{attr(rdf, "parseType", "Resource")}, kids: kids}
			g.kept.writeInto(li, ours)
			bag.kids = append(bag.kids, li)
		}
		list := &xmlNode{kind: elemNode, name: xml.Name{Space: p, Local: "RegionList"}, kids: []*xmlNode{bag}}
		regions := &xmlNode{kind: elemNode, name: xml.Name{Space: p, Local: "Regions"}, attrs: []xml.Attr{
			attr(rdf, "parseType", "Resource"),
			attr(nsXMLNS, "stDim", nsStDim),
			attr(nsXMLNS, "stArea", nsStArea),
		}, kids: []*xmlNode{dims, list}}
		r.kept.writeInto(regions, ours)
		return regions
	})
	return true
}

func sameRegions(a, b *Regions) bool {
	unit := func(u string) string {
		if u == "" {
			return "pixel"
		}
		return u
	}
	typ := func(t string) string {
		if t == "" {
			return "Face"
		}
		return t
	}
	if a.AppliedTo.W != b.AppliedTo.W || a.AppliedTo.H != b.AppliedTo.H || unit(a.AppliedTo.Unit) != unit(b.AppliedTo.Unit) || len(a.List) != len(b.List) {
		return false
	}
	for i := range a.List {
		x, y := a.List[i], b.List[i]
		if x.Name != y.Name || typ(x.Type) != typ(y.Type) || x.Description != y.Description || x.FocusUsage != y.FocusUsage ||
			x.BarCodeValue != y.BarCodeValue || x.Rotation != y.Rotation || !sameArea(x, y) {
			return false
		}
	}
	return true
}

// sameArea tells whether two regions have the same area.
func sameArea(a, b Region) bool {
	unit := func(u string) string {
		if u == "" {
			return "normalized"
		}
		return u
	}
	return a.X == b.X && a.Y == b.Y && a.W == b.W && a.H == b.H && a.D == b.D && unit(a.Unit) == unit(b.Unit)
}
