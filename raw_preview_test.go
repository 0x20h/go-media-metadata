package mediameta

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// sizedJPEG is a baseline JPEG of w×h pixels.
func sizedJPEG(t testing.TB, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), uint8(w), 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 75}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// losslessJPEG is the start of raw data stored as lossless JPEG (SOF3),
// as DNG and CR2 store it: not a preview.
var losslessJPEG = append([]byte{0xFF, 0xD8, 0xFF, 0xC3, 0x00, 0x0B, 0x0C, 0x01, 0x00, 0x02, 0x00, 0x01, 0x01, 0x11, 0x00}, bytes.Repeat([]byte{0x5A}, 512)...)

// rawLayout lays out a TIFF file for tests: blobs first, then IFDs, each
// IFD written after the ones it points to.
type rawLayout struct {
	order binary.ByteOrder
	buf   []byte
}

type ent struct {
	tag, typ uint16
	count    uint32
	value    []byte
}

func newRawLayout(order binary.ByteOrder, header []byte) *rawLayout {
	return &rawLayout{order: order, buf: bytes.Clone(header)}
}

func (w *rawLayout) blob(b []byte) uint32 {
	at := uint32(len(w.buf))
	w.buf = append(w.buf, b...)
	if len(w.buf)%2 == 1 {
		w.buf = append(w.buf, 0)
	}
	return at
}

func (w *rawLayout) long(v uint32) []byte {
	return w.order.(binary.AppendByteOrder).AppendUint32(nil, v)
}
func (w *rawLayout) short(v uint16) []byte {
	return w.order.(binary.AppendByteOrder).AppendUint16(nil, v)
}

func (w *rawLayout) longs(vs ...uint32) ent0 {
	var b []byte
	for _, v := range vs {
		b = append(b, w.long(v)...)
	}
	return ent0{typLong, uint32(len(vs)), b}
}

type ent0 struct {
	typ   uint16
	count uint32
	value []byte
}

const typLong = typeLong

// ifd writes an IFD (entries sorted by tag) and returns its offset.
func (w *rawLayout) ifd(next uint32, es ...ent) uint32 {
	for i := 1; i < len(es); i++ {
		for j := i; j > 0 && es[j].tag < es[j-1].tag; j-- {
			es[j], es[j-1] = es[j-1], es[j]
		}
	}
	var extra []uint32
	for _, e := range es {
		if len(e.value) > 4 {
			extra = append(extra, w.blob(e.value))
		} else {
			extra = append(extra, 0)
		}
	}
	at := uint32(len(w.buf))
	w.buf = append(w.buf, w.short(uint16(len(es)))...)
	for i, e := range es {
		w.buf = append(w.buf, w.short(e.tag)...)
		w.buf = append(w.buf, w.short(e.typ)...)
		w.buf = append(w.buf, w.long(e.count)...)
		if len(e.value) > 4 {
			w.buf = append(w.buf, w.long(extra[i])...)
		} else {
			w.buf = append(w.buf, append(bytes.Clone(e.value), make([]byte, 4-len(e.value))...)...)
		}
	}
	w.buf = append(w.buf, w.long(next)...)
	return at
}

func (w *rawLayout) finish(off uint32) []byte {
	w.order.PutUint32(w.buf[4:], off)
	return w.buf
}

func (w *rawLayout) e(tag uint16, v ent0) ent { return ent{tag, v.typ, v.count, v.value} }
func (w *rawLayout) s(tag, v uint16) ent      { return ent{tag, typeShort, 1, w.short(v)} }
func (w *rawLayout) l(tag uint16, v uint32) ent {
	return ent{tag, typeLong, 1, w.long(v)}
}
func asciiEnt(tag uint16, s string) ent {
	return ent{tag, typeASCII, uint32(len(s) + 1), append([]byte(s), 0)}
}

// rawSample is a synthetic RAW and the previews it holds.
type rawSample struct {
	name, ext string
	file      []byte
	previews  [][]byte // largest first
}

