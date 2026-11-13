package mediameta

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// XMP is what this package reads from an XMP packet (ISO 16684-1; Adobe
// XMP Specification Part 1): the fields a photo library shows and
// searches. A field the packet doesn't have reads as its zero value.
type XMP struct {
	// Title is dc:title and Description dc:description: their default
	// language ("x-default"), else their first.
	Title, Description string
	// Keywords are dc:subject's items, in order.
	Keywords []string
	// Rating is xmp:Rating, -1 (rejected) or 0 to 5, when HasRating. A
	// rating that isn't one of those whole numbers reads as none.
	Rating    int
	HasRating bool
	// PersonInImage are the people shown (Iptc4xmpExt:PersonInImage).
	PersonInImage []string
	// Regions are the image's face and other regions (mwg-rs:Regions),
	// nil without any; see Regions.
	Regions *Regions
}

// XMPEdit is a change to an XMP packet. A nil field leaves that property
// as it is.
type XMPEdit struct {
	// Title and Description set dc:title and dc:description's default
	// language, keeping their other languages; "" removes the property.
	Title, Description *string
	// Keywords sets dc:subject; an empty list removes it.
	Keywords *[]string
	// Rating sets xmp:Rating (-1 to 5); RemoveRating removes it.
	Rating       *int
	RemoveRating bool
	// PersonInImage sets Iptc4xmpExt:PersonInImage; an empty list removes
	// it.
	PersonInImage *[]string
	// Regions sets mwg-rs:Regions as a whole; one without regions removes
	// it. Fields of the old regions that Region doesn't model (a region's
	// rotation, extensions) aren't kept.
	Regions *Regions
}

// Namespaces of the properties read and written.
const (
	nsRDF   = "http://www.w3.org/1999/02/22-rdf-syntax-ns#"
	nsX     = "adobe:ns:meta/"
	nsDC    = "http://purl.org/dc/elements/1.1/"
	nsXMP   = "http://ns.adobe.com/xap/1.0/"
	nsXMLNS = "xmlns"
)

// preferredPrefix is the prefix a namespace gets when this package has to
// declare it.
var preferredPrefix = map[string]string{nsRDF: "rdf", nsDC: "dc", nsXMP: "xmp", nsX: "x"}

// maxXMP caps the packets parsed: real ones are kilobytes, an extended
// one a few megabytes.
const maxXMP = 16 << 20

// xmpDoc is a parsed packet.
type xmpDoc struct {
	nodes []*xmlNode
	rdf   *xmlNode // the rdf:RDF element
	rdfIn scope    // the scope inside rdf:RDF
}

func parseXMPDoc(packet []byte) (*xmpDoc, error) {
	if len(packet) > maxXMP {
		return nil, fmt.Errorf("%w: XMP of %d bytes", ErrUnsupported, len(packet))
	}
	if !utf8.Valid(packet) {
		return nil, fmt.Errorf("%w: XMP that isn't UTF-8", ErrMalformed)
	}
	nodes, err := parseXML(packet)
	if err != nil {
		return nil, err
	}
	doc := &xmpDoc{nodes: nodes}
	// rdf:RDF: at the top, or in x:xmpmeta (or x:xapmeta, its old name).
	var find func(ns []*xmlNode, s scope, depth int) bool
	find = func(ns []*xmlNode, s scope, depth int) bool {
		for _, n := range ns {
			if n.kind != elemNode {
				continue
			}
			in := s.enter(n)
			uri, _ := in.ns(n.name, false)
			switch {
			case uri == nsRDF && n.name.Local == "RDF":
				doc.rdf, doc.rdfIn = n, in
				return true
			case depth == 0 && uri == nsX && (n.name.Local == "xmpmeta" || n.name.Local == "xapmeta"):
				if find(n.kids, in, 1) {
					return true
				}
			}
		}
		return false
	}
	if !find(nodes, scope{}, 0) {
		return nil, fmt.Errorf("%w: XMP without rdf:RDF", ErrMalformed)
	}
	return doc, nil
}

// prop is one top-level property of an rdf:Description: an element child,
// or an attribute (attr ≥ 0).
type prop struct {
	desc *xmlNode
	in   scope // inside desc
	elem *xmlNode
	attr int
}

// descriptions are the rdf:Description elements with the scope inside
// each.
func (d *xmpDoc) descriptions() ([]*xmlNode, []scope) {
	var ds []*xmlNode
	var ss []scope
	for _, n := range elements(d.rdf) {
		in := d.rdfIn.enter(n)
		if uri, _ := in.ns(n.name, false); uri == nsRDF && n.name.Local == "Description" {
			ds = append(ds, n)
			ss = append(ss, in)
		}
	}
	return ds, ss
}

