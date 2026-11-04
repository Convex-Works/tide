#!/usr/bin/env bash
# Refreshes tide's vendored copy of the moil Go SDK (server/third_party/moil)
# from a moil checkout. Only committed files are copied, so the recorded commit
# describes exactly what landed here.
#
#   scripts/vendor-moil.sh [path to a moil checkout]   (default: ../moil)
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
source_repo=$(cd "${1:-$root/../moil}" && pwd)
commit=$(git -C "$source_repo" rev-parse HEAD)

sdk="$root/server/third_party/moil"
staging=$(mktemp -d)
trap 'rm -rf "$staging"' EXIT

git -C "$source_repo" archive "$commit" LICENSE sdk/go |
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

echo "Vendored moil $commit"
