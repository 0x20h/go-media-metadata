# IPTC-IIM

The IPTC Information Interchange Model 4.2, the Application Record (2:),
as JPEG files carry it: in APP13, among Photoshop's image resource blocks
(Adobe Photoshop File Formats Specification), resource 0x0404.

## Reading

- **`ReadIPTC`** returns a JPEG's IIM data, nil without any. It reads the
  image resources of every APP13 segment that starts with
  `Photoshop 3.0\0`, joined together, because writers split them across
  segments when they pass one.
- **`ParseIPTC`** reads the fields that mirror XMP, as the IPTC Photo
  Metadata Standard maps them:

| Field | Dataset | XMP |
|---|---|---|
| `Title` | 2:05 Object Name | dc:title |
| `Keywords` | 2:25 Keywords, repeated | dc:subject |
| `Description` | 2:120 Caption/Abstract | dc:description |

**Character set.** Text is UTF-8 when 1:90 says `ESC % G`, or when it is
valid UTF-8 anyway. Otherwise it's read as Latin-1 (Windows-1252), as
ExifTool assumes for IIM without a character set.

**Lengths and padding.** Extended dataset lengths are read, and trailing
zero padding is ignored.

## Writing

**`EditIPTC(iim, IPTCEdit)`.** A nil field leaves a field as it is; `""`
or an empty list removes it.

- **Every other dataset stays in its place**, the byline and copyright
  included. A value that is set replaces the old dataset where it was. A
  new one goes in IIM's order, by record and then dataset number.
- **Values are written as UTF-8.** 1:90 is set to say so, and the
  record's Latin-1 text is re-encoded so that one character set covers it
  all. A new record gets 2:00 (record version 4).
- **IIM's length limits apply.** A title or keyword is at most 64 bytes, a
  description at most 2000. A longer value is cut at a character boundary,
  as IIM readers expect. XMP keeps the full text.
- **An edit that changes nothing** returns the data as it was.

**`SetIPTC` and `PlanIPTC`** write a JPEG's IIM data. Empty data removes
it.

- **Other resources** stay as they are, in their order.
- **The digest:** an IPTC digest (resource 0x0425) is set to the new
  data's MD5, so Photoshop doesn't take the change for one made by another
  program. It is removed together with the IIM.
- **Where it's written:** the resources go in one APP13 segment, in place
  of the old ones. That is in place when the segment keeps its length;
  otherwise a new segment is written and the old ones aren't copied.
- **A JPEG without APP13** gets one after its APPn segments, before the
  tables.
- **Image resources past one segment's 64 KB** are refused
  (`ErrUnsupported`).

## Checks

- **`TestIPTCAgreesWithExifTool`:** ExifTool reads what `SetIPTC` writes
  as `ParseIPTC` does, including Latin-1 sources and the untouched
  byline. It finds the digest current, with no warning.
- **`FuzzIPTC`:** whatever `ParseIPTC` reads can be edited. The result
  reads back with the edit applied and the description unchanged, and the
  JPEG plan passes the plan checks.
