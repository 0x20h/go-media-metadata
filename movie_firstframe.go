package mediameta

import (
	"encoding/binary"
	"fmt"
	"math"
	"math/big"

	"github.com/0x20h/go-media-metadata/internal/bmff"
)

// startAtFirstFrame gives each video track whose first frame is presented
// after time 0 an edit list that starts it there: one entry, media time
// the first composition time, lasting the track's presented duration.
// That's a track with composition offsets (ctts: B-frames) and no edit
// list (edts), as FFmpeg's MP4 muxer writes one; it leaves the other
// tracks as they are, as the muxer does. The track's duration (tkhd) and
// the movie's (mvhd) become the presented ones.
func startAtFirstFrame(moov *qtNode) (bool, error) {
	mvhd := moov.child(bmff.T("mvhd"))
	if mvhd == nil {
		return false, nil
	}
	mp := mvhd.payload()
	movieScale, ok := headerTimescale(mp, 12, 20)
	if !ok {
		return false, nil
	}
	changed := false
	for _, trak := range moov.kids {
		if trak.typ != bmff.T("trak") || !trak.parsed() || trak.child(bmff.T("edts")) != nil {
			continue
		}
		mdia := path(trak, "mdia")
		if mdia == nil || mdia.handler() != "vide" {
			continue
		}
		mdhd, stbl := mdia.child(bmff.T("mdhd")), path(mdia, "minf", "stbl")
		tkhd := trak.child(bmff.T("tkhd"))
		if mdhd == nil || stbl == nil || tkhd == nil {
			continue
		}
		mediaScale, ok := headerTimescale(mdhd.payload(), 12, 20)
		if !ok {
			continue
		}
		stts, ctts := stbl.child(bmff.T("stts")), stbl.child(bmff.T("ctts"))
		if stts == nil || ctts == nil {
			continue
		}
		first, end, ok, err := compositionSpan(stts.payload(), ctts.payload())
		if err != nil {
			return false, err
		}
		if !ok || first <= 0 || end <= first {
			continue
		}
		dur, ok := rescale(end-first, movieScale, mediaScale)
		if !ok {
			return false, fmt.Errorf("%w: a track duration out of range", ErrUnsupported)
		}
		elst := make([]byte, 0, 28)
		if dur <= math.MaxUint32 && first <= math.MaxInt32 {
			elst = append(elst, 0, 0, 0, 0, 0, 0, 0, 1)
			elst = binary.BigEndian.AppendUint32(elst, uint32(dur))
			elst = binary.BigEndian.AppendUint32(elst, uint32(first))
		} else {
			elst = append(elst, 1, 0, 0, 0, 0, 0, 0, 1)
			elst = binary.BigEndian.AppendUint64(elst, uint64(dur))
			elst = binary.BigEndian.AppendUint64(elst, uint64(first))
		}
		elst = append(elst, 0, 1, 0, 0) // rate 1.0
		edts := &qtNode{typ: bmff.T("edts"), kids: []*qtNode{{typ: bmff.T("elst"), body: elst}}}
		// After tkhd, where muxers put it.
		at := 0
		for i, k := range trak.kids {
			if k == tkhd {
				at = i + 1
			}
		}
		trak.kids = append(trak.kids[:at], append([]*qtNode{edts}, trak.kids[at:]...)...)
		if !setHeaderDuration(tkhd, 20, 28, dur) {
			return false, fmt.Errorf("%w: a track header this package can't write", ErrUnsupported)
		}
		changed = true
	}
	if !changed {
		return false, nil
	}
	// The movie lasts as long as its longest track.
	var longest uint64
	for _, trak := range moov.kids {
		if trak.typ != bmff.T("trak") || !trak.parsed() {
			continue
		}
		if tkhd := trak.child(bmff.T("tkhd")); tkhd != nil {
			if d, ok := headerDuration(tkhd.payload(), 20, 28); ok && d > longest {
				longest = d
			}
		}
	}
	if !setHeaderDuration(mvhd, 16, 24, longest) {
		return false, fmt.Errorf("%w: a movie header this package can't write", ErrUnsupported)
	}
	return true, nil
}

