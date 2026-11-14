// Command mmbench compares this package's speed with ExifTool's, on a test
// set it generates from committed templates.
//
//	go run ./tools/mmbench gen -dir /tmp/mmbench -n 20 -seed 1
//	go run ./tools/mmbench run -dir /tmp/mmbench
//
// gen writes, per template, n copies with seeded random metadata (a date,
// a place, a title, keywords; some copies keep none), and a manifest of
// their SHA-256 sums whose own hash names the set. run reads and writes
// every file with this package and with ExifTool in -stay_open mode (one
// Perl process for the whole run, so its start-up isn't counted), and
// prints a Markdown report: files per second, p50 and p95 per file, and the
// share of writes that this package could do in place.
//
// Both write a new file (ExifTool's -o), so they're compared on the same
// work; an in-place write of this package's, into a clone, does less.
package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	mediameta "github.com/0x20h/go-media-metadata"
	"github.com/0x20h/go-media-metadata/internal/bmff/qtbuild"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: mmbench gen|run [flags]")
		os.Exit(2)
	}
	fs := flag.NewFlagSet(os.Args[1], flag.ExitOnError)
	dir := fs.String("dir", "", "the test set's folder")
	n := fs.Int("n", 20, "gen: copies per template")
	seed := fs.Int64("seed", 1, "gen: the random seed")
	repo := fs.String("repo", ".", "gen: the repository, for its sample files")
	rounds := fs.Int("rounds", 3, "run: how often each file is read and written by each")
	_ = fs.Parse(os.Args[2:])
	if *dir == "" {
		fmt.Fprintln(os.Stderr, "-dir is required")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "gen":
		err = gen(*dir, *repo, *n, *seed)
	case "run":
		err = run(*dir, *rounds)
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// template is a file the test set is cloned from.
type template struct {
	name, ext string
	data      []byte
}

func templates(repo string, rng *rand.Rand) ([]template, error) {
	read := func(p string) ([]byte, error) { return os.ReadFile(filepath.Join(repo, p)) }
	heic, err := read("testdata/iphone/grid-gainmap-orientation6.heic")
	if err != nil {
		return nil, err
	}
	webp, err := read("testdata/webp/lossy.webp")
	if err != nil {
		return nil, err
	}
	photo := encodeJPEG(photoImage(2400, 1800, rng), 88)
	mov := qtbuild.Default()
	mov.Brand, mov.MediaSize, mov.Seed = "mp42", 8<<20, rng.Int63()
	mp4, _, _ := qtbuild.Build(mov)
	return []template{
		{"phone HEIC with a gain map", "heic", heic},
		{"camera JPEG with a 60 KB maker note", "jpg", withSegment(photo, app1(append([]byte("Exif\x00\x00"), makerNoteTIFF(rng)...)))},
		{"stripped JPEG", "jpg", photo},
		{"JPEG with a 40 KB XMP packet", "jpg", withSegment(photo, app1(append([]byte("http://ns.adobe.com/xap/1.0/\x00"), bigXMP(rng)...)))},
		{"PNG screenshot", "png", encodePNG(screenshot(1920, 1080, rng))},
		{"WebP", "webp", webp},
		{"MP4, 8 MB", "mp4", mp4},
	}, nil
}

// photoImage is a photo-like image: smooth gradients with noise.
func photoImage(w, h int, rng *rand.Rand) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			n := uint8(rng.Intn(24))
			img.Set(x, y, color.RGBA{uint8(x*255/w) + n, uint8(y*255/h) + n, 128 + n, 255})
		}
	}
	return img
}

// screenshot is a UI-like image: flat panels and lines of "text".
func screenshot(w, h int, rng *rand.Rand) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	bg := color.RGBA{245, 245, 247, 255}
	for y := range h {
		for x := range w {
			img.Set(x, y, bg)
		}
	}
	for range 40 {
		x0, y0 := rng.Intn(w-300), rng.Intn(h-200)
		c := color.RGBA{uint8(rng.Intn(255)), uint8(rng.Intn(255)), uint8(rng.Intn(255)), 255}
		for y := y0; y < y0+20+rng.Intn(180); y += 1 + rng.Intn(3) {
			for x := x0; x < x0+40+rng.Intn(260); x++ {
				img.Set(x, y, c)
			}
		}
	}
	return img
}

