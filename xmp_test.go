package mediameta

import (
	"bytes"
	"errors"
	"slices"
	"strings"
	"testing"
)

// lightroomXMP is a packet laid out as Lightroom and Photoshop write
// them: some properties as attributes, others as elements, a second
// language for the title, a property this package doesn't know, a comment,
// and padding.
var lightroomXMP = "<?xpacket begin=\"\uFEFF\" id=\"W5M0MpCehiHzreSzNTczkc9d\"?>\n" +
	`<x:xmpmeta xmlns:x="adobe:ns:meta/" x:xmptk="Adobe XMP Core 7.0">
 <rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">
  <rdf:Description rdf:about=""
    xmlns:xmp="http://ns.adobe.com/xap/1.0/"
    xmlns:dc="http://purl.org/dc/elements/1.1/"
    xmlns:photoshop="http://ns.adobe.com/photoshop/1.0/"
   xmp:Rating="4"
   photoshop:City="Köln">
   <!-- kept as it is -->
   <dc:title>
    <rdf:Alt>
     <rdf:li xml:lang="x-default">Lake &amp; mountains</rdf:li>
     <rdf:li xml:lang="de">See &amp; Berge</rdf:li>
    </rdf:Alt>
   </dc:title>
   <dc:subject>
    <rdf:Bag>
     <rdf:li>lake</rdf:li>
     <rdf:li>snow</rdf:li>
    </rdf:Bag>
   </dc:subject>
   <dc:creator><rdf:Seq><rdf:li>Someone</rdf:li></rdf:Seq></dc:creator>
  </rdf:Description>
 </rdf:RDF>
</x:xmpmeta>
` + "\n" + strings.Repeat(" ", 99) + "\n" + strings.Repeat(" ", 99) + "\n" + `<?xpacket end="w"?>`

func parseXMP(t *testing.T, packet []byte) XMP {
	t.Helper()
	x, err := ParseXMP(packet)
	if err != nil {
		t.Fatalf("ParseXMP: %v\n%s", err, packet)
	}
	return x
}

func editXMP(t *testing.T, packet []byte, e XMPEdit) []byte {
	t.Helper()
	out, err := EditXMP(packet, e)
	if err != nil {
		t.Fatalf("EditXMP: %v", err)
	}
	if _, err := parseXMLDoc(out); err != nil {
		t.Fatalf("EditXMP wrote XML that doesn't parse: %v\n%s", err, out)
	}
	return out
}

func parseXMLDoc(b []byte) ([]*xmlNode, error) { return parseXML(b) }

func TestParseXMP(t *testing.T) {
	x := parseXMP(t, []byte(lightroomXMP))
	want := XMP{Title: "Lake & mountains", Keywords: []string{"lake", "snow"}, Rating: 4, HasRating: true}
	if x.Title != want.Title || x.Description != "" || !slices.Equal(x.Keywords, want.Keywords) || x.Rating != 4 || !x.HasRating {
		t.Errorf("got %+v, want %+v", x, want)
	}
}

// Forms that writers use besides the canonical ones: other prefixes, the
// default namespace, rdf:RDF at the top, several rdf:Descriptions, a
// title without x-default, simple values where XMP wants arrays, ratings
// as elements and as reals.
func TestParseXMPForms(t *testing.T) {
	for name, c := range map[string]struct {
		packet string
		want   XMP
	}{
		"other prefixes": {`<a:RDF xmlns:a="http://www.w3.org/1999/02/22-rdf-syntax-ns#"><a:Description xmlns:e="http://purl.org/dc/elements/1.1/"><e:title><a:Alt><a:li xml:lang="x-default">T</a:li></a:Alt></e:title></a:Description></a:RDF>`,
			XMP{Title: "T"}},
		"default namespace": {`<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#"><rdf:Description><Rating xmlns="http://ns.adobe.com/xap/1.0/">5</Rating></rdf:Description></rdf:RDF>`,
			XMP{Rating: 5, HasRating: true}},
		"two descriptions": {`<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#"><rdf:Description xmlns:xmp="http://ns.adobe.com/xap/1.0/" xmp:Rating="-1"/><rdf:Description xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:description><rdf:Alt><rdf:li xml:lang="en">D</rdf:li></rdf:Alt></dc:description></rdf:Description></rdf:RDF></x:xmpmeta>`,
			XMP{Description: "D", Rating: -1, HasRating: true}},
		"simple values": {`<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#" xmlns:dc="http://purl.org/dc/elements/1.1/"><rdf:Description dc:title="T"><dc:subject>one</dc:subject></rdf:Description></rdf:RDF>`,
			XMP{Title: "T", Keywords: []string{"one"}}},
		"real rating": {`<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#" xmlns:xmp="http://ns.adobe.com/xap/1.0/"><rdf:Description xmp:Rating="3.0"/></rdf:RDF>`,
			XMP{Rating: 3, HasRating: true}},
		"fractional rating": {`<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#" xmlns:xmp="http://ns.adobe.com/xap/1.0/"><rdf:Description xmp:Rating="2.5"/></rdf:RDF>`,
			XMP{}},
		"rating out of range": {`<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#" xmlns:xmp="http://ns.adobe.com/xap/1.0/"><rdf:Description xmp:Rating="7"/></rdf:RDF>`,
			XMP{}},
		"old xapmeta": {`<x:xapmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#" xmlns:xmp="http://ns.adobe.com/xap/1.0/"><rdf:Description xmp:Rating="1"/></rdf:RDF></x:xapmeta>`,
			XMP{Rating: 1, HasRating: true}},
	} {
		x := parseXMP(t, []byte(c.packet))
		if x.Title != c.want.Title || x.Description != c.want.Description || !slices.Equal(x.Keywords, c.want.Keywords) ||
			x.Rating != c.want.Rating || x.HasRating != c.want.HasRating {
			t.Errorf("%s: got %+v, want %+v", name, x, c.want)
		}
	}
}

