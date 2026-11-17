package mediameta

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/0x20h/go-media-metadata/internal/bmff"
	"github.com/0x20h/go-media-metadata/internal/bmff/qtbuild"
)

// A movie of 4 MB with moov last, rewritten in memory.
func BenchmarkRewriteMovie(b *testing.B) {
	src := assembleMovieSized(b, 4<<20)
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	for range b.N {
		if err := RewriteMovie(bytes.NewReader(src), int64(len(src)), io.Discard, RewriteOptions{StripMetadata: true}); err != nil {
			b.Fatal(err)
		}
	}
}

// A movie of MEDIAMETA_BENCH_MOVIE_MB megabytes (1024 for 1 GB) with moov
// last, rewritten from one file on disk to another; set to run.
func BenchmarkRewriteMovieFile(b *testing.B) {
	mb, _ := strconv.Atoi(os.Getenv("MEDIAMETA_BENCH_MOVIE_MB"))
	if mb <= 0 {
		b.Skip("MEDIAMETA_BENCH_MOVIE_MB not set")
	}
	dir := b.TempDir()
	srcPath := filepath.Join(dir, "src.mov")
	writeMovieFile(b, srcPath, int64(mb)<<20)
	src, err := os.Open(srcPath)
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = src.Close() }()
	st, _ := src.Stat()
	b.SetBytes(st.Size())
	for range b.N {
		dst, err := os.Create(filepath.Join(dir, "dst.mov"))
		if err != nil {
			b.Fatal(err)
		}
		if err := RewriteMovie(src, st.Size(), dst, RewriteOptions{StripMetadata: true}); err != nil {
			b.Fatal(err)
		}
		if err := dst.Close(); err != nil {
			b.Fatal(err)
		}
	}
}

// writeMovie writes a built movie with moov last whose mdat holds its
// media data and then pad more bytes.
func writeMovie(tb testing.TB, w io.Writer, pad int64) {
	tb.Helper()
	o := qtbuild.Default()
	o.MediaSize = 4096
	src, ms, me := qtbuild.Build(o)
	top, err := bmff.ReadBoxes(bytes.NewReader(src), 0, int64(len(src)))
	if err != nil {
		tb.Fatal(err)
	}
	ftyp, _ := bmff.Find(top, bmff.T("ftyp"))
	moovB, _ := bmff.Find(top, bmff.T("moov"))
	moov, err := parseQTNode(src[moovB.Offset:moovB.End()], 0)
	if err != nil {
		tb.Fatal(err)
	}
	body := (me - ms) + pad
	hdr := bmff.AppendHeader(nil, bmff.T("mdat"), int(body), false)
	tables, err := chunkTables(moov)
	if err != nil {
		tb.Fatal(err)
	}
	for _, t := range tables {
		t.mapped = make([]int64, len(t.offsets))
		for i, v := range t.offsets {
			t.mapped[i] = ftyp.End() + int64(len(hdr)) + v - ms
		}
		t.write()
	}
	zeros := make([]byte, 1<<20)
	write := func(b []byte) {
		if _, err := w.Write(b); err != nil {
			tb.Fatal(err)
		}
	}
	write(src[ftyp.Offset:ftyp.End()])
	write(hdr)
	write(src[ms:me])
	for left := pad; left > 0; left -= int64(len(zeros)) {
		write(zeros[:min(left, int64(len(zeros)))])
	}
	write(moov.encode())
}

func assembleMovieSized(tb testing.TB, size int64) []byte {
	var b bytes.Buffer
	writeMovie(tb, &b, size)
	return b.Bytes()
}

func writeMovieFile(tb testing.TB, path string, size int64) {
	f, err := os.Create(path)
	if err != nil {
		tb.Fatal(err)
	}
	writeMovie(tb, f, size)
	if err := f.Close(); err != nil {
		tb.Fatal(err)
	}
}