func encodeJPEG(img image.Image, q int) []byte {
	var b bytes.Buffer
	_ = jpeg.Encode(&b, img, &jpeg.Options{Quality: q})
	return b.Bytes()
}

func encodePNG(img image.Image) []byte {
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	return b.Bytes()
}

func app1(body []byte) []byte {
	return append([]byte{0xFF, 0xE1, byte((len(body) + 2) >> 8), byte(len(body) + 2)}, body...)
}

// withSegment puts a marker segment right after a JPEG's SOI.
func withSegment(jpg, seg []byte) []byte {
	return append(append(append([]byte(nil), jpg[:2]...), seg...), jpg[2:]...)
}

// makerNoteTIFF is camera-like Exif: Make, Model, a date and a 60 KB maker
// note, little-endian.
func makerNoteTIFF(rng *rand.Rand) []byte {
	le := binary.LittleEndian
	note := make([]byte, 60<<10)
	rng.Read(note)
	type entry struct {
		tag, typ uint16
		val      []byte
	}
	ifd0 := []entry{{0x010F, 2, []byte("Example\x00")}, {0x0110, 2, []byte("Camera X100\x00")}, {0x0112, 3, []byte{1, 0}}}
	exif := []entry{{0x9003, 2, []byte("2023:04:05 06:07:08\x00")}, {0x927C, 7, note}}
	var out []byte
	out = append(out, "II*\x00"...)
	out = le.AppendUint32(out, 8)
	write := func(es []entry, extra []entry, next uint32) []byte {
		at := len(out)
		all := append(append([]entry(nil), es...), extra...)
		size := 2 + 12*len(all) + 4
		data := at + size
		var vals []byte
		ifd := le.AppendUint16(nil, uint16(len(all)))
		for _, e := range all {
			count := len(e.val) // BYTE, ASCII, UNDEFINED
			switch e.typ {
			case 3: // SHORT
				count = len(e.val) / 2
			case 4: // LONG
				count = len(e.val) / 4
			}
			ifd = le.AppendUint16(ifd, e.tag)
			ifd = le.AppendUint16(ifd, e.typ)
			ifd = le.AppendUint32(ifd, uint32(count))
			if len(e.val) <= 4 {
				ifd = append(ifd, append(append([]byte(nil), e.val...), make([]byte, 4-len(e.val))...)...)
			} else {
				ifd = le.AppendUint32(ifd, uint32(data+len(vals)))
				vals = append(vals, e.val...)
				if len(vals)%2 == 1 {
					vals = append(vals, 0)
				}
			}
		}
		ifd = le.AppendUint32(ifd, next)
		return append(ifd, vals...)
	}
	// IFD0 at 8 points to the Exif IFD, written after it.
	probe := write(ifd0, []entry{{0x8769, 4, []byte{0, 0, 0, 0}}}, 0)
	exifAt := 8 + len(probe)
	out = append(out, write(ifd0, []entry{{0x8769, 4, le.AppendUint32(nil, uint32(exifAt))}}, 0)...)
	out = append(out, write(exif, nil, 0)...)
	return out
}

// bigXMP is an XMP packet of about 40 KB: a title, many keywords and a
// long edit history, as editing software leaves.
func bigXMP(rng *rand.Rand) []byte {
	var b strings.Builder
	b.WriteString(`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?><x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">`)
	b.WriteString(`<rdf:Description rdf:about="" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:xmpMM="http://ns.adobe.com/xap/1.0/mm/" xmlns:stEvt="http://ns.adobe.com/xap/1.0/sType/ResourceEvent#">`)
	b.WriteString(`<dc:title><rdf:Alt><rdf:li xml:lang="x-default">A photo</rdf:li></rdf:Alt></dc:title><dc:subject><rdf:Bag>`)
	for i := range 200 {
		fmt.Fprintf(&b, "<rdf:li>keyword %d %d</rdf:li>", i, rng.Intn(1000))
	}
	b.WriteString(`</rdf:Bag></dc:subject><xmpMM:History><rdf:Seq>`)
	for b.Len() < 40<<10 {
		fmt.Fprintf(&b, `<rdf:li stEvt:action="saved" stEvt:instanceID="xmp.iid:%016x" stEvt:when="2023-04-05T06:07:08+02:00" stEvt:softwareAgent="An Editor 1.0"/>`, rng.Uint64())
	}
	b.WriteString(`</rdf:Seq></xmpMM:History></rdf:Description></rdf:RDF></x:xmpmeta><?xpacket end="w"?>`)
	return []byte(b.String())
}

