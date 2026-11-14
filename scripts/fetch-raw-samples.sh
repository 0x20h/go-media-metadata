#!/bin/sh
# Downloads the camera RAW samples listed in testdata/raw-samples.txt
# (CC0, from raw.pixls.us) into testdata/raw-samples/, checks their
# SHA-256 sums, and runs the real-file tests on them. Files already there
# with the right sum aren't downloaded again.
#
#   scripts/fetch-raw-samples.sh            # download, check, test
#   scripts/fetch-raw-samples.sh -n         # download and check only
set -eu
cd "$(dirname "$0")/.."
dir=testdata/raw-samples
mkdir -p "$dir"

sum() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | cut -d' ' -f1
	else
		shasum -a 256 "$1" | cut -d' ' -f1
	fi
}

failed=0
grep -v '^#' testdata/raw-samples.txt | while read -r name want url; do
	[ -n "$name" ] || continue
	f="$dir/$name"
	if [ ! -f "$f" ] || [ "$(sum "$f")" != "$want" ]; then
		echo "fetching $name"
		curl -fsSL --retry 3 -o "$f.part" "$url"
		mv "$f.part" "$f"
	fi
	if [ "$(sum "$f")" != "$want" ]; then
		echo "$name: SHA-256 mismatch" >&2
		rm -f "$f"
		exit 1
	fi
done

echo "all samples present and checked in $dir"
[ "${1:-}" = "-n" ] && exit 0
MEDIAMETA_RAW_DIR="$dir" go test -count=1 -run 'TestRealRAW' -v . | grep -E '^(=== RUN|--- |ok|FAIL|PASS)|raw_test|exiftool' || true
MEDIAMETA_RAW_DIR="$dir" go test -count=1 -run 'TestRealRAW' .