// props are the occurrences of the property ns:local, in document order.
func (d *xmpDoc) props(ns, local string) []prop {
	var out []prop
	ds, ss := d.descriptions()
	for i, desc := range ds {
		for j, a := range desc.attrs {
			if a.Name.Local != local || a.Name.Space == "" || a.Name.Space == nsXMLNS {
				continue
			}
			if uri, _ := ss[i].ns(a.Name, true); uri == ns {
				out = append(out, prop{desc: desc, in: ss[i], attr: j})
			}
		}
		for _, k := range elements(desc) {
			if k.name.Local != local {
				continue
			}
			if uri, _ := ss[i].enter(k).ns(k.name, false); uri == ns {
				out = append(out, prop{desc: desc, in: ss[i], elem: k, attr: -1})
			}
		}
	}
	return out
}

// value is a simple property's value.
func (p prop) value() string {
	if p.elem == nil {
		return p.desc.attrs[p.attr].Value
	}
	// <ns:p rdf:value="…"/> or <ns:p rdf:resource="…"/> aren't read here.
	return strings.TrimSpace(textOf(p.elem))
}

// container is the rdf:Alt, rdf:Bag or rdf:Seq in a property element,
// nil without one.
func (p prop) container() *xmlNode {
	if p.elem == nil {
		return nil
	}
	in := p.in.enter(p.elem)
	for _, k := range elements(p.elem) {
		if uri, _ := in.enter(k).ns(k.name, false); uri == nsRDF && (k.name.Local == "Alt" || k.name.Local == "Bag" || k.name.Local == "Seq") {
			return k
		}
	}
	return nil
}

// items are a container's rdf:li elements.
func items(c *xmlNode) []*xmlNode {
	var out []*xmlNode
	for _, k := range elements(c) {
		if k.name.Local == "li" {
			out = append(out, k)
		}
	}
	return out
}

func langOf(li *xmlNode) string {
	for _, a := range li.attrs {
		if a.Name.Space == "xml" && a.Name.Local == "lang" {
			return a.Value
		}
	}
	return ""
}

// langAlt reads a language alternative: the x-default item, else the
// first. A simple value (which some writers use) is read as it is.
func (d *xmpDoc) langAlt(ns, local string) string {
	for _, p := range d.props(ns, local) {
		c := p.container()
		if c == nil {
			if v := p.value(); v != "" {
				return v
			}
			continue
		}
		lis := items(c)
		for _, li := range lis {
			if strings.EqualFold(langOf(li), "x-default") {
				return textOf(li)
			}
		}
		if len(lis) > 0 {
			return textOf(lis[0])
		}
	}
	return ""
}

// ParseXMP reads the fields of XMP from an XMP packet: an x:xmpmeta
// element, an rdf:RDF one, or either in an xpacket wrapper. Properties in
// any of the packet's rdf:Description elements count; the first
// occurrence of each wins.
func ParseXMP(packet []byte) (XMP, error) {
	d, err := parseXMPDoc(packet)
	if err != nil {
		return XMP{}, err
	}
	return d.fields(), nil
}

func (d *xmpDoc) fields() XMP {
	var x XMP
	x.Title = d.langAlt(nsDC, "title")
	x.Description = d.langAlt(nsDC, "description")
	x.Keywords = d.stringBag(nsDC, "subject")
	x.PersonInImage = d.stringBag(nsIPTCX, "PersonInImage")
	x.Regions = d.regions()
	if ps := d.props(nsXMP, "Rating"); len(ps) > 0 {
		f, err := strconv.ParseFloat(ps[0].value(), 64)
		if err == nil && f == math.Trunc(f) && f >= -1 && f <= 5 {
			x.Rating, x.HasRating = int(f), true
		}
	}
	return x
}

