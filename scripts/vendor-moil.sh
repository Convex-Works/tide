#!/usr/bin/env bash
# Refreshes tide's vendored copy of moil from a moil checkout: the Go SDK
# (server/third_party/moil) and the transcribe bundle tide publishes
# (server/internal/transcripts/bundle). Only committed files are copied, so the
# recorded commit describes exactly what landed here.
#
#   scripts/vendor-moil.sh [path to a moil checkout]   (default: ../moil)
#
# A changed bundle has a new hash, which makes every machine owner review and
# approve it again: update the pinned hash in the transcripts tests on purpose.
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
source_repo=$(cd "${1:-$root/../moil}" && pwd)
commit=$(git -C "$source_repo" rev-parse HEAD)

sdk="$root/server/third_party/moil"
bundle="$root/server/internal/transcripts/bundle"
staging=$(mktemp -d)
trap 'rm -rf "$staging"' EXIT

git -C "$source_repo" archive "$commit" LICENSE sdk/go examples/bundles/transcribe |
	tar -x -C "$staging"

rm -rf "$sdk"
mkdir -p "$sdk"
(cd "$staging/sdk/go" && find . -type f ! -name '*_test.go' ! -path '*/testdata/*' |
	while read -r file; do
		mkdir -p "$sdk/$(dirname "$file")"
		cp "$file" "$sdk/$file"
	done)
cp "$staging/LICENSE" "$sdk/LICENSE"
printf '%s\n' "$commit" >"$sdk/COMMIT"

mkdir -p "$bundle"
for file in job.py job.py.lock manifest.json README.md; do
	cp "$staging/examples/bundles/transcribe/$file" "$bundle/$file"
done
printf '%s\n' "$commit" >"$bundle/COMMIT"

echo "Vendored moil $commit"
echo "Bundle hash: $(cd "$bundle" && shasum -a 256 job.py job.py.lock manifest.json | shasum -a 256 | cut -d' ' -f1)"
