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
  compose down --volumes --remove-orphans >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM

# Build first so the claim is made immediately before the network is created.
# Claiming across the build would leave a window of minutes in which another
# run takes the same /24.
compose build

KLISI_MEDIA_NET_PREFIX="$(claim_network_prefix)"
export KLISI_MEDIA_NET_PREFIX
echo "Media stack network: ${KLISI_MEDIA_NET_PREFIX}.0/24 (project ${project_name})"

compose up --abort-on-container-exit --exit-code-from runner runner