func rawSamples(t testing.TB) []rawSample {
	big, mid, small := sizedJPEG(t, 96, 64), sizedJPEG(t, 64, 48), sizedJPEG(t, 32, 24)
	rgbThumb := bytes.Repeat([]byte{0x80}, 16*12*3)
	le := binary.LittleEndian
	var out []rawSample

	// DNG: IFD0 an uncompressed thumbnail; SubIFDs the raw (lossless
	// JPEG, CFA) and a JPEG preview.
	{
		w := newRawLayout(le, []byte("II*\x00\x00\x00\x00\x00"))
		thumb, raw, prev := w.blob(rgbThumb), w.blob(losslessJPEG), w.blob(mid)
		rawIFD := w.ifd(0, w.l(tagNewSubfileType, 0), w.s(tagCompression, 7), w.s(0x0106, 32803),
			w.l(tagStripOffsets, raw), w.l(tagStripByteCounts, uint32(len(losslessJPEG))))
		prevIFD := w.ifd(0, w.l(tagNewSubfileType, 1), w.s(tagCompression, 7), w.s(0x0106, 6),
			w.l(0x0100, 64), w.l(0x0101, 48),
			w.l(tagStripOffsets, prev), w.l(tagStripByteCounts, uint32(len(mid))))
		ifd0 := w.ifd(0, w.l(tagNewSubfileType, 1), w.s(tagCompression, 1), w.s(0x0106, 2),
			w.l(0x0100, 16), w.l(0x0101, 12), asciiEnt(0x010F, "Example"), asciiEnt(0x0110, "Camera"),
			w.l(tagStripOffsets, thumb), w.l(tagStripByteCounts, uint32(len(rgbThumb))),
			w.e(tagSubIFDs, w.longs(rawIFD, prevIFD)),
			ent{0xC612, typeByte, 4, []byte{1, 4, 0, 0}}) // DNGVersion
		out = append(out, rawSample{"DNG", "dng", w.finish(ifd0), [][]byte{mid}})
	}
	// CR2: IFD0 the full-size JPEG as a strip (Compression 6), IFD1 a
	// thumbnail (JPEGInterchangeFormat), IFD3 the raw (lossless JPEG).
	{
		w := newRawLayout(le, []byte("II*\x00\x00\x00\x00\x00CR\x02\x00\x00\x00\x00\x00"))
		full, thumb, raw := w.blob(big), w.blob(small), w.blob(losslessJPEG)
		ifd3 := w.ifd(0, w.s(tagCompression, 6), w.l(tagStripOffsets, raw), w.l(tagStripByteCounts, uint32(len(losslessJPEG))))
		ifd2 := w.ifd(ifd3, w.s(tagCompression, 1), w.l(0x0100, 16), w.l(0x0101, 12))
		ifd1 := w.ifd(ifd2, w.s(tagCompression, 6), w.l(tagJPEGOffset, thumb), w.l(tagJPEGLength, uint32(len(small))))
		ifd0 := w.ifd(ifd1, w.s(tagCompression, 6), asciiEnt(0x010F, "Canon"), asciiEnt(0x0110, "Canon EOS"),
			w.l(tagStripOffsets, full), w.l(tagStripByteCounts, uint32(len(big))))
		file := w.finish(ifd0)
		le.PutUint32(file[12:], ifd3)
		out = append(out, rawSample{"CR2", "cr2", file, [][]byte{big, small}})
	}
	// NEF: IFD0 an uncompressed thumbnail; SubIFDs a JpgFromRaw and the raw.
	{
		w := newRawLayout(binary.BigEndian, []byte("MM\x00*\x00\x00\x00\x00"))
		thumb, jpg, raw := w.blob(rgbThumb), w.blob(big), w.blob(bytes.Repeat([]byte{0x11}, 512))
		sub1 := w.ifd(0, w.l(tagNewSubfileType, 1), w.s(tagCompression, 6), w.l(tagJPEGOffset, jpg), w.l(tagJPEGLength, uint32(len(big))))
		sub2 := w.ifd(0, w.l(tagNewSubfileType, 0), w.s(tagCompression, 34713), w.l(tagStripOffsets, raw), w.l(tagStripByteCounts, 512))
		ifd0 := w.ifd(0, w.l(tagNewSubfileType, 1), w.s(tagCompression, 1), asciiEnt(0x010F, "NIKON CORPORATION"), asciiEnt(0x0110, "NIKON D1"),
			w.l(tagStripOffsets, thumb), w.l(tagStripByteCounts, uint32(len(rgbThumb))),
			w.e(tagSubIFDs, w.longs(sub1, sub2)))
		out = append(out, rawSample{"NEF", "nef", w.finish(ifd0), [][]byte{big}})
	}
	// ARW: IFD0 a preview (JPEGInterchangeFormat), IFD1 a thumbnail.
	{
		w := newRawLayout(le, []byte("II*\x00\x00\x00\x00\x00"))
		prev, thumb := w.blob(mid), w.blob(small)
		ifd1 := w.ifd(0, w.s(tagCompression, 6), w.l(tagJPEGOffset, thumb), w.l(tagJPEGLength, uint32(len(small))))
		ifd0 := w.ifd(ifd1, asciiEnt(0x010F, "SONY"), asciiEnt(0x0110, "ILCE-7"), w.l(tagJPEGOffset, prev), w.l(tagJPEGLength, uint32(len(mid))))
		out = append(out, rawSample{"ARW", "arw", w.finish(ifd0), [][]byte{mid, small}})
	}
	// RAF: the header points to a JPEG whose Exif has a thumbnail.
	{
		embedded := jpegWithThumbnail(t, big, small)
		h := make([]byte, 92)
		copy(h, rafMagic)
		binary.BigEndian.PutUint32(h[84:], 100)
		binary.BigEndian.PutUint32(h[88:], uint32(len(embedded)))
		file := append(append(h, make([]byte, 8)...), embedded...)
		out = append(out, rawSample{"RAF", "raf", file, [][]byte{embedded, small}})
	}
	// RW2: header "IIU\0", the JPEG (with a thumbnail) as JpgFromRaw.
	{
		embedded := jpegWithThumbnail(t, mid, small)
		w := newRawLayout(le, []byte("IIU\x00\x00\x00\x00\x00"))
		ifd0 := w.ifd(0, asciiEnt(0x010F, "Panasonic"), ent{tagJpgFromRaw, typeUndef, uint32(len(embedded)), embedded})
		out = append(out, rawSample{"RW2", "rw2", w.finish(ifd0), [][]byte{embedded, small}})
	}
	// CR3: THMB in the metadata uuid box, PRVW in its own, the full-size
	// JPEG as the first sample of a track.
	out = append(out, cr3Sample(t, big, mid, small))
	return out
}

