package mediameta

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"
)

// A small XML tree that keeps what an XMP packet says, so that a packet
// can be read, changed in a few places, and written back with everything
// else in it as it was: the prefixes as written, comments, processing
// instructions (the xpacket wrapper), whitespace and empty-element tags.
// Only escaping is normalized on output.

// Limits on untrusted XML: depth and node count.
const (
	maxXMLDepth = 64
	maxXMLNodes = 1 << 18
)

type nodeKind uint8

const (
	elemNode nodeKind = iota
	textNode
	commentNode
	procInstNode
	directiveNode
)

// xmlNode is one node. For an element, name and attrs are as written
// (Space is the prefix, not a namespace); empty is true for one written
// as <a/>. text holds a text node's characters, a comment's or a
// directive's text, or a processing instruction's instruction (with
// target its target).
type xmlNode struct {
	kind   nodeKind
	name   xml.Name
	attrs  []xml.Attr
	kids   []*xmlNode
	text   []byte
	target string
	empty  bool
}

// parseXML reads an XML document into a list of top-level nodes,
// checking that elements nest and that no prefix is used undeclared.
func parseXML(b []byte) ([]*xmlNode, error) {
	d := xml.NewDecoder(bytes.NewReader(b))
	d.Strict = true
	var top []*xmlNode
	var stack []*xmlNode
	nodes := 0
	add := func(n *xmlNode) {
		if len(stack) == 0 {
			top = append(top, n)
		} else {
			p := stack[len(stack)-1]
			p.kids = append(p.kids, n)
		}
	}
	for {
		before := d.InputOffset()
		tok, err := d.RawToken()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%w: XML: %v", ErrMalformed, err)
		}
		if nodes++; nodes > maxXMLNodes {
			return nil, fmt.Errorf("%w: XML of more than %d nodes", ErrMalformed, maxXMLNodes)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if len(stack) >= maxXMLDepth {
				return nil, fmt.Errorf("%w: XML nested deeper than %d", ErrMalformed, maxXMLDepth)
			}
			end := d.InputOffset()
			n := &xmlNode{kind: elemNode, name: t.Name, attrs: append([]xml.Attr(nil), t.Attr...)}
			// RawToken reports <a/> as a start and an end: note which,
			// so that it's written back the same way.
			n.empty = end-before >= 2 && end <= int64(len(b)) && string(b[end-2:end]) == "/>"
			add(n)
			stack = append(stack, n)
		case xml.EndElement:
			if len(stack) == 0 || stack[len(stack)-1].name != t.Name {
				return nil, fmt.Errorf("%w: XML: unexpected </%s>", ErrMalformed, qname(t.Name))
			}
			stack = stack[:len(stack)-1]
		case xml.CharData:
			add(&xmlNode{kind: textNode, text: bytes.Clone(t)})
		case xml.Comment:
			add(&xmlNode{kind: commentNode, text: bytes.Clone(t)})
		case xml.ProcInst:
			add(&xmlNode{kind: procInstNode, target: t.Target, text: bytes.Clone(t.Inst)})
		case xml.Directive:
			add(&xmlNode{kind: directiveNode, text: bytes.Clone(t)})
		}
	}
	if len(stack) != 0 {
		return nil, fmt.Errorf("%w: XML: <%s> not closed", ErrMalformed, qname(stack[len(stack)-1].name))
	}
	if err := checkPrefixes(top, nil); err != nil {
		return nil, err
	}
	return top, nil
}

func qname(n xml.Name) string {
	if n.Space == "" {
		return n.Local
	}
	return n.Space + ":" + n.Local
}

// scope maps the prefixes in scope to their namespaces; "" is the default
// namespace.
type scope map[string]string

// enter is the scope inside element n.
func (s scope) enter(n *xmlNode) scope {
	var out scope
	for _, a := range n.attrs {
		var prefix string
		switch {
		case a.Name.Space == "xmlns":
			prefix = a.Name.Local
		case a.Name.Space == "" && a.Name.Local == "xmlns":
		default:
			continue
		}
		if out == nil {
			out = make(scope, len(s)+1)
			for k, v := range s {
				out[k] = v
			}
		}
		out[prefix] = a.Value
	}
	if out == nil {
		return s
	}
	return out
}

// ns is the namespace of a name as written in this scope: its prefix's,
// or for an element without one the default namespace. Attributes
// without a prefix have none.
func (s scope) ns(n xml.Name, attr bool) (string, bool) {
	switch n.Space {
	case "xml":
		return xmlNS, true
	case "":
		if attr {
			return "", true
		}
		return s[""], true
	}
	v, ok := s[n.Space]
	return v, ok
}

const xmlNS = "http://www.w3.org/XML/1998/namespace"

