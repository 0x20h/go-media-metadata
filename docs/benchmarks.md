# Benchmarks against ExifTool

How fast this package reads and writes metadata, next to ExifTool doing
the same, measured with `tools/mmbench`. Not a CI gate: CI already guards
against regressions (`.github/benchgate`).

## Result

Apple M4 (10 cores, 16 GB), macOS 26.6, an APFS SSD; Go 1.26.3,
ExifTool 13.55. Measured for v0.2.0. Test set: 140 files (20 per
template), seed 1, manifest `b25cfad2eb1e6ce9`; 3 rounds, so 420 runs of
each operation per tool.

On this set, **reading everything an importer needs (`ReadAll`) is 24×
faster than ExifTool reading the same tags (69× for HEIC)**. The narrower
read of orientation, date, GPS and XMP title and keywords is 15× faster,
and writes are 5× faster. 100 of the 140 writes could also have been done
in place.

### Read

| Format | Runs | mediameta files/s | p50 | p95 | ExifTool files/s | p50 | p95 | Speed-up |
|---|---|---|---|---|---|---|---|---|
| heic | 60 | 6532 | 116 µs | 281 µs | 217 | 4.2 ms | 4.8 ms | 30× |
| jpg | 180 | 2529 | 114 µs | 1.1 ms | 203 | 2.1 ms | 12.4 ms | 12× |
| mp4 | 60 | 18350 | 53 µs | 68 µs | 1061 | 941 µs | 1.1 ms | 17× |
| png | 60 | 16179 | 66 µs | 85 µs | 787 | 1.3 ms | 1.6 ms | 21× |
| webp | 60 | 17492 | 60 µs | 77 µs | 754 | 1.4 ms | 1.6 ms | 23× |
| all | 420 | 4627 | 89 µs | 985 µs | 305 | 1.5 ms | 11.3 ms | 15× |

### Read everything (ReadAll)

| Format | Runs | mediameta files/s | p50 | p95 | ExifTool files/s | p50 | p95 | Speed-up |
|---|---|---|---|---|---|---|---|---|
| heic | 60 | 12381 | 74 µs | 127 µs | 181 | 5.5 ms | 6.3 ms | 69× |
| jpg | 180 | 2775 | 90 µs | 997 µs | 141 | 2.7 ms | 18.6 ms | 20× |
| mp4 | 60 | 19587 | 49 µs | 59 µs | 766 | 1.3 ms | 1.4 ms | 26× |
| png | 60 | 20358 | 53 µs | 69 µs | 538 | 2.0 ms | 2.2 ms | 38× |
| webp | 60 | 13266 | 70 µs | 119 µs | 533 | 2.0 ms | 2.2 ms | 25× |
| all | 420 | 5234 | 73 µs | 933 µs | 220 | 2.1 ms | 16.7 ms | 24× |

### Write (a new file)

| Format | Runs | mediameta files/s | p50 | p95 | ExifTool files/s | p50 | p95 | Speed-up |
|---|---|---|---|---|---|---|---|---|
| heic | 60 | 1216 | 788 µs | 1.1 ms | 141 | 5.7 ms | 6.8 ms | 9× |
| jpg | 180 | 809 | 895 µs | 2.4 ms | 143 | 4.4 ms | 13.7 ms | 6× |
| mp4 | 60 | 349 | 2.4 ms | 3.6 ms | 125 | 7.5 ms | 10.2 ms | 3× |
| png | 60 | 4664 | 198 µs | 325 µs | 264 | 3.8 ms | 4.2 ms | 18× |
| webp | 60 | 4966 | 185 µs | 440 µs | 384 | 2.5 ms | 3.1 ms | 13× |
| all | 420 | 896 | 687 µs | 2.7 ms | 165 | 4.4 ms | 13.2 ms | 5× |

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
- **Read everything:** what `ReadAll` gives an importer, in one call:
  every date (with sub-seconds and offsets), GPS, camera, lens and
  exposure, size, orientation, title, description, keywords and rating
  from XMP, IPTC and Exif, face regions and people, and the content
  identifier (for an MP4 its duration and video fields). ExifTool gets the
  same tags with `-json -n -fast2 -struct` (the list is `readAllTags` in
  `tools/mmbench`).
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
