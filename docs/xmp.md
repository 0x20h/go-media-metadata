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
	PersonInImage      []string // Iptc4xmpExt:PersonInImage
	Regions            *Regions // mwg-rs:Regions, nil without any
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
	PersonInImage      *[]string // nil leaves; empty removes
	Regions            *Regions  // as a whole; no regions removes
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

## Face regions and people

`XMP.Regions` and `XMP.PersonInImage` cover the people in an image:

- **Regions** are `mwg-rs:Regions`, as the Metadata Working Group's
  guidelines (2.0, 5.9) define them. They hold the image size the regions
  were made for (`AppliedToDimensions`) and the list of regions. Each
  region has a name, a type (`Face`, `Pet`, `Focus` or `BarCode`; empty
  means `Face`) and an area. The area is its centre `X`, `Y` and size `W`,
  `H`, as fractions of the stored image (`stArea`, unit `normalized`), with
  (0, 0) at the top-left corner.
- **PersonInImage** is `Iptc4xmpExt:PersonInImage`, the names of the
  people shown (IPTC Photo Metadata, Extension schema).

**Reading.** The struct fields are read wherever RDF allows them: as
attributes of the property element, as its children with
`rdf:parseType="Resource"`, or inside an `rdf:Description`. A region whose
area isn't normalized, or doesn't fit the image (a value outside 0 to 1),
is left out. Readers should compare `AppliedTo` with the image's own size,
and ignore regions made for another one (an image cropped or turned
since).

**Writing.** `XMPEdit.Regions` writes `mwg-rs:Regions` as a whole, in the
MWG's layout. The unit defaults to `pixel` and the type to `Face`.
Fields of the old regions that `Region` doesn't model (a region's
rotation, extensions) aren't kept. A `Regions` without regions removes
the property. Regions without `AppliedTo`, or with an area outside 0 to
1, are refused. `XMPEdit.PersonInImage` sets the names, and an empty list
removes them. Setting the same regions again changes nothing.

`TestXMPRegionsAgreeWithExifTool` checks that ExifTool reads the regions
of a sample laid out as Lightroom and other writers do, and the ones
`EditXMP` writes, as `ParseXMP` does.

## Editing a file's packet: PlanXMPEdit

```go
func PlanXMPEdit(src io.ReaderAt, size int64, edit XMPEdit) (*Plan, error)
func EditFileXMP(src io.ReaderAt, size int64, dst io.Writer, edit XMPEdit) error
```

`PlanXMPEdit` edits the XMP packet of a JPEG, HEIF, PNG or WebP file in
three steps:

1. It reads the packet as `ReadXMP` does, with a JPEG's extended XMP
   merged in.
2. It applies the edit with `EditXMP`.
3. It writes the packet back as `PlanXMP` does.

A packet that keeps its length (the edit fit in its padding) is written
in place: a HEIF item's data, a JPEG's XMP segment, or a PNG or WebP
chunk. A longer one goes where `SetXMP` puts it, and the old one doesn't
stay in the file. A file without XMP gets a new packet holding the edit.
A packet this package can't parse is refused with `EditXMP`'s error
rather than replaced, since the rest of it would be lost. `EditFileXMP`
streams the same edit to a writer.

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
- `TestPlanXMPEdit`: every edit, on files of every format with and
  without XMP, meets four conditions:
  - it reads back with the edit, and the rest of the packet as it was;
  - it leaves the image data as it was;
  - it keeps the plan's invariants;
  - made again, it is in place and changes nothing.
- `TestPlanXMPEditWithExifTool`: ExifTool reads the edited title, keywords
  and rating from every format. Its `-validate` reports no warning the
  source file didn't already have.
- `FuzzPlanXMPEdit`: the same as `TestPlanXMPEdit`, on mutated files.
