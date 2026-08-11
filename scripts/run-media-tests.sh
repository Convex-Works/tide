#!/bin/sh
set -eu

repository_root="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
compose_file="${repository_root}/deploy/media-test/compose.yaml"
artifact_dir="${KLISI_MEDIA_ARTIFACT_DIR:-${repository_root}/artifacts/media}"
mkdir -p "${artifact_dir}"
artifact_dir="$(CDPATH= cd -- "${artifact_dir}" && pwd)"
export KLISI_MEDIA_ARTIFACT_DIR="${artifact_dir}"

run_identity="${CI_RUN_ID:-local}-${CI_JOB_ID:-media}-$$"
project_name="$(printf '%s' "klisi-media-${run_identity}" | tr '[:upper:]_' '[:lower:]-' | tr -cd 'a-z0-9-')"
project_name="$(printf '%.55s' "${project_name}")"

# The stack pins container addresses so LiveKit can advertise a reachable
# node-ip, which means it needs a /24 to itself. A fixed one collides whenever
# two gate runs overlap on a runner — and every pull request produces two, one
# for the push event and one for the pull_request event.
#
# Claim one by *creating* a network with it rather than by reading the list of
# subnets already in use: Docker refuses an overlapping pool, so the create
# either succeeds or tells us to try the next candidate. Reading the list first
# is check-then-act, and two runs that read before either wrote both proceed.
#
# Seeded from the run and job identity, not $$: this runner hands the script
# the same pid on every run, so concurrent runs would otherwise start from the
# same candidate and walk the range in lockstep.
claim_network_prefix() {
  seed="$(printf '%s' "${run_identity}" | cksum | cut -d' ' -f1)"
  candidate=$(( (seed % 240) + 10 ))
  attempt=0
  while [ "${attempt}" -lt 240 ]; do
    if docker network create --driver bridge \
      --subnet "10.253.${candidate}.0/24" "${project_name}-claim" >/dev/null 2>&1; then
      docker network rm "${project_name}-claim" >/dev/null 2>&1 || true
      printf '10.253.%s' "${candidate}"
      return 0
    fi
    candidate=$(( ((candidate + 1) % 240) + 10 ))
    attempt=$(( attempt + 1 ))
  done
  echo "No free /24 remains in 10.253.0.0/16 for the media stack." >&2
  return 1
}

compose() {
  docker compose --project-name "${project_name}" --file "${compose_file}" "$@"
}

cleanup() {
  runner_id="$(compose ps --all --quiet runner 2>/dev/null || true)"
  if [ -n "${runner_id}" ]; then
    docker cp "${runner_id}:/artifacts/." "${artifact_dir}/" 2>/dev/null || true
  fi
  compose logs --no-color livekit klisi >"${artifact_dir}/stack.log" 2>&1 || true
  # --rmi local: the per-run project name means every run builds a uniquely
  # named image set, so images `down` leaves behind are garbage no later run
  # can reuse. Run 339 found the end of that road: the runner disk filled,
  # egress's Chrome hit "No space left on device", and every recording 502'd.
  compose down --volumes --remove-orphans --rmi local >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM

# Leftovers from runs that died before their own cleanup (or ran before
# cleanup removed images at all). The klisi-media- prefix is this script's
# own namespace, and an image belonging to a live concurrent run is in use by
# its containers, so its removal fails and is skipped. The layer cache
# survives image removal, so rebuilds stay warm; the cache itself is bounded
# separately below.
docker image ls --filter 'reference=klisi-media-*' --format '{{.Repository}}:{{.Tag}}' |
  grep -v "^${project_name}-" |
  while read -r stale_image; do
    docker image rm "${stale_image}" >/dev/null 2>&1 || true
  done
docker builder prune --force --keep-storage 20GB >/dev/null 2>&1 || true

# Build first so the claim is made immediately before the network is created.
# Claiming across the build would leave a window of minutes in which another
# run takes the same /24.
compose build

KLISI_MEDIA_NET_PREFIX="$(claim_network_prefix)"
export KLISI_MEDIA_NET_PREFIX
echo "Media stack network: ${KLISI_MEDIA_NET_PREFIX}.0/24 (project ${project_name})"

compose up --abort-on-container-exit --exit-code-from runner runner
