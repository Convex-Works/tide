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
# for the push event and one for the pull_request event. Claim a /24 that no
# existing network holds; Docker rejects an overlapping pool outright.
claim_network_prefix() {
  taken="$(docker network ls --quiet |
    xargs -r docker network inspect --format '{{range .IPAM.Config}}{{.Subnet}} {{end}}' 2>/dev/null ||
    true)"
  candidate=$(( ($$ % 240) + 10 ))
  attempt=0
  while [ "${attempt}" -lt 240 ]; do
    case " ${taken} " in
      *" 10.253.${candidate}.0/24 "*) ;;
      *)
        printf '10.253.%s' "${candidate}"
        return 0
        ;;
    esac
    candidate=$(( (candidate % 240) + 10 ))
    attempt=$(( attempt + 1 ))
  done
  echo "No free /24 remains in 10.253.0.0/16 for the media stack." >&2
  return 1
}

KLISI_MEDIA_NET_PREFIX="$(claim_network_prefix)"
export KLISI_MEDIA_NET_PREFIX

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

compose up --build --abort-on-container-exit --exit-code-from runner runner