// EditXMP returns packet with edit applied. Everything else in it stays:
// other properties, namespaces as declared, other languages of a title,
// comments, whitespace between elements. What XML doesn't tell apart is
// written one way: attributes on one line, separated by a space, in
// double quotes; text escaped minimally. A packet the edit doesn't change
// comes back as it was, byte for byte. A property set where it is (in whichever
// rdf:Description holds it) stays there; a new one goes in the first
// rdf:Description, with its namespace declared there when it isn't in
// scope.
//
// An xpacket wrapper keeps the packet's length where the edit fits in its
// padding, so a writer can put it back in place; a packet that grows past
// it gets 2 KB of new padding. With an empty packet, EditXMP makes a new
// one holding the edit.
func EditXMP(packet []byte, edit XMPEdit) ([]byte, error) {
	if r := edit.Rating; r != nil && (*r < -1 || *r > 5) {
		return nil, fmt.Errorf("mediameta: rating %d: not -1 to 5", *r)
	}
	if edit.Rating != nil && edit.RemoveRating {
		return nil, fmt.Errorf("mediameta: rating both set and removed")
	}
	for _, s := range []*string{edit.Title, edit.Description} {
		if s != nil && !utf8.ValidString(*s) {
			return nil, fmt.Errorf("mediameta: text that isn't UTF-8")
		}
	}
	if edit.Keywords != nil {
		for _, k := range *edit.Keywords {
			if !utf8.ValidString(k) {
				return nil, fmt.Errorf("mediameta: a keyword that isn't UTF-8")
			}
		}
	}
	if edit.PersonInImage != nil {
		for _, k := range *edit.PersonInImage {
			if !utf8.ValidString(k) {
				return nil, fmt.Errorf("mediameta: a name that isn't UTF-8")
			}
		}
	}
	if err := checkRegions(edit.Regions); err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(packet)) == 0 {
		packet = newXMPPacket()
	}
	d, err := parseXMPDoc(packet)
	if err != nil {
		return nil, err
	}
	changed := false
	if edit.Title != nil {
		changed = d.setLangAlt(nsDC, "title", *edit.Title) || changed
	}
	if edit.Description != nil {
		changed = d.setLangAlt(nsDC, "description", *edit.Description) || changed
	}
	if edit.Keywords != nil {
		changed = d.setBag(nsDC, "subject", *edit.Keywords) || changed
	}
	if edit.Rating != nil {
		changed = d.setSimple(nsXMP, "Rating", strconv.Itoa(*edit.Rating)) || changed
	}
	if edit.RemoveRating {
		changed = d.remove(nsXMP, "Rating") || changed
	}
	if edit.PersonInImage != nil {
		changed = d.setBag(nsIPTCX, "PersonInImage", *edit.PersonInImage) || changed
	}
	if edit.Regions != nil {
		changed = d.setRegions(edit.Regions) || changed
	}
	if !changed {
		return bytes.Clone(packet), nil
	}
	return d.serialize(len(packet)), nil
}

// newXMPPacket is an empty packet in an xpacket wrapper.
func newXMPPacket() []byte {
	return []byte("<?xpacket begin=\"\uFEFF\" id=\"W5M0MpCehiHzreSzNTczkc9d\"?>\n" +
		"<x:xmpmeta xmlns:x=\"adobe:ns:meta/\">\n" +
		" <rdf:RDF xmlns:rdf=\"http://www.w3.org/1999/02/22-rdf-syntax-ns#\">\n" +
		"  <rdf:Description rdf:about=\"\"/>\n" +
		" </rdf:RDF>\n" +
		"</x:xmpmeta>\n" +
		"<?xpacket end=\"w\"?>")
}

// xmpPadding is the padding a packet gets when it outgrows its own: room
// for later edits in place (XMP Part 1 7.3.3 suggests 2 KB to 4 KB).
const xmpPadding = 2048

// serialize writes the document. With an xpacket end instruction, the
// whitespace before it is the padding: sized so that the packet is
// oldLen bytes where it fits, else xmpPadding bytes.
func (d *xmpDoc) serialize(oldLen int) []byte {
	end := -1
	for i, n := range d.nodes {
		if n.kind == procInstNode && n.target == "xpacket" && bytes.Contains(n.text, []byte("end=")) {
			end = i
		}
	}
	var buf bytes.Buffer
	if end < 0 {
		writeXML(&buf, d.nodes)
		return buf.Bytes()
	}
	// The padding: the whitespace text nodes right before the end.
	start := end
	for start > 0 && d.nodes[start-1].kind == textNode && len(bytes.TrimSpace(d.nodes[start-1].text)) == 0 {
		start--
	}
	writeXML(&buf, d.nodes[:start])
	var tail bytes.Buffer
	writeXML(&tail, d.nodes[end:])
	pad := oldLen - buf.Len() - tail.Len()
	if pad < 1 {
		pad = xmpPadding
	}
	buf.Write(padding(pad))
	buf.Write(tail.Bytes())
	return buf.Bytes()
}

// padding is n bytes of whitespace: a newline first, then lines of 99
// spaces and a newline, as XMP writers pad.
func padding(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = ' '
		if i%100 == 0 {
			b[i] = '\n'
		}
	}
	return b
}

// remove takes out every occurrence of ns:local; it reports whether there
// was one.
func (d *xmpDoc) remove(ns, local string) bool {
	ps := d.props(ns, local)
	d.removeAll(ps)
	return len(ps) > 0
}

