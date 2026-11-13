package mediameta

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"slices"
	"strings"
)

// Extended XMP (XMP Specification Part 3, 1.1.3.1): a packet too big for
// one JPEG segment is split in two. The standard part keeps what fits and
// names the extended part, by the MD5 digest of its serialization, in
// xmpNote:HasExtendedXMP; the extended part holds the rest.

const nsXMPNote = "http://ns.adobe.com/xmp/note/"

func init() { preferredPrefix[nsXMPNote] = "xmpNote" }

// extendedGUID is the GUID HasExtendedXMP names in a standard packet: 32
// hexadecimal digits, "" without one.
func (d *xmpDoc) extendedGUID() string {
	ps := d.props(nsXMPNote, "HasExtendedXMP")
	if len(ps) == 0 {
		return ""
	}
	g := ps[0].value()
	if len(g) != 32 {
		return ""
	}
	if _, err := hex.DecodeString(g); err != nil {
		return ""
	}
	return g
}

// mergeXMP is a standard packet with its extended part's rdf:Descriptions
// added to its rdf:RDF, and HasExtendedXMP removed: one packet, as if it
// had never been split.
func mergeXMP(standard, extended []byte) ([]byte, error) {
	sd, err := parseXMPDoc(standard)
	if err != nil {
		return nil, err
	}
	ed, err := parseXMPDoc(extended)
	if err != nil {
		return nil, err
	}
	sd.remove(nsXMPNote, "HasExtendedXMP")
	ds, ss := ed.descriptions()
	for i, desc := range ds {
		declareUsed(desc, ss[i], sd.rdfIn)
		appendIndented(sd.rdf, desc)
	}
	return sd.serialize(0), nil
}

// declareUsed adds to el the namespace declarations the prefixes used in
// it need, where have (the scope el moves into) doesn't bind them as need
// (the scope it comes from) does.
func declareUsed(el *xmlNode, need, have scope) {
	declared := map[string]bool{}
	for _, a := range el.attrs {
		if a.Name.Space == nsXMLNS {
			declared[a.Name.Local] = true
		}
		if a.Name.Space == "" && a.Name.Local == "xmlns" {
			declared[""] = true
		}
	}
	for _, p := range usedPrefixes(el) {
		uri, ok := need[p]
		if !ok || declared[p] || have[p] == uri {
			continue
		}
		if p == "" {
			el.attrs = append(el.attrs, xml.Attr{Name: xml.Name{Local: "xmlns"}, Value: uri})
		} else {
			el.attrs = append(el.attrs, xml.Attr{Name: xml.Name{Space: nsXMLNS, Local: p}, Value: uri})
		}
		declared[p] = true
	}
}

// usedPrefixes are the prefixes el and its descendants use in their names
// ("" for an element in the default namespace), sorted.
func usedPrefixes(el *xmlNode) []string {
	set := map[string]bool{}
	var walk func(n *xmlNode)
	walk = func(n *xmlNode) {
		if n.kind != elemNode {
			return
		}
		if n.name.Space != "xml" {
			set[n.name.Space] = true
		}
		for _, a := range n.attrs {
			if a.Name.Space != "" && a.Name.Space != nsXMLNS && a.Name.Space != "xml" {
				set[a.Name.Space] = true
			}
		}
		for _, k := range n.kids {
			walk(k)
		}
	}
	walk(el)
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	slices.Sort(out)
	return out
}