func TestParseXMPErrors(t *testing.T) {
	deep := strings.Repeat("<a>", 100) + strings.Repeat("</a>", 100)
	for name, packet := range map[string]string{
		"not XML":            "hello <",
		"no rdf:RDF":         `<x:xmpmeta xmlns:x="adobe:ns:meta/"/>`,
		"unclosed":           `<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#"><rdf:Description>`,
		"mismatched":         `<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#"></rdf:Description>`,
		"undeclared prefix":  `<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#"><dc:title/></rdf:RDF>`,
		"too deep":           deep,
		"unknown entity":     `<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">&bomb;</rdf:RDF>`,
		"not UTF-8":          "<rdf:RDF xmlns:rdf=\"http://www.w3.org/1999/02/22-rdf-syntax-ns#\">\xff</rdf:RDF>",
		"two roots elements": `<a/><b/>`,
	} {
		if _, err := ParseXMP([]byte(packet)); !errors.Is(err, ErrMalformed) {
			t.Errorf("%s: %v, want ErrMalformed", name, err)
		}
	}
}

// Editing changes what was asked and keeps the rest: the other language,
// the unknown property and attribute, the comment, the prefixes, the
// padding's total length.
func TestEditXMPKeepsTheRest(t *testing.T) {
	src := []byte(lightroomXMP)
	out := editXMP(t, src, XMPEdit{Title: ptr("Lake <3"), Keywords: &[]string{"lake", "ice"}, Rating: ptr(5), Description: ptr("New")})
	x := parseXMP(t, out)
	if x.Title != "Lake <3" || x.Description != "New" || !slices.Equal(x.Keywords, []string{"lake", "ice"}) || x.Rating != 5 {
		t.Errorf("got %+v", x)
	}
	for _, keep := range []string{
		`<rdf:li xml:lang="de">See &amp; Berge</rdf:li>`,
		`photoshop:City="Köln"`,
		`<!-- kept as it is -->`,
		`<dc:creator><rdf:Seq><rdf:li>Someone</rdf:li></rdf:Seq></dc:creator>`,
		`x:xmptk="Adobe XMP Core 7.0"`,
		`xmp:Rating="5"`,
		`<?xpacket end="w"?>`,
	} {
		if !bytes.Contains(out, []byte(keep)) {
			t.Errorf("lost %q:\n%s", keep, out)
		}
	}
	if len(out) != len(src) {
		t.Errorf("length %d, want %d (the edit fits in the padding)", len(out), len(src))
	}
	if !bytes.HasPrefix(out, []byte("<?xpacket begin=\"\uFEFF\"")) {
		t.Error("the xpacket header changed")
	}
}

func TestEditXMPNoChange(t *testing.T) {
	src := []byte(lightroomXMP)
	for name, e := range map[string]XMPEdit{
		"nothing":      {},
		"same title":   {Title: ptr("Lake & mountains")},
		"same rating":  {Rating: ptr(4)},
		"same subject": {Keywords: &[]string{"lake", "snow"}},
		"remove none":  {Description: ptr("")},
	} {
		if out := editXMP(t, src, e); !bytes.Equal(out, src) {
			t.Errorf("%s: the packet changed", name)
		}
	}
}

func TestEditXMPRemoves(t *testing.T) {
	out := editXMP(t, []byte(lightroomXMP), XMPEdit{Title: ptr(""), Keywords: &[]string{}, RemoveRating: true})
	x := parseXMP(t, out)
	if x.Title != "" || x.Keywords != nil || x.HasRating {
		t.Errorf("got %+v", x)
	}
	for _, gone := range []string{"dc:title", "dc:subject", "Rating", "See &amp; Berge"} {
		if bytes.Contains(out, []byte(gone)) {
			t.Errorf("%q is still there", gone)
		}
	}
	if !bytes.Contains(out, []byte("dc:creator")) {
		t.Error("removed dc:creator too")
	}
}

