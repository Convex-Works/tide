#!/bin/sh
# Checks that the install docs and manifests pin the release about to be
# tagged, so nobody who follows them installs an older tide. Release runs
# it for every v* tag that isn't a pre-release; run it before tagging:
#
#   ./scripts/check-release-pins.sh 0.1.2
set -eu

if [ "$#" -ne 1 ]; then
  echo "usage: $0 <version>" >&2
  exit 2
fi
version="$1"
repository_root="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"

# The pins: the image tag, an overlay's newTag, and a clone of the tag.
pins='ghcr\.io/convex-works/tide:[0-9][0-9A-Za-z.-]*|newTag: [0-9][0-9A-Za-z.-]*|--branch v[0-9][0-9A-Za-z.-]*'

status=0
for file in README.md docs/DEPLOYMENT.md deploy/k8s/tide.yaml site/src/content/docs/index.md; do
  found="$(grep -Eo "$pins" "$repository_root/$file" || true)"
  if [ -z "$found" ]; then
    echo "$file: no tide version pin" >&2
    status=1
    continue
  fi
  printf '%s\n' "$found" | while IFS= read -r pin; do
    pinned="${pin##*[:v ]}"
    if [ "$pinned" != "$version" ]; then
      echo "$file: pins $pinned, not $version ($pin)" >&2
      exit 1
    fi
  done || status=1
done
exit "$status"