// jpegWithThumbnail is main with an Exif segment whose IFD1 holds thumb.
func jpegWithThumbnail(t testing.TB, main, thumb []byte) []byte {
	w := newRawLayout(binary.LittleEndian, []byte("II*\x00\x00\x00\x00\x00"))
	at := w.blob(thumb)
	ifd1 := w.ifd(0, w.s(tagCompression, 6), w.l(tagJPEGOffset, at), w.l(tagJPEGLength, uint32(len(thumb))))
	ifd0 := w.ifd(ifd1, w.s(tagOrientation, 1))
	tiff := w.finish(ifd0)
	return append(append([]byte{0xFF, 0xD8}, app1(append(append([]byte(nil), exifHeader...), tiff...))...), main[2:]...)
}

func cr3Sample(t testing.TB, full, prvw, thmb []byte) rawSample {
	box := func(typ string, body ...[]byte) []byte {
		n := 8
		for _, b := range body {
			n += len(b)
		}
		out := binary.BigEndian.AppendUint32(nil, uint32(n))
		out = append(out, typ...)
		for _, b := range body {
			out = append(out, b...)
		}
		return out
	}
	u32 := func(v uint32) []byte { return binary.BigEndian.AppendUint32(nil, v) }
	// A 16-byte header with the JPEG's length at lenAt, then the JPEG and
	// some padding, as Canon writes them.
	headed := func(typ string, lenAt int, jpg []byte) []byte {
		h := make([]byte, 16)
		binary.BigEndian.PutUint32(h[lenAt:], uint32(len(jpg)))
		return box(typ, h, jpg, []byte{0, 0, 0})
	}
	ftyp := box("ftyp", []byte("crx "), u32(1), []byte("crx isom"))
	thmbBox := headed("THMB", 8, thmb)
	meta := box("uuid", cr3MetaUUID[:], thmbBox)
	trak := func(chunk uint32) []byte {
		stsz := box("stsz", u32(0), u32(0), u32(1), u32(uint32(len(full))))
		stco := box("stco", u32(0), u32(1), u32(chunk))
		return box("trak", box("mdia", box("minf", box("stbl", stsz, stco))))
	}
	moovFor := func(chunk uint32) []byte { return box("moov", meta, trak(chunk)) }
	prvwBox := box("uuid", cr3PreviewUUID[:], make([]byte, 8), headed("PRVW", 12, prvw))
	head := append(append(ftyp, moovFor(0)...), prvwBox...)
	chunk := uint32(len(head) + 8)
	file := append(append(append(ftyp, moovFor(chunk)...), prvwBox...), box("mdat", full)...)
	return rawSample{"CR3", "cr3", file, [][]byte{full, prvw, thmb}}
}

