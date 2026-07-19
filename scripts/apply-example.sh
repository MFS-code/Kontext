#!/usr/bin/env bash
# Apply one release-neutral example with either local dev or versioned images.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
EXAMPLES_DIR="${ROOT_DIR}/deploy/examples/v1alpha1"
EXAMPLE_ARG="${1:-}"
if [[ -n "${EXAMPLE_ARG}" && "${EXAMPLE_ARG}" != */* ]]; then
  EXAMPLE_FILE="${EXAMPLES_DIR}/${EXAMPLE_ARG}"
else
  EXAMPLE_FILE="${EXAMPLE_ARG}"
fi

if ! command -v kubectl >/dev/null 2>&1; then
  echo "missing required command: kubectl" >&2
  exit 1
fi

if [[ -z "${EXAMPLE_FILE}" || ! -f "${EXAMPLE_FILE}" ]]; then
  echo "usage: $0 <example.yaml|deploy/examples/v1alpha1/example.yaml> [kubectl apply options]" >&2
  exit 2
fi

example_dir="$(cd "$(dirname "${EXAMPLE_FILE}")" && pwd)"
if [[ "${example_dir}" != "${EXAMPLES_DIR}" ]]; then
  echo "example must be a direct child of ${EXAMPLES_DIR}" >&2
  exit 2
fi
example_name="$(basename "${EXAMPLE_FILE}")"

overlay_dir="$(mktemp -d "${EXAMPLES_DIR}/.render.XXXXXX")"
cleanup() {
  rm -rf "${overlay_dir}"
}
trap cleanup EXIT

cp "${EXAMPLE_FILE}" "${overlay_dir}/${example_name}"
cp "${EXAMPLES_DIR}/kustomizeconfig.yaml" "${overlay_dir}/kustomizeconfig.yaml"

cat >"${overlay_dir}/kustomization.yaml" <<EOF
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization

resources:
  - ${example_name}

configurations:
  - kustomizeconfig.yaml

images:
EOF

append_image() {
  local original="$1"
  local reference="$2"
  local name=""
  local tag=""
  local digest=""
  local final_component="${reference##*/}"

  if [[ "${reference}" == *@* ]]; then
    name="${reference%@*}"
    digest="${reference#*@}"
  elif [[ "${final_component}" == *:* ]]; then
    name="${reference%:*}"
    tag="${reference##*:}"
  else
    name="${reference}"
    tag="latest"
  fi

  {
    echo "  - name: ${original}"
    echo "    newName: ${name}"
    if [[ -n "${digest}" ]]; then
      echo "    digest: ${digest}"
    else
      echo "    newTag: ${tag}"
    fi
  } >>"${overlay_dir}/kustomization.yaml"
}

if [[ -n "${KONTEXT_RELEASE_TAG:-}" ]]; then
  core='v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)'
  prerelease_id='(0|[1-9][0-9]*|[0-9A-Za-z-]*[A-Za-z-][0-9A-Za-z-]*)'
  release_pattern="^${core}(-${prerelease_id}(\.${prerelease_id})*)?$"
  if [[ ! "${KONTEXT_RELEASE_TAG}" =~ ${release_pattern} ||
    "${#KONTEXT_RELEASE_TAG}" -gt 63 ]]; then
    echo "invalid KONTEXT_RELEASE_TAG: ${KONTEXT_RELEASE_TAG}" >&2
    exit 2
  fi

  append_image \
    "ghcr.io/mfs-code/kontext-echo" \
    "ghcr.io/mfs-code/kontext-echo:${KONTEXT_RELEASE_TAG}"
  append_image \
    "ghcr.io/mfs-code/kontext-reference" \
    "ghcr.io/mfs-code/kontext-reference:${KONTEXT_RELEASE_TAG}"
else
  append_image \
    "ghcr.io/mfs-code/kontext-echo" \
    "${KONTEXT_ECHO_IMAGE:-kontext-echo:dev}"
  append_image \
    "ghcr.io/mfs-code/kontext-reference" \
    "${KONTEXT_REFERENCE_IMAGE:-kontext-reference:dev}"
  append_image \
    "busybox" \
    "${KONTEXT_STDOUT_FIXTURE_IMAGE:-kontext-stdout-fixture:dev}"
fi

if [[ "${KONTEXT_RENDER_ONLY:-false}" == "true" ]]; then
  kubectl kustomize "${overlay_dir}"
  exit 0
fi

kubectl apply -k "${overlay_dir}" "${@:2}"
