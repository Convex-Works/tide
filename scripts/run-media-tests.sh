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

compose() {
  docker compose --project-name "${project_name}" --file "${compose_file}" "$@"
}

cleanup() {
  compose logs --no-color livekit klisi >"${artifact_dir}/stack.log" 2>&1 || true
  compose down --volumes --remove-orphans >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM

compose up --build --abort-on-container-exit --exit-code-from runner runner
