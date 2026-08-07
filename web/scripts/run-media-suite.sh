#!/bin/sh
set -eu

mkdir -p "${KLISI_MEDIA_ARTIFACTS:-/artifacts}"

ready=0
attempt=0
while [ "${attempt}" -lt 60 ]; do
  if curl --fail --silent "${KLISI_MEDIA_BASE_URL:-http://klisi:8080}/healthz" >/dev/null; then
    ready=1
    break
  fi
  attempt=$((attempt + 1))
  sleep 1
done
if [ "${ready}" -ne 1 ]; then
  echo "Klisi did not become healthy within 60 seconds."
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
full_suite() {
  mkdir -p "${artifacts}/deterministic" "${artifacts}/fuzz" "${artifacts}/network-chaos"
  KLISI_MEDIA_ARTIFACTS="${artifacts}/deterministic" npm run media:e2e

  if [ -z "${FC_SEED:-}" ]; then
    FC_SEED="$(date -u +%Y%m%d)"
    export FC_SEED
  fi
  KLISI_MEDIA_ARTIFACTS="${artifacts}/fuzz" npm run media:fuzz

  enable_netem
  KLISI_MEDIA_ARTIFACTS="${artifacts}/network-chaos" npm run media:chaos
  cleanup_netem
}

artifacts="${KLISI_MEDIA_ARTIFACTS:-/artifacts}"

case "${KLISI_MEDIA_SUITE:-deterministic}" in
  deterministic | nightly)
    full_suite
    ;;
  quick)
    # Local shortcut only; never what CI runs.
    npm run media:e2e
    ;;
  fuzz)
    if [ "${KLISI_NETWORK_CHAOS:-false}" = "true" ]; then
      enable_netem
    fi
    npm run media:fuzz
    ;;
  *)
    echo "Unknown KLISI_MEDIA_SUITE=${KLISI_MEDIA_SUITE}"
    exit 2
    ;;
esac
