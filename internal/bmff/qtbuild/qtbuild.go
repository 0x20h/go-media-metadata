// Package qtbuild makes synthetic QuickTime and MP4 movies for tests: the
// box structure of a real file around random bytes as "media data", so a
// test can check that a write leaves the media data byte for byte as it
// was, and that the chunk offsets still point at it.
package qtbuild

import (
	"encoding/binary"
	"math/rand"
)

// KV is one metadata entry: a Keys name (com.apple.quicktime.…) or a user
// data box type ("\xa9xyz" for ©xyz), and its text.
type KV struct {
	Key, Value string
}

// Options shape a movie.
type Options struct {
	// Brand is the ftyp major brand; "" leaves ftyp out (an old QuickTime
	// file).
	Brand string
	// MoovFirst puts moov before mdat ("fast start"), else after it.
	MoovFirst bool
	// MvhdVersion and TkhdVersion are 0 (32-bit times) or 1 (64-bit).
	MvhdVersion, TkhdVersion uint8
	// Created is mvhd's creation time in seconds since 1904; Timescale and
	// Duration its duration.
	Created             uint64
	Timescale, Duration uint32
	// Matrix is the video track's a, b, c, d in 16.16 fixed point.
	Matrix [4]int32
	// MovieMatrix is the movie header's; all zero is the identity.
	MovieMatrix [4]int32
	// VideoTracks is how many video tracks there are (0 is one).
	VideoTracks int
	// Width and Height are the video track's, in pixels.
	Width, Height int
	// NoVideo leaves the video track out; AudioFirst puts an audio track
	// before it.
	NoVideo, AudioFirst bool
	// Keys are the moov meta box's Keys entries (handler mdta). KeysInUdta
	// puts that meta box in udta instead; FullBoxMeta writes it as an MP4
	// FullBox rather than a QuickTime container.
	Keys        []KV
	KeysInUdta  bool
	FullBoxMeta bool
	// Udta are QuickTime user data text entries in moov's udta.
	Udta []KV
	// MediaSize is how many random bytes mdat holds; the video track's
	// chunk offsets (stco, or co64 with Co64) point into it.
	MediaSize int
	Co64      bool
	// FreeAfterMoov adds a free box of that many bytes (header included,
	// at least 8) right after moov, as writers leave room to grow into.
	FreeAfterMoov int
	// Seed makes the media data.
	Seed int64
}

// Default is a portrait iPhone-style movie: moov after mdat, a video track
// turned 90°, Keys with date, place, make, model and a content identifier.
func Default() Options {
	return Options{
		Brand:     "qt  ",
		Created:   3845750400, // 2025-11-12 00:00 UTC
		Timescale: 600, Duration: 1800,
		Matrix: [4]int32{0, 1 << 16, -1 << 16, 0},
		Width:  1920, Height: 1080,
		AudioFirst: true,
		Keys: []KV{
			{"com.apple.quicktime.location.ISO6709", "+52.5200+013.4050+034.000/"},
			{"com.apple.quicktime.make", "Apple"},
			{"com.apple.quicktime.model", "iPhone 12 Pro"},
			{"com.apple.quicktime.creationdate", "2025-11-12T10:15:30+0100"},
			{"com.apple.quicktime.content.identifier", "6E2B5B8C-1F0A-4C3B-9D7E-2A1B3C4D5E6F"},
		},
		MediaSize: 4096,
		Seed:      1,
	}
}

func box(typ string, body ...[]byte) []byte {
	n := 8
	for _, b := range body {
		n += len(b)
	}
	out := binary.BigEndian.AppendUint32(make([]byte, 0, n), uint32(n))
	out = append(out, typ...)
	for _, b := range body {
		out = append(out, b...)
	}
	return out
}

func u16(v uint16) []byte { return binary.BigEndian.AppendUint16(nil, v) }
func u32(v uint32) []byte { return binary.BigEndian.AppendUint32(nil, v) }
func u64(v uint64) []byte { return binary.BigEndian.AppendUint64(nil, v) }

func cat(bs ...[]byte) []byte {
	var out []byte
	for _, b := range bs {
		out = append(out, b...)
	}
	return out
}

func mvhd(o Options) []byte {
	movie := o.MovieMatrix
	if movie == ([4]int32{}) {
		movie = [4]int32{1 << 16, 0, 0, 1 << 16}
	}
	var times []byte
	if o.MvhdVersion == 1 {
		times = cat(u64(o.Created), u64(o.Created), u32(o.Timescale), u64(uint64(o.Duration)))
	} else {
		times = cat(u32(uint32(o.Created)), u32(uint32(o.Created)), u32(o.Timescale), u32(o.Duration))
	}
	return box("mvhd", []byte{o.MvhdVersion, 0, 0, 0}, times,
		u32(1<<16), u16(1<<8), make([]byte, 10), // rate, volume, reserved
		matrix(movie),
		make([]byte, 24), u32(3)) // pre_defined, next track ID
}

