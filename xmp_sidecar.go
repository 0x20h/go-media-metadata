package mediameta

import (
	"slices"
	"strconv"
)

// nsTIFF is the namespace of the TIFF tags' XMP mirrors (tiff:Orientation).
const nsTIFF = "http://ns.adobe.com/tiff/1.0/"

// xmpOrientation is tiff:Orientation, 0 without a valid one (1 to 8).
func (d *xmpDoc) xmpOrientation() Orientation {
	ps := d.props(nsTIFF, "Orientation")
	if len(ps) == 0 {
		return 0
	}
	n, err := strconv.Atoi(ps[0].value())
	if err != nil || n < 1 || n > 8 {
		return 0
	}
	return Orientation(n)
}

// MergeXMP combines a file's own XMP with its sidecar's, in the order the
// Metadata Working Group's guidelines (2.0) give for a camera RAW: the
// sidecar holds what was edited since, so each value the sidecar has wins,
// and the file keeps the values the sidecar leaves out.
//
// The orientation comes from the sidecar's tiff:Orientation when it has
// one; otherwise the file's own (ReadOrientation, its Exif or container)
// applies, as XMP.Orientation is 0 in the result unless the file's packet
// held one.
//
// A value is the sidecar's when it is set there: a title or description
// that isn't empty, keywords, hierarchical keywords or people that aren't
// an empty list, a rating (HasRating), regions, a GPS position, a date, or
// a valid orientation. Lists are taken whole, not joined. The result
// shares no slices with file or sidecar.
//
// Finding the sidecar (IMG_1.xmp or IMG_1.CR3.xmp next to IMG_1.CR3) and
// reading it with ParseXMP is the caller's: a sidecar is an XMP packet with
// no wrapper. A sidecar that doesn't parse (ErrMalformed) is best left out,
// which keeps the file's own metadata.
func MergeXMP(file, sidecar XMP) XMP {
	m := file
	if sidecar.Title != "" {
		m.Title = sidecar.Title
	}
	if sidecar.Description != "" {
		m.Description = sidecar.Description
	}
	if len(sidecar.Keywords) > 0 {
		m.Keywords = sidecar.Keywords
	}
	if len(sidecar.HierarchicalKeywords) > 0 {
		m.HierarchicalKeywords = sidecar.HierarchicalKeywords
	}
	if sidecar.HasRating {
		m.Rating, m.HasRating = sidecar.Rating, true
	}
	if len(sidecar.PersonInImage) > 0 {
		m.PersonInImage = sidecar.PersonInImage
	}
	if sidecar.Regions != nil {
		m.Regions = sidecar.Regions
	}
	if sidecar.GPS != nil {
		m.GPS = sidecar.GPS
	}
	if sidecar.DateCreated != nil {
		m.DateCreated = sidecar.DateCreated
	}
	if sidecar.CreateDate != nil {
		m.CreateDate = sidecar.CreateDate
	}
	if sidecar.DateTimeOriginal != nil {
		m.DateTimeOriginal = sidecar.DateTimeOriginal
	}
	if sidecar.Orientation.Valid() {
		m.Orientation = sidecar.Orientation
	}
	m.Keywords = slices.Clone(m.Keywords)
	m.HierarchicalKeywords = slices.Clone(m.HierarchicalKeywords)
	m.PersonInImage = slices.Clone(m.PersonInImage)
	if m.Regions != nil {
		r := *m.Regions
		r.List = slices.Clone(r.List)
		m.Regions = &r
	}
	if m.GPS != nil {
		g := *m.GPS
		m.GPS = &g
	}
	for _, d := range []**Date{&m.DateCreated, &m.CreateDate, &m.DateTimeOriginal} {
		if *d != nil {
			c := **d
			*d = &c
		}
	}
	return m
}
