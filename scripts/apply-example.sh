#!/usr/bin/env bash
# Apply one release-neutral example with either local dev or versioned images.
set -euo pipefail

# shellcheck source=scripts/lib/common.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib/common.sh"
ROOT_DIR="$(repo_root)"
EXAMPLES_DIR="${ROOT_DIR}/deploy/examples/v1alpha1"
EXAMPLE_ARG="${1:-}"
if [[ -n "${EXAMPLE_ARG}" && "${EXAMPLE_ARG}" != */* ]]; then
  EXAMPLE_FILE="${EXAMPLES_DIR}/${EXAMPLE_ARG}"
else
  EXAMPLE_FILE="${EXAMPLE_ARG}"
fi

need kubectl

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
  if ! validate_release_tag "${KONTEXT_RELEASE_TAG}"; then
    echo "invalid KONTEXT_RELEASE_TAG: ${KONTEXT_RELEASE_TAG}" >&2
    exit 2
  fi

  append_image \
    "$(kontext_image kontext-echo)" \
    "$(kontext_image kontext-echo):${KONTEXT_RELEASE_TAG}"
  append_image \
    "$(kontext_image kontext-reference)" \
    "$(kontext_image kontext-reference):${KONTEXT_RELEASE_TAG}"
else
  append_image \
    "$(kontext_image kontext-echo)" \
    "${KONTEXT_ECHO_IMAGE:-kontext-echo:dev}"
  append_image \
    "$(kontext_image kontext-reference)" \
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
