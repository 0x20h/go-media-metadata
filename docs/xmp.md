# XMP

ISO 16684-1 and the Adobe XMP Specification (Parts 1 to 3). `ParseXMP`
and `EditXMP` work on a packet's bytes, whatever file holds it; the
format pages say where packets are read from and written to. A JPEG's
extended XMP is covered in [jpeg](jpeg.md).

## Reading: ParseXMP

```go
type XMP struct {
	Title, Description string   // dc:title, dc:description
	Keywords           []string // dc:subject
	Rating             int      // xmp:Rating, -1 to 5, when HasRating
	HasRating          bool
}
```

- `dc:title` and `dc:description` are language alternatives. The
  `x-default` item is read, else the first.
- Properties are read from any `rdf:Description`, as attributes or as
  elements, under whatever prefixes the packet declares (the default
  namespace included). The `rdf:RDF` can be in `x:xmpmeta`, in its old
  name `x:xapmeta`, or at the top.
- The first occurrence of a property wins.
- Simple values where XMP wants an array (`dc:title="T"`,
  `<dc:subject>one</dc:subject>`) are read too.
- A rating is a Real. `3.0` reads as 3; a fractional or out-of-range
  rating reads as none.

## Writing: EditXMP

```go
type XMPEdit struct {
	Title, Description *string   // nil leaves; "" removes
	Keywords           *[]string // nil leaves; empty removes
	Rating             *int      // -1 to 5
	RemoveRating       bool
}
```

`EditXMP` changes those properties and keeps everything else:

- other properties, and a title's other languages;
- namespaces as declared, comments, whitespace between elements, and
  empty-element tags.

What XML doesn't distinguish is written one way: attributes on one line
in double quotes, and text escaped minimally. An edit that changes
nothing returns the packet byte for byte.

**Where properties go.** A property that is set stays where it was. A
new one goes in the first `rdf:Description`, which gets the namespace
declared when no prefix in scope binds it (`dc1` when `dc` is taken). A
title without `x-default` gets one as its first item. A packet without an
`rdf:Description` gets one, and an empty input becomes a new packet.

**Padding.** An xpacket wrapper keeps the packet's length when the edit
fits in its padding (the whitespace before `<?xpacket end`), so a writer
can put it back in place. A packet that outgrows its padding gets 2 KB of
new padding, as Part 1 suggests.

## Parsing untrusted XML

`xmlnode.go` builds a small tree from `encoding/xml`'s raw tokens:

- Nesting must match, and every prefix must be declared.
- Unknown entities are rejected (strict mode), as is input that isn't
  UTF-8.
- Depth is capped at 64, nodes at 2^18 and packets at 16 MiB.

## Checks

- `TestXMPAgreesWithExifTool`: ExifTool reads what `EditXMP` writes the
  same way `ParseXMP` does, and the properties the edit didn't touch are
  still there.
- `FuzzEditXMP`: any packet `ParseXMP` reads can be edited, and the
  result parses back with the edit applied.