// randomEdit is seeded random metadata.
func randomEdit(rng *rand.Rand) (time.Time, mediameta.GPS, string, []string) {
	t := time.Date(2010+rng.Intn(15), time.Month(1+rng.Intn(12)), 1+rng.Intn(28), rng.Intn(24), rng.Intn(60), rng.Intn(60), 0,
		time.FixedZone("", (rng.Intn(25)-12)*3600))
	g := mediameta.GPS{Latitude: rng.Float64()*180 - 90, Longitude: rng.Float64()*360 - 180}
	title := fmt.Sprintf("Title %d", rng.Intn(100000))
	kw := make([]string, 1+rng.Intn(8))
	for i := range kw {
		kw[i] = fmt.Sprintf("kw%d", rng.Intn(500))
	}
	return t, g, title, kw
}

// apply writes the metadata into a file of ext.
func apply(data []byte, ext string, t time.Time, g mediameta.GPS, title string, kw []string) ([]byte, error) {
	var out bytes.Buffer
	var err error
	r := bytes.NewReader(data)
	if ext == "mp4" {
		err = mediameta.SetVideoMetadata(r, int64(len(data)), &out, mediameta.VideoEdit{Created: &t, GPS: &g})
	} else {
		err = mediameta.SetMetadata(r, int64(len(data)), &out, mediameta.MetadataEdit{
			Exif: &mediameta.ExifEdit{DateTimeOriginal: &t, OffsetKnown: true, GPS: &g},
			XMP:  &mediameta.XMPEdit{Title: &title, Keywords: &kw},
		})
	}
	return out.Bytes(), err
}

func gen(dir, repo string, n int, seed int64) error {
	rng := rand.New(rand.NewSource(seed))
	ts, err := templates(repo, rng)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	var manifest strings.Builder
	fmt.Fprintf(&manifest, "# mmbench test set, seed %d, %d per template\n", seed, n)
	for ti, tpl := range ts {
		for i := range n {
			data := tpl.data
			// Every fourth copy keeps the template's metadata as it is.
			if i%4 != 3 {
				t, g, title, kw := randomEdit(rng)
				if data, err = apply(tpl.data, tpl.ext, t, g, title, kw); err != nil {
					return fmt.Errorf("%s: %w", tpl.name, err)
				}
			}
			name := fmt.Sprintf("%d-%03d.%s", ti, i, tpl.ext)
			if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
				return err
			}
			sum := sha256.Sum256(data)
			fmt.Fprintf(&manifest, "%s  %s  %s\n", hex.EncodeToString(sum[:]), name, tpl.name)
		}
	}
	m := []byte(manifest.String())
	if err := os.WriteFile(filepath.Join(dir, "MANIFEST"), m, 0o644); err != nil {
		return err
	}
	sum := sha256.Sum256(m)
	fmt.Printf("%d files in %s; manifest %s\n", n*len(ts), dir, hex.EncodeToString(sum[:8]))
	return nil
}

// exiftool is an ExifTool process in -stay_open mode.
type exiftool struct {
	cmd *exec.Cmd
	in  io.WriteCloser
	out *bufio.Reader
}

func startExifTool() (*exiftool, error) {
	cmd := exec.Command("exiftool", "-stay_open", "True", "-@", "-")
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &exiftool{cmd: cmd, in: in, out: bufio.NewReaderSize(out, 1<<20)}, nil
}

// do runs one command (its arguments one per line) and waits for its end.
func (e *exiftool) do(args ...string) error {
	if _, err := io.WriteString(e.in, strings.Join(args, "\n")+"\n-execute\n"); err != nil {
		return err
	}
	for {
		line, err := e.out.ReadString('\n')
		if err != nil {
			return err
		}
		if strings.HasPrefix(line, "{ready") {
			return nil
		}
	}
}

func (e *exiftool) close() {
	_, _ = io.WriteString(e.in, "-stay_open\nFalse\n")
	_ = e.in.Close()
	_ = e.cmd.Wait()
}

type sample struct {
	format string
	d      time.Duration
}

type result struct {
	read, write [2][]sample // [0] this package, [1] ExifTool
	inPlace     map[string][2]int
}

