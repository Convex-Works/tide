#!/bin/sh
# Local media-gate loop. Unlike scripts/run-media-tests.sh (which builds the
# whole stack, runs the suite and tears everything down), this keeps the stack
# up between runs and rebuilds only what changed, so a spec edit costs seconds.
#
#   ./scripts/media-dev.sh media-lifecycle.spec.ts --project=chromium
#   ./scripts/media-dev.sh --project=chromium -g "later joiner"
#
# Stop the stack with: ./scripts/media-dev.sh --down
set -eu

repository_root="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
project_name="tide-media-dev"
compose_file="${repository_root}/deploy/media-test/compose.yaml"

compose() {
  docker compose --project-name "${project_name}" --file "${compose_file}" "$@"
}

if [ "${1:-}" = "--down" ]; then
  compose down --volumes --remove-orphans
  exit 0
fi

# The SPA is embedded in the Go binary, so changes under web/src need the tide
# image rebuilt; specs under web/e2e need the runner image rebuilt. Both layers
# are cached, so an unchanged tree is a no-op.
compose up -d --build dex minio minio-init tide egress
compose build runner

compose run --rm --entrypoint npx runner \
  playwright test "$@" --config=playwright.media.config.ts
