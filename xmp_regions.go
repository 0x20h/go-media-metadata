package mediameta

import (
	"encoding/xml"
	"fmt"
	"math"
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
}

// Dimensions are a width and height, in Unit (MWG: "pixel").
type Dimensions struct {
	W, H float64
	Unit string
}

// Region is one area of an image (an mwg-rs:RegionList item).
type Region struct {
	// Name is who or what it shows, e.g. the person's name.
	Name string
	// Type is "Face", "Pet", "Focus" or "BarCode" (MWG); "" reads and
	// writes as "Face".
	Type string
	// X and Y are the area's centre, W and H its size, each a fraction of
	// the image's width or height (stArea, unit "normalized"), with (0, 0)
	// the top-left corner of the image as it is stored.
	X, Y, W, H float64
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

// regions reads mwg-rs:Regions; nil without one. A region without a
// normalized area that fits the image is left out.
func (d *xmpDoc) regions() *Regions {
	ps := d.props(nsMWGRS, "Regions")
	if len(ps) == 0 || ps[0].elem == nil {
		return nil
	}
	el, in := ps[0].elem, ps[0].in.enter(ps[0].elem)
	r := &Regions{}
	if _, dims, dimsIn, ok := structField(el, in, nsMWGRS, "AppliedToDimensions"); ok && dims != nil {
		ws, _, _, _ := structField(dims, dimsIn, nsStDim, "w")
		hs, _, _, _ := structField(dims, dimsIn, nsStDim, "h")
		r.AppliedTo.Unit, _, _, _ = structField(dims, dimsIn, nsStDim, "unit")
		r.AppliedTo.W, _ = number(ws)
		r.AppliedTo.H, _ = number(hs)
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
			_, area, areaIn, ok := structField(li, lin, nsMWGRS, "Area")
			if !ok || area == nil {
				continue
			}
			unit, _, _, _ := structField(area, areaIn, nsStArea, "unit")
			if unit != "" && unit != "normalized" {
				continue
			}
			vals := [4]float64{}
			good := true
			for i, k := range []string{"x", "y", "w", "h"} {
				s, _, _, _ := structField(area, areaIn, nsStArea, k)
				v, ok := number(s)
				// The centre lies on the image; a size is at most all of it.
				if !ok || v < 0 || v > 1 {
					good = false
				}
				vals[i] = v
			}
			if !good {
				continue
			}
			g.X, g.Y, g.W, g.H = vals[0], vals[1], vals[2], vals[3]
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
		for _, v := range []float64{g.X, g.Y, g.W, g.H} {
			if !(v >= 0 && v <= 1) {
				return fmt.Errorf("mediameta: region %d: %v, %v, %v, %v: not fractions of the image", i, g.X, g.Y, g.W, g.H)
			}
		}
		if !utf8.ValidString(g.Name) || !utf8.ValidString(g.Type) {
			return fmt.Errorf("mediameta: region %d: text that isn't UTF-8", i)
		}
	}
	return nil
}

func formatNumber(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

// setRegions writes mwg-rs:Regions as a whole: the regions given, in the
// MWG's layout. Fields of the old regions this package doesn't model (a
// region's rotation, extensions) aren't kept. No regions remove the
// property.
func (d *xmpDoc) setRegions(r *Regions) bool {
	if r == nil || len(r.List) == 0 {
		return d.remove(nsMWGRS, "Regions")
	}
	if cur := d.regions(); cur != nil && sameRegions(cur, r) {
		return false
	}
	d.replace(nsMWGRS, "Regions", func(p string, in scope) *xmlNode {
		rdf := prefixIn(in, nsRDF)
		attr := func(space, local, v string) xml.Attr {
			return xml.Attr{Name: xml.Name{Space: space, Local: local}, Value: v}
		}
		text := func(space, local, v string) *xmlNode {
			return &xmlNode{kind: elemNode, name: xml.Name{Space: space, Local: local}, kids: []*xmlNode{{kind: textNode, text: []byte(v)}}}
		}
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
		bag := &xmlNode{kind: elemNode, name: xml.Name{Space: rdf, Local: "Bag"}}
		for _, g := range r.List {
			typ := g.Type
			if typ == "" {
				typ = "Face"
			}
			area := &xmlNode{kind: elemNode, name: xml.Name{Space: p, Local: "Area"}, attrs: []xml.Attr{attr(rdf, "parseType", "Resource")}, kids: []*xmlNode{
				text("stArea", "x", formatNumber(g.X)),
				text("stArea", "y", formatNumber(g.Y)),
				text("stArea", "w", formatNumber(g.W)),
				text("stArea", "h", formatNumber(g.H)),
				text("stArea", "unit", "normalized"),
			}}
			kids := []*xmlNode{area, text(p, "Type", typ)}
			if g.Name != "" {
				kids = append(kids, text(p, "Name", g.Name))
			}
			bag.kids = append(bag.kids, &xmlNode{kind: elemNode, name: xml.Name{Space: rdf, Local: "li"}, attrs: []xml.Attr{attr(rdf, "parseType", "Resource")}, kids: kids})
		}
		list := &xmlNode{kind: elemNode, name: xml.Name{Space: p, Local: "RegionList"}, kids: []*xmlNode{bag}}
		return &xmlNode{kind: elemNode, name: xml.Name{Space: p, Local: "Regions"}, attrs: []xml.Attr{
			attr(rdf, "parseType", "Resource"),
			attr(nsXMLNS, "stDim", nsStDim),
			attr(nsXMLNS, "stArea", nsStArea),
		}, kids: []*xmlNode{dims, list}}
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
		if x.Name != y.Name || typ(x.Type) != typ(y.Type) || x.X != y.X || x.Y != y.Y || x.W != y.W || x.H != y.H {
			return false
		}
	}
	return true
}
