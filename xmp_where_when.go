package mediameta

import (
	"math"
	"strconv"
	"strings"
	"time"
)

// Namespaces of the position and dates read from XMP.
const (
	nsExif      = "http://ns.adobe.com/exif/1.0/"
	nsPhotoshop = "http://ns.adobe.com/photoshop/1.0/"
)

func init() {
	preferredPrefix[nsExif] = "exif"
	preferredPrefix[nsPhotoshop] = "photoshop"
}

// XMPDate is a date as XMP writes it: as precise as its writer made it, with
// or without a time zone.
type XMPDate struct {
	// Time is the date and time written. Parts not written are at their
	// start: "2025" is 1 January 2025, 00:00. With a time zone, OffsetKnown
	// is true and Time is in a fixed zone of that offset. Without one, Time
	// is the clock time written, in time.UTC, and is not a UTC time.
	Time        time.Time
	OffsetKnown bool
	// Precision is how much of the date was written.
	Precision DatePrecision
}

// DatePrecision is how much of a date was written.
type DatePrecision uint8

const (
	PrecisionYear   DatePrecision = iota + 1 // "2025"
	PrecisionMonth                           // "2025-05"
	PrecisionDay                             // "2025-05-04"
	PrecisionMinute                          // "2025-05-04T13:14"
	PrecisionSecond                          // "2025-05-04T13:14:15", with or without a fraction
)

// xmpGPS reads exif:GPSLatitude and exif:GPSLongitude, and with them
// exif:GPSAltitude and exif:GPSAltitudeRef: nil without a valid latitude
// and longitude.
func (d *xmpDoc) xmpGPS() *GPS {
	first := func(local string) (string, bool) {
		if ps := d.props(nsExif, local); len(ps) > 0 {
			return ps[0].value(), true
		}
		return "", false
	}
	s, ok := first("GPSLatitude")
	if !ok {
		return nil
	}
	lat, ok := parseXMPCoord(s, 'N', 'S', 90)
	if !ok {
		return nil
	}
	s, _ = first("GPSLongitude")
	lon, ok := parseXMPCoord(s, 'E', 'W', 180)
	if !ok {
		return nil
	}
	p := &GPS{Latitude: lat, Longitude: lon}
	if s, ok := first("GPSAltitude"); ok {
		if alt, ok := parseXMPRational(s); ok {
			p.Altitude, p.HasAltitude = math.Abs(alt), true
			// As in Exif: 1 is below the reference, 0 (the default)
			// above it.
			if ref, _ := first("GPSAltitudeRef"); strings.TrimSpace(ref) == "1" {
				p.Altitude = -p.Altitude
			}
		}
	}
	return p
}

// parseXMPCoord reads an XMP GPS coordinate: "DDD,MM,SSk" or "DDD,MM.mmk"
// as the XMP specification has them, where k is the direction (pos or
// neg, either case), and the decimal degrees some writers use instead,
// with a direction ("51.5N") or a sign ("-0.125"). Spaces may stand for
// the commas. Minutes and seconds are below 60, and the result no more
// than limit degrees either way.
func parseXMPCoord(s string, pos, neg byte, limit float64) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	sign := 1.0
	switch c := s[len(s)-1] | 0x20; c {
	case pos | 0x20, neg | 0x20:
		if c == neg|0x20 {
			sign = -1
		}
		s = strings.TrimSpace(s[:len(s)-1])
	default:
		if c >= 'a' && c <= 'z' {
			return 0, false // another axis's direction, or none at all
		}
		if s[0] == '-' {
			sign, s = -1, s[1:]
		}
	}
	parts := strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' })
	if len(parts) == 0 || len(parts) > 3 {
		return 0, false
	}
	var v [3]float64
	for i, p := range parts {
		// Unsigned decimals only: the sign is the direction's.
		for j := 0; j < len(p); j++ {
			if c := p[j]; (c < '0' || c > '9') && c != '.' {
				return 0, false
			}
		}
		f, err := strconv.ParseFloat(p, 64)
		if err != nil {
			return 0, false
		}
		// Only the last part may have a fraction; minutes and seconds
		// are below 60.
		if i < len(parts)-1 && f != math.Trunc(f) || i > 0 && f >= 60 {
			return 0, false
		}
		v[i] = f
	}
	deg := v[0] + v[1]/60 + v[2]/3600
	if deg > limit {
		return 0, false
	}
	return sign * deg, true
}

