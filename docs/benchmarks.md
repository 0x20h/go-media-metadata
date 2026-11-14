# Benchmarks against ExifTool

How fast this package reads and writes metadata, next to ExifTool doing
the same, measured with `tools/mmbench`. Not a CI gate: CI already guards
against regressions (`.github/benchgate`).

## Result

Apple M4 (10 cores, 16 GB), macOS 26.6, an APFS SSD; Go 1.26.3,
ExifTool 13.55. Test set: 140 files (20 per template), seed 1, manifest
`b25cfad2eb1e6ce9`; 3 rounds, so 420 runs of each operation per tool.

On this set, **reads are 16× faster and writes 4× faster** than ExifTool
in `-stay_open` mode, and 100 of the 140 writes could also have been done
in place.

### Read

| Format | Runs | mediameta files/s | p50 | p95 | ExifTool files/s | p50 | p95 | Speed-up |
|---|---|---|---|---|---|---|---|---|
| heic | 60 | 8456 | 92 µs | 266 µs | 242 | 3.9 ms | 4.2 ms | 35× |
| jpg | 180 | 2820 | 98 µs | 963 µs | 218 | 1.9 ms | 12.0 ms | 13× |
| mp4 | 60 | 23399 | 39 µs | 51 µs | 1291 | 759 µs | 870 µs | 18× |
| png | 60 | 19111 | 55 µs | 75 µs | 822 | 1.3 ms | 1.6 ms | 23× |
| webp | 60 | 21188 | 51 µs | 63 µs | 839 | 1.3 ms | 1.4 ms | 25× |
| all | 420 | 5286 | 76 µs | 936 µs | 332 | 1.4 ms | 10.5 ms | 16× |

### Write (a new file)

| Format | Runs | mediameta files/s | p50 | p95 | ExifTool files/s | p50 | p95 | Speed-up |
|---|---|---|---|---|---|---|---|---|
| heic | 60 | 821 | 702 µs | 4.2 ms | 153 | 5.1 ms | 8.8 ms | 5× |
| jpg | 180 | 582 | 652 µs | 2.9 ms | 148 | 4.0 ms | 12.8 ms | 4× |
| mp4 | 60 | 286 | 1.8 ms | 6.8 ms | 134 | 6.9 ms | 10.6 ms | 2× |
| png | 60 | 5500 | 169 µs | 286 µs | 281 | 3.6 ms | 4.2 ms | 20× |
| webp | 60 | 6807 | 136 µs | 209 µs | 444 | 2.3 ms | 2.4 ms | 15× |
| all | 420 | 686 | 566 µs | 4.0 ms | 175 | 4.1 ms | 11.8 ms | 4× |

### Writes this package could do in place

| Format | In place |
|---|---|
| heic | 10 of 20 |
| jpg | 45 of 60 |
| mp4 | 15 of 20 |
| png | 15 of 20 |
| webp | 15 of 20 |

## What's measured

- **Read:** the orientation, the date taken, the GPS position, the XMP
  title and keywords. For this package that's `ReadOrientation`,
  `ReadExif` and `ReadXMP` with `ParseXMP` (`ReadVideo` for an MP4).
  ExifTool gets the same tags with `-json -n -fast2`.
- **Write:** a new date taken (with its offset), a place, an XMP title
  and keywords, written to a new file. For this package that's
  `PlanMetadata` (`PlanVideoMetadata` for an MP4) and `WriteTo`, through
  a 1 MB buffer. ExifTool writes the same tags with `-o`.
- **In place:** the share of writes whose plan is `InPlace`. A caller
  that applies those to a copy-on-write clone instead of writing a new
  file does much less work than either column shows: a few bytes, not
  the whole file. That isn't timed here.
- **Fairness:** ExifTool runs with `-stay_open` for the whole run, so
  its Perl start-up isn't counted. Every file is read once, untimed,
  before timing starts, so neither tool pays for a cold cache the other
  then warms. Which tool goes first alternates by round. Times are wall
  time per file, as the caller sees them.

## The test set

`tools/mmbench gen` makes it from seven templates. The templates are
committed or generated with a seed; the files aren't committed.

| Template | Size |
|---|---|
| A phone HEIC with a gain map (`testdata/iphone`) | 1.6 MB |
| A camera JPEG, 2400×1800, with a 60 KB maker note | 1.5 MB |
| The same JPEG without metadata | 1.5 MB |
| The same JPEG with a 40 KB XMP packet (200 keywords, an edit history) | 1.5 MB |
| A PNG screenshot, 1920×1080 | about 100 KB |
| A WebP (`testdata/webp/lossy.webp`) | small |
| An MP4 with 8 MB of media data | 8 MB |

Each template is copied 20 times. Three of every four copies get seeded
random metadata (a date with an offset, a place, a title, keywords); the
fourth keeps the template's. `MANIFEST` lists every file's SHA-256, and
its own hash names the set in the report.

## Repeating it

```sh
go run ./tools/mmbench gen -dir /tmp/mmbench -n 20 -seed 1
go run ./tools/mmbench run -dir /tmp/mmbench -rounds 3
```

`run` needs `exiftool` on the PATH and prints the report in Markdown. The
same seed gives the same set (check the manifest hash). The set takes
about 280 MB; delete it afterwards.
