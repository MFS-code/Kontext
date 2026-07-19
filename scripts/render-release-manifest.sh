#!/usr/bin/env bash
# Render a single install manifest from the digest metadata produced by release CI.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
METADATA_FILE="${1:-}"

need() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "missing required command: $1" >&2
    exit 1
  fi
}

need jq
need kubectl

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
core='v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)'
prerelease_id='(0|[1-9][0-9]*|[0-9A-Za-z-]*[A-Za-z-][0-9A-Za-z-]*)'
release_pattern="^${core}(-${prerelease_id}(\.${prerelease_id})*)?$"
if [[ ! "${release_tag}" =~ ${release_pattern} || "${#release_tag}" -gt 63 ]]; then
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

operator_image="$(image_ref operator)"
reporter_image="$(image_ref reporter)"
if [[ ! "${operator_image}" =~ ^ghcr\.io/mfs-code/kontext-operator@sha256:[0-9a-f]{64}$ ]]; then
  echo "invalid operator image reference: ${operator_image}" >&2
  exit 1
fi
if [[ ! "${reporter_image}" =~ ^ghcr\.io/mfs-code/kontext-reporter@sha256:[0-9a-f]{64}$ ]]; then
  echo "invalid reporter image reference: ${reporter_image}" >&2
  exit 1
fi

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