// splitXMP splits packet so that the standard part's serialization is at
// most limit bytes: extended is nil when it fits as it is. Otherwise the
// largest top-level properties move to the extended part until the rest
// fits, and the standard part names the extended one in HasExtendedXMP.
// A packet whose standard part can't be made to fit is refused
// (ErrUnsupported).
func splitXMP(packet []byte, limit int) (standard, extended []byte, err error) {
	d, err := parseXMPDoc(packet)
	if err != nil {
		return nil, nil, err
	}
	removedNote := d.remove(nsXMPNote, "HasExtendedXMP")
	if !removedNote && len(packet) <= limit {
		return packet, nil, nil
	}
	if s := d.serialize(len(packet)); len(s) <= limit {
		return s, nil, nil
	}

	// The extended part: x:xmpmeta, rdf:RDF and one rdf:Description.
	rdfP := "rdf"
	extIn := scope{"x": nsX, rdfP: nsRDF}
	extDesc := &xmlNode{kind: elemNode, name: xml.Name{Space: rdfP, Local: "Description"},
		attrs: []xml.Attr{{Name: xml.Name{Space: rdfP, Local: "about"}, Value: ""}}}
	rdf := &xmlNode{kind: elemNode, name: xml.Name{Space: rdfP, Local: "RDF"},
		attrs: []xml.Attr{{Name: xml.Name{Space: nsXMLNS, Local: rdfP}, Value: nsRDF}}, kids: []*xmlNode{extDesc}}
	meta := &xmlNode{kind: elemNode, name: xml.Name{Space: "x", Local: "xmpmeta"},
		attrs: []xml.Attr{{Name: xml.Name{Space: nsXMLNS, Local: "x"}, Value: nsX}}, kids: []*xmlNode{rdf}}

	// The movable properties, largest first.
	type movable struct {
		desc *xmlNode
		in   scope
		elem *xmlNode
		attr xml.Attr
		size int
	}
	var ms []movable
	ds, ss := d.descriptions()
	for i, desc := range ds {
		for _, a := range desc.attrs {
			if a.Name.Space == nsXMLNS || a.Name.Space == "" && a.Name.Local == "xmlns" {
				continue
			}
			if uri, _ := ss[i].ns(a.Name, true); uri == nsRDF {
				continue
			}
			ms = append(ms, movable{desc: desc, in: ss[i], attr: a, size: len(qname(a.Name)) + len(a.Value) + 4})
		}
		for _, k := range elements(desc) {
			var b bytes.Buffer
			writeXML(&b, []*xmlNode{k})
			ms = append(ms, movable{desc: desc, in: ss[i], elem: k, size: b.Len()})
		}
	}
	slices.SortStableFunc(ms, func(a, b movable) int { return b.size - a.size })

	placeholder := strings.Repeat("0", 32)
	d.setSimple(nsXMPNote, "HasExtendedXMP", placeholder)
	fits := func() bool { return len(d.serialize(0)) <= limit }
	for _, m := range ms {
		if fits() {
			break
		}
		if m.elem != nil {
			removeKid(m.desc, m.elem)
			declareUsed(m.elem, m.in, extIn)
			appendIndented(extDesc, m.elem)
			continue
		}
		i := slices.IndexFunc(m.desc.attrs, func(a xml.Attr) bool { return a == m.attr })
		if i < 0 {
			continue
		}
		m.desc.attrs = slices.Delete(m.desc.attrs, i, i+1)
		a := m.attr
		uri, _ := m.in.ns(a.Name, true)
		if extIn[a.Name.Space] != uri {
			p := a.Name.Space
			for n := 1; extIn[p] != "" && extIn[p] != uri; n++ {
				p = fmt.Sprintf("%s%d", a.Name.Space, n)
			}
			if extIn[p] == "" {
				extDesc.attrs = append(extDesc.attrs, xml.Attr{Name: xml.Name{Space: nsXMLNS, Local: p}, Value: uri})
				extIn = extIn.enter(&xmlNode{attrs: []xml.Attr{{Name: xml.Name{Space: nsXMLNS, Local: p}, Value: uri}}})
			}
			a.Name.Space = p
		}
		extDesc.attrs = append(extDesc.attrs, a)
	}
	if !fits() {
		return nil, nil, fmt.Errorf("%w: XMP whose standard part doesn't fit in %d bytes", ErrUnsupported, limit)
	}
	var ext bytes.Buffer
	writeXML(&ext, []*xmlNode{meta})
	sum := md5.Sum(ext.Bytes())
	d.setSimple(nsXMPNote, "HasExtendedXMP", strings.ToUpper(hex.EncodeToString(sum[:])))
	return d.serialize(0), ext.Bytes(), nil
}

// padXMP is packet n bytes long, with whitespace added in its padding
// (before the xpacket end instruction) or, without a wrapper, after it:
// XML allows whitespace after the root element.
func padXMP(packet []byte, n int) []byte {
	if len(packet) >= n {
		return packet
	}
	pad := bytes.Repeat([]byte{' '}, n-len(packet))
	if i := bytes.LastIndex(packet, []byte("<?xpacket end")); i >= 0 {
		return slices.Concat(packet[:i], pad, packet[i:])
	}
	return slices.Concat(packet, pad)
}
