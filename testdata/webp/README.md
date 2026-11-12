Synthetic WebP files for the tests, 24×16 test patterns, no photos:

- `lossy.webp`: a simple lossy file (VP8).
- `lossless-alpha.webp`: a simple lossless file with alpha (VP8L).
- `lossy-alpha.webp`: an extended file (VP8X, ALPH, VP8).

Made with FFmpeg's `testsrc` and libwebp's `cwebp` (`-q 80`, or
`-lossless -exact`).