func run(dir string, rounds int) error {
	m, err := os.ReadFile(filepath.Join(dir, "MANIFEST"))
	if err != nil {
		return fmt.Errorf("no test set in %s (run gen first): %w", dir, err)
	}
	sum := sha256.Sum256(m)
	var files []string
	for _, line := range strings.Split(string(m), "\n") {
		if f := strings.Fields(line); len(f) >= 2 && !strings.HasPrefix(line, "#") {
			files = append(files, f[1])
		}
	}
	et, err := startExifTool()
	if err != nil {
		return fmt.Errorf("exiftool: %w", err)
	}
	defer et.close()
	if err := et.do("-ver"); err != nil {
		return err
	}
	outDir, err := os.MkdirTemp("", "mmbench-out-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(outDir) }()

	// Every file read once first, untimed, so neither tool pays for a
	// cold cache the other then warms.
	for _, name := range files {
		if _, err := os.ReadFile(filepath.Join(dir, name)); err != nil {
			return err
		}
	}
	res := result{inPlace: map[string][2]int{}}
	rng := rand.New(rand.NewSource(2))
	for round := range rounds {
		for i, name := range files {
			if err := runFile(et, &res, dir, outDir, name, i, round, rng); err != nil {
				return err
			}
		}
	}
	fmt.Print(report(res, len(files), rounds, hex.EncodeToString(sum[:8])))
	return nil
}

// runFile reads and writes one file with each tool, which of them goes
// first alternating by round. In-place writes are counted in round 0.
func runFile(et *exiftool, res *result, dir, outDir, name string, i, round int, rng *rand.Rand) error {
	path := filepath.Join(dir, name)
	format := strings.TrimPrefix(filepath.Ext(name), ".")
	t, g, title, kw := randomEdit(rng)
	dst := filepath.Join(outDir, fmt.Sprintf("ours-%d.%s", i, format))
	edst := filepath.Join(outDir, fmt.Sprintf("et-%d.%s", i, format))
	defer func() { _ = os.Remove(dst); _ = os.Remove(edst) }()

	timed := func(into *[]sample, f func() error) error {
		start := time.Now()
		if err := f(); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		*into = append(*into, sample{format, time.Since(start)})
		return nil
	}
	oursRead := func() error { return readOurs(path, format) }
	etRead := func() error {
		return et.do("-json", "-n", "-fast2", "-Orientation", "-DateTimeOriginal", "-CreationDate", "-GPSLatitude",
			"-GPSLongitude", "-XMP-dc:Title", "-XMP-dc:Subject", path)
	}
	oursWrite := func() error {
		inPlace, err := writeOurs(path, dst, format, t, g, title, kw)
		if err == nil && round == 0 {
			c := res.inPlace[format]
			c[1]++
			if inPlace {
				c[0]++
			}
			res.inPlace[format] = c
		}
		return err
	}
	etWrite := func() error {
		// A new file (-o). No -q: it also silences the {ready} that ends
		// each command.
		args := []string{"-o", edst}
		if format == "mp4" {
			args = append(args, "-Keys:CreationDate="+t.Format("2006:01:02 15:04:05-07:00"),
				fmt.Sprintf("-Keys:GPSCoordinates=%f, %f", g.Latitude, g.Longitude))
		} else {
			args = append(args, "-DateTimeOriginal="+t.Format("2006:01:02 15:04:05"), "-OffsetTimeOriginal="+t.Format("-07:00"),
				fmt.Sprintf("-GPSLatitude=%f", g.Latitude), fmt.Sprintf("-GPSLongitude=%f", g.Longitude),
				fmt.Sprintf("-GPSLatitudeRef=%f", g.Latitude), fmt.Sprintf("-GPSLongitudeRef=%f", g.Longitude),
				"-XMP-dc:Title="+title, "-XMP-dc:Subject="+strings.Join(kw, ","), "-sep", ",")
		}
		return et.do(append(args, path)...)
	}
	steps := []struct {
		into *[]sample
		f    func() error
	}{{&res.read[0], oursRead}, {&res.read[1], etRead}, {&res.write[0], oursWrite}, {&res.write[1], etWrite}}
	if round%2 == 1 {
		steps[0], steps[1] = steps[1], steps[0]
		steps[2], steps[3] = steps[3], steps[2]
	}
	for _, st := range steps {
		if err := timed(st.into, st.f); err != nil {
			return err
		}
	}
	return nil
}

func readOurs(path, format string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if format == "mp4" {
		_, err := mediameta.ReadVideo(f, st.Size())
		return err
	}
	if _, err := mediameta.ReadOrientation(f, st.Size()); err != nil {
		return err
	}
	if _, err := mediameta.ReadExif(f, st.Size()); err != nil {
		return err
	}
	b, err := mediameta.ReadXMP(f, st.Size())
	if err == nil && b != nil {
		_, err = mediameta.ParseXMP(b)
	}
	return err
}

func writeOurs(src, dst, format string, t time.Time, g mediameta.GPS, title string, kw []string) (bool, error) {
	in, err := os.Open(src)
	if err != nil {
		return false, err
	}
	defer func() { _ = in.Close() }()
	st, err := in.Stat()
	if err != nil {
		return false, err
	}
	var p *mediameta.Plan
	if format == "mp4" {
		p, err = mediameta.PlanVideoMetadata(in, st.Size(), mediameta.VideoEdit{Created: &t, GPS: &g})
	} else {
		p, err = mediameta.PlanMetadata(in, st.Size(), mediameta.MetadataEdit{
			Exif: &mediameta.ExifEdit{DateTimeOriginal: &t, OffsetKnown: true, GPS: &g},
			XMP:  &mediameta.XMPEdit{Title: &title, Keywords: &kw},
		})
	}
	if err != nil {
		return false, err
	}
	out, err := os.Create(dst)
	if err != nil {
		return false, err
	}
	w := bufio.NewWriterSize(out, 1<<20)
	if _, err := p.WriteTo(w); err != nil {
		_ = out.Close()
		return false, err
	}
	if err := w.Flush(); err != nil {
		_ = out.Close()
		return false, err
	}
	return p.InPlace, out.Close()
}

func report(r result, n, rounds int, manifest string) string {
	w := &strings.Builder{}
	etVer, _ := exec.Command("exiftool", "-ver").Output()
	fmt.Fprintf(w, "Test set: %d files, manifest %s; %d rounds. Go %s, %s/%s, %d CPUs. ExifTool %s.\n\n",
		n, manifest, rounds, runtime.Version(), runtime.GOOS, runtime.GOARCH, runtime.NumCPU(), strings.TrimSpace(string(etVer)))
	formats := map[string]bool{}
	for _, s := range r.read[0] {
		formats[s.format] = true
	}
	var fl []string
	for f := range formats {
		fl = append(fl, f)
	}
	sort.Strings(fl)
	fl = append(fl, "all")
	for _, op := range []struct {
		name string
		s    [2][]sample
	}{{"Read", r.read}, {"Write (a new file)", r.write}} {
		fmt.Fprintf(w, "### %s\n\n| Format | Runs | mediameta files/s | p50 | p95 | ExifTool files/s | p50 | p95 | Speed-up |\n|---|---|---|---|---|---|---|---|---|\n", op.name)
		for _, f := range fl {
			a, b := pick(op.s[0], f), pick(op.s[1], f)
			fmt.Fprintf(w, "| %s | %d | %.0f | %s | %s | %.0f | %s | %s | %.0f× |\n", f, len(a),
				rate(a), pct(a, 50), pct(a, 95), rate(b), pct(b, 50), pct(b, 95), rate(a)/rate(b))
		}
		fmt.Fprintln(w)
	}
	fmt.Fprintf(w, "### Writes this package could do in place\n\n| Format | In place |\n|---|---|\n")
	for _, f := range fl[:len(fl)-1] {
		c := r.inPlace[f]
		fmt.Fprintf(w, "| %s | %d of %d |\n", f, c[0], c[1])
	}
	return w.String()
}

func pick(s []sample, format string) []time.Duration {
	var out []time.Duration
	for _, x := range s {
		if format == "all" || x.format == format {
			out = append(out, x.d)
		}
	}
	return out
}

func rate(d []time.Duration) float64 {
	var total time.Duration
	for _, x := range d {
		total += x
	}
	if total == 0 {
		return 0
	}
	return float64(len(d)) / total.Seconds()
}

func pct(d []time.Duration, p int) string {
	if len(d) == 0 {
		return "-"
	}
	s := append([]time.Duration(nil), d...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	v := s[(len(s)-1)*p/100]
	switch {
	case v < time.Millisecond:
		return fmt.Sprintf("%.0f µs", float64(v)/float64(time.Microsecond))
	default:
		return fmt.Sprintf("%.1f ms", float64(v)/float64(time.Millisecond))
	}
}
