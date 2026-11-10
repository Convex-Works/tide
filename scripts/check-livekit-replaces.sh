#!/bin/sh
# Checks that server/go.mod replaces the pion modules exactly as the
# livekit-server version it requires does. Replaces in a dependency's go.mod
# don't apply to tide, so tide mirrors them by hand, and Dependabot bumps
# livekit-server without touching them. A stale fork still builds and
# passes the tests, so nothing else notices.
set -eu

repository_root="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
cd "$repository_root/server"

version="$(go list -m -f '{{.Version}}' github.com/livekit/livekit-server)"
upstream="$(go mod download -json "github.com/livekit/livekit-server@$version" |
  sed -n 's/^[[:space:]]*"GoMod": "\(.*\)",*$/\1/p')"
if [ -z "$upstream" ]; then
  echo "can't download livekit-server $version's go.mod" >&2
  exit 1
fi

# The pion replaces of a go.mod, one "old => new version" per line, in both
# the single-line and the block form.
pion_replaces() {
  awk '
    /^replace[[:space:]]*\(/ { block = 1; next }
    block && /^\)/ { block = 0; next }
    /^replace[[:space:]]/ { sub(/^replace[[:space:]]+/, ""); print; next }
    block { print }
  ' "$1" | sed 's|//.*||' | awk '$1 ~ /^github\.com\/pion\// { $1 = $1; print }' | sort
}

ours="$(pion_replaces go.mod)"
theirs="$(pion_replaces "$upstream")"
if [ "$ours" != "$theirs" ]; then
  echo "server/go.mod's pion replaces differ from livekit-server $version's." >&2
  echo "tide has:" >&2
  printf '%s\n' "$ours" | sed 's/^/  /' >&2
  echo "livekit-server $version has:" >&2
  printf '%s\n' "$theirs" | sed 's/^/  /' >&2
  exit 1
fi