// removeKid removes element k from n, with the whitespace before it, so
// that the indentation of what's left is as it was.
func removeKid(n, k *xmlNode) {
	i := slices.Index(n.kids, k)
	if i < 0 {
		return
	}
	from := i
	if i > 0 && n.kids[i-1].kind == textNode && len(bytes.TrimSpace(n.kids[i-1].text)) == 0 {
		from = i - 1
	}
	n.kids = slices.Delete(n.kids, from, i+1)
	if len(elements(n)) == 0 {
		// Only whitespace left: an empty element.
		n.kids = nil
		n.empty = true
	}
}

// setSimple sets a simple property: where it is, or as a new element. It
// reports whether anything changed.
func (d *xmpDoc) setSimple(ns, local, v string) bool {
	ps := d.props(ns, local)
	if len(ps) > 0 && ps[0].container() == nil && (ps[0].elem == nil || len(elements(ps[0].elem)) == 0) {
		p := ps[0]
		if p.value() == v && len(ps) == 1 {
			return false
		}
		d.removeAll(ps[1:])
		if p.elem == nil {
			p.desc.attrs[p.attr].Value = v
		} else {
			p.elem.kids = []*xmlNode{{kind: textNode, text: []byte(v)}}
			p.elem.empty = false
		}
		return true
	}
	d.replace(ns, local, func(prefix string, in scope) *xmlNode {
		return &xmlNode{kind: elemNode, name: xml.Name{Space: prefix, Local: local}, kids: []*xmlNode{{kind: textNode, text: []byte(v)}}}
	})
	return true
}

// removeAll removes the occurrences ps: attributes, then elements.
func (d *xmpDoc) removeAll(ps []prop) {
	d.removeAttrs(ps)
	for _, p := range ps {
		if p.elem != nil {
			removeKid(p.desc, p.elem)
		}
	}
}

// setLangAlt sets a language alternative's x-default item, keeping the
// other languages; "" removes the property.
func (d *xmpDoc) setLangAlt(ns, local, v string) bool {
	if v == "" {
		return d.remove(ns, local)
	}
	ps := d.props(ns, local)
	if len(ps) == 1 {
		if c := ps[0].container(); c != nil && c.name.Local == "Alt" {
			lis := items(c)
			for _, li := range lis {
				if strings.EqualFold(langOf(li), "x-default") {
					if textOf(li) == v && len(elements(li)) == 0 {
						return false
					}
					li.kids = []*xmlNode{{kind: textNode, text: []byte(v)}}
					li.empty = false
					return true
				}
			}
			// No x-default: it goes first, as XMP requires.
			cin := ps[0].in.enter(ps[0].elem).enter(c)
			li := d.newLi(cin, v)
			li.attrs = []xml.Attr{{Name: xml.Name{Space: "xml", Local: "lang"}, Value: "x-default"}}
			insertFirst(c, li)
			return true
		}
	}
	d.replace(ns, local, func(prefix string, in scope) *xmlNode {
		alt := d.newRDF(in, "Alt")
		li := d.newLi(in, v)
		li.attrs = []xml.Attr{{Name: xml.Name{Space: "xml", Local: "lang"}, Value: "x-default"}}
		alt.kids = []*xmlNode{li}
		return &xmlNode{kind: elemNode, name: xml.Name{Space: prefix, Local: local}, kids: []*xmlNode{alt}}
	})
	return true
}

// setBag sets an unordered array; an empty list removes it.
func (d *xmpDoc) setBag(ns, local string, vs []string) bool {
	if len(vs) == 0 {
		return d.remove(ns, local)
	}
	ps := d.props(ns, local)
	if len(ps) == 1 {
		if c := ps[0].container(); c != nil {
			var cur []string
			for _, li := range items(c) {
				cur = append(cur, strings.TrimSpace(textOf(li)))
			}
			if slices.Equal(cur, vs) {
				return false
			}
		}
	}
	d.replace(ns, local, func(prefix string, in scope) *xmlNode {
		bag := d.newRDF(in, "Bag")
		for _, v := range vs {
			bag.kids = append(bag.kids, d.newLi(in, v))
		}
		return &xmlNode{kind: elemNode, name: xml.Name{Space: prefix, Local: local}, kids: []*xmlNode{bag}}
	})
	return true
}

func (d *xmpDoc) newRDF(in scope, local string) *xmlNode {
	return &xmlNode{kind: elemNode, name: xml.Name{Space: prefixIn(in, nsRDF), Local: local}}
}