// parseXMPRational reads an XMP Rational ("71/2") or a decimal ("35.5"):
// a finite number.
func parseXMPRational(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	num, den, frac := strings.Cut(s, "/")
	n, err := strconv.ParseFloat(num, 64)
	if err != nil {
		return 0, false
	}
	if frac {
		d, err := strconv.ParseFloat(den, 64)
		if err != nil || d == 0 {
			return 0, false
		}
		n /= d
	}
	if math.IsNaN(n) || math.IsInf(n, 0) {
		return 0, false
	}
	return n, true
}

// xmpDate reads the date property ns:local, nil without a valid one.
func (d *xmpDoc) xmpDate(ns, local string) *XMPDate {
	ps := d.props(ns, local)
	if len(ps) == 0 {
		return nil
	}
	if at, ok := parseXMPDate(ps[0].value()); ok {
		return &at
	}
	return nil
}

// parseXMPDate reads an XMP Date: "YYYY", "YYYY-MM", "YYYY-MM-DD", then
// "Thh:mm", "Thh:mm:ss" or "Thh:mm:ss.s…" with an optional time zone ("Z",
// "+hh:mm", "-hh:mm"). It also reads the Exif layout some writers put in
// XMP: ":" between the date's parts, a space before the time. A date that
// doesn't exist (30 February, 24:00, a leap second) or year 0 is no date;
// a fraction past nanoseconds is cut there.
func parseXMPDate(s string) (XMPDate, bool) {
	s = strings.TrimSpace(s)
	digits := func(s string, n int) (int, bool) {
		if len(s) < n {
			return 0, false
		}
		v := 0
		for _, c := range []byte(s[:n]) {
			if c < '0' || c > '9' {
				return 0, false
			}
			v = v*10 + int(c-'0')
		}
		return v, true
	}
	var at XMPDate
	f := [6]int{0, 1, 1} // year, month, day, hour, minute, second
	var ok bool
	if f[0], ok = digits(s, 4); !ok || f[0] == 0 {
		return XMPDate{}, false
	}
	at.Precision, s = PrecisionYear, s[4:]
	var sep byte
	if len(s) > 0 && (s[0] == '-' || s[0] == ':') {
		sep = s[0]
		if f[1], ok = digits(s[1:], 2); !ok {
			return XMPDate{}, false
		}
		at.Precision, s = PrecisionMonth, s[3:]
	}
	if at.Precision == PrecisionMonth && len(s) > 0 && s[0] == sep {
		if f[2], ok = digits(s[1:], 2); !ok {
			return XMPDate{}, false
		}
		at.Precision, s = PrecisionDay, s[3:]
	}
	nanos := 0
	zone := time.UTC
	if at.Precision == PrecisionDay && len(s) > 0 && (s[0] == 'T' || s[0] == ' ') {
		if len(s) < 6 || s[3] != ':' {
			return XMPDate{}, false
		}
		h, ok1 := digits(s[1:], 2)
		m, ok2 := digits(s[4:], 2)
		if !ok1 || !ok2 {
			return XMPDate{}, false
		}
		f[3], f[4], at.Precision, s = h, m, PrecisionMinute, s[6:]
		if len(s) > 0 && s[0] == ':' {
			if f[5], ok = digits(s[1:], 2); !ok {
				return XMPDate{}, false
			}
			at.Precision, s = PrecisionSecond, s[3:]
			if len(s) > 0 && s[0] == '.' {
				n := 1
				for n < len(s) && s[n] >= '0' && s[n] <= '9' {
					if n <= 9 {
						nanos += int(s[n]-'0') * int(math.Pow10(9-n))
					}
					n++
				}
				if n == 1 {
					return XMPDate{}, false
				}
				s = s[n:]
			}
		}
		if s != "" {
			secs, ok := parseOffset(s)
			if !ok {
				return XMPDate{}, false
			}
			zone, at.OffsetKnown, s = time.FixedZone(s, secs), true, ""
		}
	}
	if s != "" {
		return XMPDate{}, false
	}
	t := time.Date(f[0], time.Month(f[1]), f[2], f[3], f[4], f[5], nanos, time.UTC)
	// time.Date carries 24:00 or 30 February into another day.
	if t.Year() != f[0] || int(t.Month()) != f[1] || t.Day() != f[2] || t.Hour() != f[3] || t.Minute() != f[4] || t.Second() != f[5] {
		return XMPDate{}, false
	}
	at.Time = time.Date(f[0], time.Month(f[1]), f[2], f[3], f[4], f[5], nanos, zone)
	return at, true
}