func TestReadPreviews(t *testing.T) {
	for _, s := range rawSamples(t) {
		ps, err := ReadPreviews(bytes.NewReader(s.file), int64(len(s.file)))
		if err != nil {
			t.Fatalf("%s: %v", s.name, err)
		}
		if len(ps) != len(s.previews) {
			t.Fatalf("%s: %d previews %+v, want %d", s.name, len(ps), ps, len(s.previews))
		}
		for i, p := range ps {
			if !bytes.Equal(s.file[p.Offset:p.Offset+p.Length], s.previews[i]) {
				t.Errorf("%s: preview %d at %d isn't the one embedded", s.name, i, p.Offset)
			}
			cfg, err := jpeg.DecodeConfig(bytes.NewReader(s.previews[i]))
			if err != nil || cfg.Width != p.Width || cfg.Height != p.Height {
				t.Errorf("%s: preview %d is %d×%d, want %d×%d", s.name, i, p.Width, p.Height, cfg.Width, cfg.Height)
			}
		}
		// The Exif reader still reads these files.
		if _, err := ReadExif(bytes.NewReader(s.file), int64(len(s.file))); err != nil {
			t.Errorf("%s: ReadExif: %v", s.name, err)
		}
	}
}

// Reading previews reads IFDs and the heads of the candidates, not image
// data. A preview whose frame header lies past 4 KB is still found.
func TestReadPreviewsReadsLittle(t *testing.T) {
	for _, s := range rawSamples(t) {
		cr := &countingReader{r: bytes.NewReader(s.file)}
		if _, err := ReadPreviews(cr, int64(len(s.file))); err != nil {
			t.Fatal(err)
		}
		// The test files are small, so the 4 KB heads cover the previews;
		// the IFDs are what's read besides.
		var heads int64
		for _, p := range s.previews {
			heads += int64(min(len(p), 4096))
		}
		if cr.bytes > heads+4096 {
			t.Errorf("%s: read %d bytes, more than the previews' heads (%d) and the IFDs", s.name, cr.bytes, heads)
		}
		t.Logf("%s: %d of %d bytes in %d reads", s.name, cr.bytes, len(s.file), cr.calls)
	}
}

func TestReadPreviewsNotRAW(t *testing.T) {
	jpg := plainJPEG(t)
	if _, err := ReadPreviews(bytes.NewReader(jpg), int64(len(jpg))); err != ErrNotRAW {
		t.Errorf("a JPEG: %v", err)
	}
}

func FuzzReadPreviews(f *testing.F) {
	for _, s := range rawSamples(f) {
		f.Add(s.file)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		withinDeadline(t, func() {
			ps, err := ReadPreviews(bytes.NewReader(data), int64(len(data)))
			if err != nil {
				return
			}
			for _, p := range ps {
				if p.Offset <= 0 || p.Length <= 0 || p.Offset+p.Length > int64(len(data)) || p.Width <= 0 || p.Height <= 0 {
					t.Fatalf("preview %+v in a file of %d bytes", p, len(data))
				}
			}
		})
	})
}