// A new property goes in the first rdf:Description, declaring its
// namespace there when it isn't in scope; one without a free "dc" gets
// another prefix.
func TestEditXMPAddsProperties(t *testing.T) {
	src := `<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#"><rdf:Description rdf:about="" xmlns:dc="urn:something-else"/></rdf:RDF></x:xmpmeta>`
	out := editXMP(t, []byte(src), XMPEdit{Title: ptr("T"), Keywords: &[]string{"a", "b"}, Rating: ptr(0)})
	x := parseXMP(t, out)
	if x.Title != "T" || !slices.Equal(x.Keywords, []string{"a", "b"}) || x.Rating != 0 || !x.HasRating {
		t.Errorf("got %+v\n%s", x, out)
	}
	if !bytes.Contains(out, []byte(`xmlns:dc1="http://purl.org/dc/elements/1.1/"`)) {
		t.Errorf("dc taken, want dc1:\n%s", out)
	}
	// No rdf:Description at all: one is made.
	out = editXMP(t, []byte(`<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#"></rdf:RDF>`), XMPEdit{Rating: ptr(3)})
	if x := parseXMP(t, out); x.Rating != 3 {
		t.Errorf("got %+v\n%s", x, out)
	}
}

// A title without x-default gets one, first; the other languages stay.
func TestEditXMPTitleWithoutDefault(t *testing.T) {
	src := `<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#" xmlns:dc="http://purl.org/dc/elements/1.1/"><rdf:Description><dc:title><rdf:Alt><rdf:li xml:lang="fr">Lac</rdf:li></rdf:Alt></dc:title></rdf:Description></rdf:RDF>`
	out := editXMP(t, []byte(src), XMPEdit{Title: ptr("Lake")})
	if x := parseXMP(t, out); x.Title != "Lake" {
		t.Errorf("title %q", x.Title)
	}
	if i, j := bytes.Index(out, []byte("x-default")), bytes.Index(out, []byte(`"fr"`)); i < 0 || j < 0 || i > j {
		t.Errorf("x-default isn't first:\n%s", out)
	}
}

// From nothing: a new packet in a wrapper, with padding.
func TestEditXMPNewPacket(t *testing.T) {
	out := editXMP(t, nil, XMPEdit{Title: ptr("T"), Rating: ptr(2)})
	if x := parseXMP(t, out); x.Title != "T" || x.Rating != 2 {
		t.Errorf("got %+v", x)
	}
	if !bytes.HasPrefix(out, []byte("<?xpacket begin=")) || !bytes.HasSuffix(out, []byte(`<?xpacket end="w"?>`)) || len(out) < xmpPadding {
		t.Errorf("no wrapper or padding:\n%s", out)
	}
}

// A packet that outgrows its padding gets new padding.
func TestEditXMPOutgrowsPadding(t *testing.T) {
	src := []byte(lightroomXMP)
	long := strings.Repeat("word ", 100)
	out := editXMP(t, src, XMPEdit{Description: ptr(long)})
	if parseXMP(t, out).Description != long {
		t.Error("description not set")
	}
	if len(out) <= len(src) || len(out) < len(long)+xmpPadding {
		t.Errorf("length %d", len(out))
	}
}

func TestEditXMPErrors(t *testing.T) {
	for name, e := range map[string]XMPEdit{
		"rating 6":       {Rating: ptr(6)},
		"rating -2":      {Rating: ptr(-2)},
		"set and remove": {Rating: ptr(1), RemoveRating: true},
		"bad UTF-8":      {Title: ptr("\xff")},
	} {
		if _, err := EditXMP([]byte(lightroomXMP), e); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

// Text that needs escaping round-trips.
func TestEditXMPEscapes(t *testing.T) {
	for _, s := range []string{`a & b`, `<tag>`, `"quoted"`, "tab\tand\nnewline", "]]>"} {
		out := editXMP(t, []byte(lightroomXMP), XMPEdit{Title: ptr(s), Keywords: &[]string{s}})
		if x := parseXMP(t, out); x.Title != s || len(x.Keywords) != 1 || x.Keywords[0] != strings.TrimSpace(s) {
			t.Errorf("%q: got %+v", s, x)
		}
	}
}

func FuzzEditXMP(f *testing.F) {
	f.Add([]byte(lightroomXMP))
	f.Add([]byte(`<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#"><rdf:Description/></rdf:RDF>`))
	f.Fuzz(func(t *testing.T, data []byte) {
		withinDeadline(t, func() {
			before, err := ParseXMP(data)
			if err != nil {
				return
			}
			out, err := EditXMP(data, XMPEdit{Title: ptr("T"), Keywords: &[]string{"k"}, Rating: ptr(3)})
			if err != nil {
				t.Fatalf("EditXMP of a packet ParseXMP reads: %v", err)
			}
			after, err := ParseXMP(out)
			if err != nil {
				t.Fatalf("EditXMP wrote a packet that doesn't parse: %v\n%s", err, out)
			}
			if after.Title != "T" || !slices.Equal(after.Keywords, []string{"k"}) || after.Rating != 3 {
				t.Fatalf("got %+v", after)
			}
			if before.Description != after.Description {
				t.Fatalf("description %q became %q", before.Description, after.Description)
			}
		})
	})
}
