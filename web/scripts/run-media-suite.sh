#!/bin/sh
set -eu

mkdir -p "${TIDE_MEDIA_ARTIFACTS:-/artifacts}"

ready=0
attempt=0
while [ "${attempt}" -lt 60 ]; do
  if curl --fail --silent "${TIDE_MEDIA_BASE_URL:-http://tide:8080}/healthz" >/dev/null; then
    ready=1
    break
  fi
  attempt=$((attempt + 1))
  sleep 1
done
if [ "${ready}" -ne 1 ]; then
  echo "Tide did not become healthy within 60 seconds."
  exit 1
fi

cleanup_netem() {
  tc qdisc del dev eth0 root 2>/dev/null || true
}
trap cleanup_netem EXIT INT TERM

enable_netem() {
  tc qdisc add dev eth0 root netem \
    loss 3% \
    delay 60ms 20ms distribution normal \
    duplicate 1% \
    reorder 5% 50%
}

# The full gate: lifecycle and recording scenarios, the reliability suite, the
# seeded fuzzer, and a network-chaos rerun. All of it blocks every pull request
# rather than deferring anything to a nightly job — a regression that only a
# nightly catches has already shipped.
#
# The gate replays the curated regression seeds (web/e2e/media-regression-seeds.ts)
# and nothing else. Discovery — a seed nobody has run before — belongs to the
# nightly job. Seeding the gate from the date instead made it a different test
# every day: it failed run 327/328 on a finding unrelated to the branch under
# review, and a green run proved nothing about tomorrow's seed. A nightly
# finding earns a place in the gate by being fixed and promoted into the
# regression list, which is what makes a red gate mean "this branch broke it".
full_suite() {
  mkdir -p "${artifacts}/deterministic" "${artifacts}/fuzz" "${artifacts}/network-chaos"
  TIDE_MEDIA_ARTIFACTS="${artifacts}/deterministic" npm run media:e2e

  if [ "${TIDE_MEDIA_SUITE:-deterministic}" = "nightly" ] && [ -z "${FC_SEED:-}" ]; then
    FC_SEED="$(date -u +%Y%m%d)"
    export FC_SEED
  fi
  TIDE_MEDIA_ARTIFACTS="${artifacts}/fuzz" npm run media:fuzz

  enable_netem
  TIDE_MEDIA_ARTIFACTS="${artifacts}/network-chaos" npm run media:chaos
  cleanup_netem
}

artifacts="${TIDE_MEDIA_ARTIFACTS:-/artifacts}"

case "${TIDE_MEDIA_SUITE:-deterministic}" in
  deterministic | nightly)
    full_suite
    ;;
  quick)
    # Local shortcut only; never what CI runs.
    npm run media:e2e
    ;;
  fuzz)
    if [ "${TIDE_NETWORK_CHAOS:-false}" = "true" ]; then
      enable_netem
    fi
    npm run media:fuzz
    ;;
  *)
    echo "Unknown TIDE_MEDIA_SUITE=${TIDE_MEDIA_SUITE}"
    exit 2
    ;;
esac
