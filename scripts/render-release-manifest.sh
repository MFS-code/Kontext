#!/usr/bin/env bash
# Render a single install manifest from the digest metadata produced by release CI.
set -euo pipefail

# shellcheck source=scripts/lib/common.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib/common.sh"
ROOT_DIR="$(repo_root)"
METADATA_FILE="${1:-}"

need jq kubectl

if [[ -z "${METADATA_FILE}" || ! -f "${METADATA_FILE}" ]]; then
  echo "usage: $0 <image-digests.json>" >&2
  exit 2
fi

metadata_api="$(jq -er '.apiVersion' "${METADATA_FILE}")"
if [[ "${metadata_api}" != "kontext.dev/release-images/v1alpha1" ]]; then
  echo "unsupported release metadata API in ${METADATA_FILE}: ${metadata_api}" >&2
  exit 1
fi

release_tag="$(jq -er '.releaseTag | select(type == "string" and length > 0)' "${METADATA_FILE}")"
if ! validate_release_tag "${release_tag}"; then
  echo "invalid release tag in ${METADATA_FILE}: ${release_tag}" >&2
  exit 1
fi

control_plane_api="$(jq -er '.controlPlaneAPI' "${METADATA_FILE}")"
if [[ "${control_plane_api}" != "kontext.dev/v1alpha1" ]]; then
  echo "unsupported control-plane API in ${METADATA_FILE}: ${control_plane_api}" >&2
  exit 1
fi

image_ref() {
  local name="$1"
  jq -er \
    --arg name "${name}" \
    '[
      .images[]
      | select(.name == $name)
      | .immutableReference
    ] | if length == 1 then .[0] else error("expected exactly one image named " + $name) end' \
    "${METADATA_FILE}"
}

validate_image_ref() {
  local name="$1"
  local package="$2"
  local reference="$3"
  local expected_prefix
  local digest

  expected_prefix="$(kontext_image "${package}")@sha256:"
  digest="${reference#"${expected_prefix}"}"
  if [[ "${reference}" != "${expected_prefix}"* || ! "${digest}" =~ ^[0-9a-f]{64}$ ]]; then
    echo "invalid ${name} image reference: ${reference}" >&2
    exit 1
  fi
}

operator_image="$(image_ref operator)"
reporter_image="$(image_ref reporter)"
validate_image_ref operator kontext-operator "${operator_image}"
validate_image_ref reporter kontext-reporter "${reporter_image}"

overlay_dir="$(mktemp -d "${ROOT_DIR}/config/overlays/.release.XXXXXX")"
cleanup() {
  rm -rf "${overlay_dir}"
}
trap cleanup EXIT

cat >"${overlay_dir}/kustomization.yaml" <<'EOF'
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization

resources:
  - ../release

patches:
  - path: manager_patch.yaml
EOF

cat >"${overlay_dir}/manager_patch.yaml" <<EOF
apiVersion: apps/v1
kind: Deployment
metadata:
  name: controller-manager
  namespace: kontext-system
  labels:
    app.kubernetes.io/version: "${release_tag}"
spec:
  template:
    metadata:
      labels:
        app.kubernetes.io/version: "${release_tag}"
      annotations:
        kontext.dev/release: "${release_tag}"
    spec:
      containers:
        - name: manager
          image: "${operator_image}"
          env:
            - name: KONTEXT_REPORTER_IMAGE
              value: "${reporter_image}"
EOF

kubectl kustomize "${overlay_dir}"
