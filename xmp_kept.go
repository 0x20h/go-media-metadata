package mediameta

import (
	"encoding/xml"
	"slices"
)

// keptFields are the fields of an XMP struct value this package doesn't
// model, as read, to be written back when it writes the struct anew: each
// field as an element, copied, and the namespace declarations they need
// from outside themselves.
type keptFields struct {
	nodes []*xmlNode
	decls map[string]string // prefix ("" for the default namespace) → namespace
}

// Fields modelled in each struct of mwg-rs:Regions; the others are kept.
func knownRegionsField(ns, local string) bool {
	return ns == nsMWGRS && (local == "AppliedToDimensions" || local == "RegionList")
}

func knownDimsField(ns, local string) bool {
	return ns == nsStDim && (local == "w" || local == "h" || local == "unit")
}

func knownRegionField(ns, local string) bool {
	return ns == nsMWGRS && slices.Contains([]string{"Name", "Type", "Description", "FocusUsage", "BarCodeValue", "Rotation", "Area"}, local)
}

func knownAreaField(ns, local string) bool {
	return ns == nsStArea && slices.Contains([]string{"x", "y", "w", "h", "d", "unit"}, local)
}

// keepFields collects the fields of the struct value in el (in is the
// scope inside it) that known doesn't take: attributes, which become
// elements, and elements, wherever structFields finds fields. Nil without
// any.
func keepFields(el *xmlNode, in scope, known func(ns, local string) bool) *keptFields {
	var k *keptFields
	add := func(n *xmlNode, outside scope) {
		if k == nil {
			k = &keptFields{decls: map[string]string{}}
		}
		k.nodes = append(k.nodes, n)
		// The prefixes n and what's in it use, bound outside it.
		var walk func(n *xmlNode, s scope)
		walk = func(n *xmlNode, s scope) {
			inner := s.enter(n)
			need := func(name xml.Name, attr bool) {
				if name.Space == "xml" || name.Space == nsXMLNS || attr && name.Space == "" {
					return
				}
				if uri, ok := inner.ns(name, attr); ok && outside[name.Space] == uri {
					k.decls[name.Space] = uri
				}
			}
			need(n.name, false)
			for _, a := range n.attrs {
				if !(a.Name.Space == "" && a.Name.Local == "xmlns") {
					need(a.Name, true)
				}
			}
			for _, kid := range elements(n) {
				walk(kid, inner)
			}
		}
		walk(n, outside)
	}
	nodes, scopes := structFields(el, in)
	for i, n := range nodes {
		s := scopes[i]
		for _, a := range n.attrs {
			if a.Name.Space == nsXMLNS || a.Name.Space == "" {
				continue
			}
			uri, ok := s.ns(a.Name, true)
			if !ok || uri == nsRDF || uri == xmlNS || known(uri, a.Name.Local) {
				continue
			}
			// An attribute field becomes an element: rdf:parseType
			// "Resource", which the struct is written with, allows no
			// property attributes.
			add(&xmlNode{kind: elemNode, name: a.Name, kids: []*xmlNode{{kind: textNode, text: []byte(a.Value)}}}, s)
		}
		for _, kid := range elements(n) {
			uri, ok := s.enter(kid).ns(kid.name, false)
			if !ok || uri == nsRDF && kid.name.Local == "Description" || known(uri, kid.name.Local) {
				continue
			}
			add(copyNode(kid), s)
		}
	}
	return k
}

// writeInto appends the kept fields to el, declaring on it the namespaces
// they need. A field that would need a prefix the new elements use (ours)
// for another namespace is left out.
func (k *keptFields) writeInto(el *xmlNode, ours map[string]string) {
	if k == nil {
		return
	}
	prefixes := make([]string, 0, len(k.decls))
	for p := range k.decls {
		prefixes = append(prefixes, p)
	}
	slices.Sort(prefixes)
	for _, p := range prefixes {
		uri := k.decls[p]
		if u, taken := ours[p]; taken {
			if u != uri {
				return // see above: none of them, the declarations being shared
			}
			continue
		}
		name := xml.Name{Space: nsXMLNS, Local: p}
		if p == "" {
			name = xml.Name{Local: "xmlns"}
		}
		el.attrs = append(el.attrs, xml.Attr{Name: name, Value: uri})
	}
	for _, n := range k.nodes {
		el.kids = append(el.kids, copyNode(n))
	}
}

// copyNode is a deep copy of n.
func copyNode(n *xmlNode) *xmlNode {
	c := *n
	c.attrs = slices.Clone(n.attrs)
	c.text = slices.Clone(n.text)
	c.kids = make([]*xmlNode, len(n.kids))
	for i, k := range n.kids {
		c.kids[i] = copyNode(k)
	}
	return &c
}