// isNCName reports whether s is an XML name without a colon (Namespaces
// in XML 1.0, NCName): a letter or "_" first, then letters, digits, ".",
// "-", "_" and combining marks. encoding/xml checks element names this
// way but not a namespace declaration's prefix, which a later write would
// reuse as an element's prefix.
func isNCName(s string) bool {
	if s == "" {
		return false
	}
	for i, c := range s {
		switch {
		case c == '_' || unicode.IsLetter(c):
		case i > 0 && (c == '-' || c == '.' || c == '·' || unicode.In(c, unicode.Nd, unicode.Mn, unicode.Mc, unicode.Lm)):
		default:
			return false
		}
	}
	return true
}

// checkName reports a name that isn't an NCName, or a prefix that isn't.
func checkName(n xml.Name) error {
	if n.Space != "" && !isNCName(n.Space) || !isNCName(n.Local) {
		return fmt.Errorf("%w: XML: invalid name %q", ErrMalformed, qname(n))
	}
	return nil
}

// checkPrefixes reports a prefix used without a declaration, and a name
// (an element's, an attribute's, a declared prefix) that isn't valid.
func checkPrefixes(nodes []*xmlNode, s scope) error {
	for _, n := range nodes {
		if n.kind != elemNode {
			continue
		}
		if err := checkName(n.name); err != nil {
			return err
		}
		for _, a := range n.attrs {
			if err := checkName(a.Name); err != nil {
				return err
			}
		}
		in := s.enter(n)
		if _, ok := in.ns(n.name, false); !ok {
			return fmt.Errorf("%w: XML: undeclared prefix %q", ErrMalformed, n.name.Space)
		}
		for _, a := range n.attrs {
			if a.Name.Space == "xmlns" || a.Name.Space == "" && a.Name.Local == "xmlns" {
				continue
			}
			if _, ok := in.ns(a.Name, true); !ok {
				return fmt.Errorf("%w: XML: undeclared prefix %q", ErrMalformed, a.Name.Space)
			}
		}
		if err := checkPrefixes(n.kids, in); err != nil {
			return err
		}
	}
	return nil
}

// writeXML writes nodes as XML.
func writeXML(buf *bytes.Buffer, nodes []*xmlNode) {
	for _, n := range nodes {
		switch n.kind {
		case elemNode:
			buf.WriteByte('<')
			buf.WriteString(qname(n.name))
			for _, a := range n.attrs {
				buf.WriteByte(' ')
				buf.WriteString(qname(a.Name))
				buf.WriteString(`="`)
				escapeXML(buf, a.Value, true)
				buf.WriteByte('"')
			}
			if n.empty && len(n.kids) == 0 {
				buf.WriteString("/>")
				continue
			}
			buf.WriteByte('>')
			writeXML(buf, n.kids)
			buf.WriteString("</")
			buf.WriteString(qname(n.name))
			buf.WriteByte('>')
		case textNode:
			escapeXML(buf, string(n.text), false)
		case commentNode:
			buf.WriteString("<!--")
			buf.Write(n.text)
			buf.WriteString("-->")
		case procInstNode:
			buf.WriteString("<?")
			buf.WriteString(n.target)
			if len(n.text) > 0 {
				buf.WriteByte(' ')
				buf.Write(n.text)
			}
			buf.WriteString("?>")
		case directiveNode:
			buf.WriteString("<!")
			buf.Write(n.text)
			buf.WriteByte('>')
		}
	}
}

// escapeXML writes s escaped for text, or for a double-quoted attribute
// value, where tabs and line breaks are kept as references.
func escapeXML(buf *bytes.Buffer, s string, attr bool) {
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '&':
			buf.WriteString("&amp;")
		case '<':
			buf.WriteString("&lt;")
		case '>':
			buf.WriteString("&gt;")
		case '"':
			if attr {
				buf.WriteString("&quot;")
			} else {
				buf.WriteByte(c)
			}
		case '\t', '\n', '\r':
			if attr {
				fmt.Fprintf(buf, "&#x%X;", c)
			} else {
				buf.WriteByte(c)
			}
		default:
			buf.WriteByte(c)
		}
	}
}

// textOf is the text directly in n (its text nodes, not its elements'),
// trimmed when it's only whitespace around the value.
func textOf(n *xmlNode) string {
	var sb strings.Builder
	for _, k := range n.kids {
		if k.kind == textNode {
			sb.Write(k.text)
		}
	}
	return sb.String()
}

// elements are n's element children.
func elements(n *xmlNode) []*xmlNode {
	var out []*xmlNode
	for _, k := range n.kids {
		if k.kind == elemNode {
			out = append(out, k)
		}
	}
	return out
}