// Every preview ExifTool extracts is one ReadPreviews finds. Runs where
// exiftool is installed.
func TestReadPreviewsExifTool(t *testing.T) {
	if _, err := exec.LookPath("exiftool"); err != nil {
		t.Skip("exiftool not installed")
	}
	dir := t.TempDir()
	for _, s := range rawSamples(t) {
		path := filepath.Join(dir, "sample."+s.ext)
		if err := os.WriteFile(path, s.file, 0o600); err != nil {
			t.Fatal(err)
		}
		ps, err := ReadPreviews(bytes.NewReader(s.file), int64(len(s.file)))
		if err != nil {
			t.Fatal(err)
		}
		found := 0
		for _, tag := range []string{"PreviewImage", "JpgFromRaw", "ThumbnailImage", "OtherImage"} {
			got, err := exec.Command("exiftool", "-b", "-"+tag, path).Output()
			if err != nil || len(got) == 0 || !bytes.HasPrefix(got, []byte{0xFF, 0xD8}) {
				continue
			}
			found++
			ok := false
			for _, p := range ps {
				ours := s.file[p.Offset : p.Offset+p.Length]
				ok = ok || bytes.Equal(got, ours) ||
					bytes.HasPrefix(got, ours) && len(bytes.Trim(got[len(ours):], "\x00")) == 0
			}
			if !ok {
				t.Errorf("%s: ExifTool's %s (%d bytes) isn't a preview found", s.name, tag, len(got))
			}
		}
		if found == 0 {
			t.Errorf("%s: ExifTool extracts no JPEG preview", s.name)
		}
		t.Logf("%s: ExifTool extracts %d JPEG previews; ReadPreviews finds %d", s.name, found, len(ps))
	}
}

// A preview whose marker segments run past the first 4 KB (a big Exif
// segment) is read further.
func TestReadPreviewsBigHeader(t *testing.T) {
	small := sizedJPEG(t, 40, 30)
	pad := bytes.Repeat([]byte{0}, 9000)
	app := append([]byte{0xFF, 0xE1, byte((len(pad) + 2) >> 8), byte(len(pad) + 2)}, pad...)
	jpg := append(append([]byte{0xFF, 0xD8}, app...), small[2:]...)
	w := newRawLayout(binary.LittleEndian, []byte("II*\x00\x00\x00\x00\x00"))
	at := w.blob(jpg)
	file := w.finish(w.ifd(0, w.l(tagJPEGOffset, at), w.l(tagJPEGLength, uint32(len(jpg)))))
	ps, err := ReadPreviews(bytes.NewReader(file), int64(len(file)))
	if err != nil || len(ps) != 1 || ps[0].Width != 40 || ps[0].Height != 30 {
		t.Errorf("%+v, %v", ps, err)
	}
}

// A large preview costs its first few KB to find, not its size.
func TestReadPreviewsLargeReadsLittle(t *testing.T) {
	big := sizedJPEG(t, 1600, 1200)
	w := newRawLayout(binary.LittleEndian, []byte("II*\x00\x00\x00\x00\x00"))
	at := w.blob(big)
	file := w.finish(w.ifd(0, asciiEnt(0x010F, "SONY"), w.l(tagJPEGOffset, at), w.l(tagJPEGLength, uint32(len(big)))))
	cr := &countingReader{r: bytes.NewReader(file)}
	ps, err := ReadPreviews(cr, int64(len(file)))
	if err != nil || len(ps) != 1 || ps[0].Width != 1600 {
		t.Fatalf("%+v, %v", ps, err)
	}
	if cr.bytes > 8<<10 {
		t.Errorf("read %d bytes of a %d-byte preview", cr.bytes, len(big))
	}
	t.Logf("%d bytes read for a %d-byte preview", cr.bytes, len(big))
}