func (d *xmpDoc) newLi(in scope, v string) *xmlNode {
	return &xmlNode{kind: elemNode, name: xml.Name{Space: prefixIn(in, nsRDF), Local: "li"}, kids: []*xmlNode{{kind: textNode, text: []byte(v)}}}
}

func insertFirst(c, li *xmlNode) {
	c.empty = false
	for i, k := range c.kids {
		if k.kind == elemNode {
			c.kids = slices.Insert(c.kids, i, li)
			return
		}
	}
	c.kids = append(c.kids, li)
}

// prefixIn is a prefix bound to ns in scope ("" for the default
// namespace), or "" when there is none (the caller declares one).
func prefixIn(in scope, ns string) string {
	keys := make([]string, 0, len(in))
	for k := range in {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		if in[k] == ns && k != "" {
			return k
		}
	}
	return ""
}

// replace puts a new element made by build where the property's first
// occurrence was (removing all of them), or at the end of the first
// rdf:Description, declaring the namespace there when no prefix in scope
// is bound to it.
func (d *xmpDoc) replace(ns, local string, build func(prefix string, in scope) *xmlNode) {
	ps := d.props(ns, local)
	var desc *xmlNode
	var in scope
	at := -1
	if len(ps) > 0 {
		desc, in = ps[0].desc, ps[0].in
		if ps[0].elem != nil {
			at = slices.Index(desc.kids, ps[0].elem)
		}
	} else {
		ds, ss := d.descriptions()
		if len(ds) == 0 {
			desc = &xmlNode{kind: elemNode, name: xml.Name{Space: prefixIn(d.rdfIn, nsRDF), Local: "Description"},
				attrs: []xml.Attr{{Name: xml.Name{Space: prefixIn(d.rdfIn, nsRDF), Local: "about"}, Value: ""}}}
			appendIndented(d.rdf, desc)
			ds, ss = []*xmlNode{desc}, []scope{d.rdfIn}
		}
		desc, in = ds[0], ss[0]
	}
	prefix := prefixIn(in, ns)
	if prefix == "" {
		prefix = d.declare(desc, in, ns)
		in = in.enter(desc)
	}
	el := build(prefix, in)
	d.removeAttrs(ps)
	if at >= 0 {
		// In place of the first occurrence; the others go.
		desc.kids[at] = el
		for _, p := range ps[1:] {
			if p.elem != nil {
				removeKid(p.desc, p.elem)
			}
		}
		return
	}
	for _, p := range ps {
		if p.elem != nil {
			removeKid(p.desc, p.elem)
		}
	}
	appendIndented(desc, el)
}

// removeAttrs removes the attribute occurrences among ps.
func (d *xmpDoc) removeAttrs(ps []prop) {
	byDesc := map[*xmlNode][]int{}
	for _, p := range ps {
		if p.elem == nil {
			byDesc[p.desc] = append(byDesc[p.desc], p.attr)
		}
	}
	for desc, idx := range byDesc {
		slices.Sort(idx)
		for i := len(idx) - 1; i >= 0; i-- {
			desc.attrs = slices.Delete(desc.attrs, idx[i], idx[i]+1)
		}
	}
}

// declare binds a new prefix to ns on el: the usual one when it's free in
// scope, else it with a number.
func (d *xmpDoc) declare(el *xmlNode, in scope, ns string) string {
	base := preferredPrefix[ns]
	if base == "" {
		base = "ns"
	}
	prefix := base
	for i := 1; ; i++ {
		if _, taken := in[prefix]; !taken {
			break
		}
		prefix = base + strconv.Itoa(i)
	}
	el.attrs = append(el.attrs, xml.Attr{Name: xml.Name{Space: nsXMLNS, Local: prefix}, Value: ns})
	return prefix
}

// appendIndented appends el to n's children, indented like its other
// element children, so that the packet keeps its layout. The whitespace
// before n's end tag stays last.
func appendIndented(n, el *xmlNode) {
	ws := func(s string) *xmlNode { return &xmlNode{kind: textNode, text: []byte(s)} }
	isWS := func(k *xmlNode) bool { return k.kind == textNode && len(bytes.TrimSpace(k.text)) == 0 }
	indent := "\n   "
	for i, k := range n.kids {
		if k.kind == elemNode && i > 0 && isWS(n.kids[i-1]) {
			indent = string(n.kids[i-1].text)
		}
	}
	switch k := len(n.kids); {
	case k > 0 && isWS(n.kids[k-1]):
		n.kids = slices.Insert(n.kids, k-1, ws(indent), el)
	case k > 0:
		n.kids = append(n.kids, ws(indent), el)
	default:
		n.kids = []*xmlNode{ws(indent), el, ws("\n  ")}
	}
	n.empty = false
}