// headerTimescale reads a full box's timescale: at v0 in version 0, v1 in
// version 1.
func headerTimescale(p []byte, v0, v1 int) (uint32, bool) {
	at := v0
	if len(p) > 0 && p[0] == 1 {
		at = v1
	}
	if len(p) < at+4 {
		return 0, false
	}
	s := binary.BigEndian.Uint32(p[at:])
	return s, s > 0
}

// headerDuration reads a full box's duration: 32-bit at v0 in version 0,
// 64-bit at v1 in version 1.
func headerDuration(p []byte, v0, v1 int) (uint64, bool) {
	if len(p) > 0 && p[0] == 1 {
		if len(p) < v1+8 {
			return 0, false
		}
		return binary.BigEndian.Uint64(p[v1:]), true
	}
	if len(p) < v0+4 {
		return 0, false
	}
	return uint64(binary.BigEndian.Uint32(p[v0:])), true
}

// setHeaderDuration writes d as n's duration (see headerDuration). A
// version 0 header whose field can't hold it is refused.
func setHeaderDuration(n *qtNode, v0, v1 int, d uint64) bool {
	p := append([]byte(nil), n.payload()...)
	switch {
	case len(p) > 0 && p[0] == 1 && len(p) >= v1+8:
		binary.BigEndian.PutUint64(p[v1:], d)
	case len(p) > 0 && p[0] == 0 && len(p) >= v0+4 && d <= math.MaxUint32:
		binary.BigEndian.PutUint32(p[v0:], uint32(d))
	default:
		return false
	}
	n.setPayload(p)
	return true
}

// compositionSpan is the span of a track's composition times, from its
// decoding times (stts) and composition offsets (ctts, signed as
// muxers write them in either version): the earliest, and the latest
// end (a sample's composition time plus its duration). ok is false for
// a track without samples or whose tables don't cover the same samples.
func compositionSpan(stts, ctts []byte) (first, end int64, ok bool, err error) {
	if len(stts) < 8 || len(ctts) < 8 {
		return 0, 0, false, nil
	}
	nt, nc := binary.BigEndian.Uint32(stts[4:]), binary.BigEndian.Uint32(ctts[4:])
	if int64(nt)*8 > int64(len(stts)-8) || int64(nc)*8 > int64(len(ctts)-8) {
		return 0, 0, false, fmt.Errorf("%w: a time-to-sample table longer than its box", ErrMalformed)
	}
	first, end = math.MaxInt64, math.MinInt64
	var dts int64
	ti, ci := 0, 0
	var tLeft, cLeft uint32
	var delta uint32
	var off int32
	for {
		for tLeft == 0 && ti < int(nt) {
			tLeft, delta = binary.BigEndian.Uint32(stts[8+8*ti:]), binary.BigEndian.Uint32(stts[12+8*ti:])
			ti++
		}
		for cLeft == 0 && ci < int(nc) {
			cLeft, off = binary.BigEndian.Uint32(ctts[8+8*ci:]), int32(binary.BigEndian.Uint32(ctts[12+8*ci:]))
			ci++
		}
		if tLeft == 0 || cLeft == 0 {
			break
		}
		// k samples of the same duration and offset.
		k := min(tLeft, cLeft)
		lo := dts + int64(off)
		hi := dts + int64(k-1)*int64(delta) + int64(off) + int64(delta)
		first, end = min(first, lo), max(end, hi)
		dts += int64(k) * int64(delta)
		if dts > 1<<62 {
			return 0, 0, false, fmt.Errorf("%w: decoding times out of range", ErrUnsupported)
		}
		tLeft -= k
		cLeft -= k
	}
	if tLeft != 0 || cLeft != 0 || end == math.MinInt64 {
		return 0, 0, false, nil // tables of different sample counts: not touched
	}
	return first, end, true, nil
}

// rescale is v (in from units a second) in to units a second, rounded to
// the nearest.
func rescale(v int64, to, from uint32) (uint64, bool) {
	if v < 0 {
		return 0, false
	}
	n := new(big.Int).Mul(big.NewInt(v), big.NewInt(int64(to)))
	n.Add(n, big.NewInt(int64(from)/2))
	n.Quo(n, big.NewInt(int64(from)))
	if !n.IsUint64() {
		return 0, false
	}
	return n.Uint64(), true
}