func matrix(m [4]int32) []byte {
	return cat(u32(uint32(m[0])), u32(uint32(m[1])), u32(0),
		u32(uint32(m[2])), u32(uint32(m[3])), u32(0),
		u32(0), u32(0), u32(1<<30))
}

func trak(o Options, id uint32, handler string, chunk uint64) []byte {
	var times []byte
	if o.TkhdVersion == 1 {
		times = cat(u64(o.Created), u64(o.Created), u32(id), u32(0), u64(uint64(o.Duration)))
	} else {
		times = cat(u32(uint32(o.Created)), u32(uint32(o.Created)), u32(id), u32(0), u32(o.Duration))
	}
	m, w, h := [4]int32{1 << 16, 0, 0, 1 << 16}, 0, 0
	if handler == "vide" {
		m, w, h = o.Matrix, o.Width, o.Height
	}
	tkhd := box("tkhd", []byte{o.TkhdVersion, 0, 0, 7}, times, make([]byte, 8),
		u16(0), u16(0), u16(0), u16(0), matrix(m), u32(uint32(w)<<16), u32(uint32(h)<<16))
	hdlr := box("hdlr", u32(0), []byte("mhlr"), []byte(handler), make([]byte, 12), []byte{0})
	var offsets []byte
	if o.Co64 {
		offsets = box("co64", u32(0), u32(1), u64(chunk))
	} else {
		offsets = box("stco", u32(0), u32(1), u32(uint32(chunk)))
	}
	mdhd := box("mdhd", u32(0), u32(uint32(o.Created)), u32(uint32(o.Created)), u32(o.Timescale), u32(o.Duration), u16(0x55c4), u16(0))
	return box("trak", tkhd, box("mdia", mdhd, hdlr, box("minf", box("stbl", offsets))))
}

func keysMeta(o Options) []byte {
	if len(o.Keys) == 0 {
		return nil
	}
	hdlr := box("hdlr", u32(0), u32(0), []byte("mdta"), make([]byte, 12), []byte{0})
	keys := cat(u32(0), u32(uint32(len(o.Keys))))
	var items []byte
	for i, kv := range o.Keys {
		keys = append(keys, cat(u32(uint32(8+len(kv.Key))), []byte("mdta"), []byte(kv.Key))...)
		data := box("data", u32(1), u32(0), []byte(kv.Value))
		items = append(items, box(string(u32(uint32(i+1))), data)...)
	}
	body := cat(hdlr, box("keys", keys), box("ilst", items))
	if o.FullBoxMeta {
		return box("meta", u32(0), body)
	}
	return box("meta", body)
}

func udta(o Options, meta []byte) []byte {
	var entries []byte
	for _, kv := range o.Udta {
		// Key is the box type as bytes: "\xa9xyz" for ©xyz.
		entries = append(entries, box(kv.Key, u16(uint16(len(kv.Value))), u16(0x55c4), []byte(kv.Value))...)
	}
	if len(entries) == 0 && meta == nil {
		return nil
	}
	return box("udta", entries, meta)
}

// Build makes the movie, and returns where its media data is: mdat's
// payload as [start, end).
func Build(o Options) (file []byte, mediaStart, mediaEnd int64) {
	media := make([]byte, o.MediaSize)
	rand.New(rand.NewSource(o.Seed)).Read(media)
	var ftyp []byte
	if o.Brand != "" {
		ftyp = box("ftyp", []byte(o.Brand), u32(0), []byte(o.Brand), []byte("isom"))
	}
	moovFor := func(chunk uint64) []byte {
		var traks []byte
		if o.AudioFirst {
			traks = append(traks, trak(o, 2, "soun", chunk)...)
		}
		if !o.NoVideo {
			for i := range max(o.VideoTracks, 1) {
				traks = append(traks, trak(o, uint32(10+i), "vide", chunk)...)
			}
		}
		meta := keysMeta(o)
		var u []byte
		if o.KeysInUdta {
			u = udta(o, meta)
			meta = nil
		} else {
			u = udta(o, nil)
		}
		return box("moov", mvhd(o), traks, meta, u)
	}
	mdatHeader := 8
	var free []byte
	if o.FreeAfterMoov >= 8 {
		free = box("free", make([]byte, o.FreeAfterMoov-8))
	}
	if o.MoovFirst {
		moov := moovFor(0)
		start := len(ftyp) + len(moov) + len(free) + mdatHeader
		moov = moovFor(uint64(start))
		file = cat(ftyp, moov, free, box("mdat", media))
		return file, int64(start), int64(start + len(media))
	}
	start := len(ftyp) + mdatHeader
	file = cat(ftyp, box("mdat", media), moovFor(uint64(start)), free)
	return file, int64(start), int64(start + len(media))
}
