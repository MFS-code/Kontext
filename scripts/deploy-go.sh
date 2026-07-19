#!/usr/bin/env bash
# Apply the development overlay with caller-selected operator and reporter images.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OPERATOR_IMAGE="${KONTEXT_OPERATOR_IMAGE:-kontext-operator:dev}"
REPORTER_IMAGE="${KONTEXT_REPORTER_IMAGE:-kontext-reporter:dev}"

need() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "missing required command: $1" >&2
    exit 1
  fi
}

need kubectl

DEPLOY_OVERLAY_DIR="$(mktemp -d "${TMPDIR:-/tmp}/kontext-deploy-overlay.XXXXXX")"
cleanup() {
  rm -rf "${DEPLOY_OVERLAY_DIR}"
}
trap cleanup EXIT

cp -R "${ROOT_DIR}/config" "${DEPLOY_OVERLAY_DIR}/config"

cat >"${DEPLOY_OVERLAY_DIR}/kustomization.yaml" <<'EOF'
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization

resources:
  - config/overlays/dev

patches:
  - path: manager_patch.yaml
EOF

cat >"${DEPLOY_OVERLAY_DIR}/manager_patch.yaml" <<EOF
apiVersion: apps/v1
kind: Deployment
metadata:
  name: controller-manager
  namespace: kontext-system
spec:
  template:
    spec:
      containers:
        - name: manager
          image: "${OPERATOR_IMAGE}"
          env:
            - name: KONTEXT_REPORTER_IMAGE
              value: "${REPORTER_IMAGE}"
EOF

kubectl kustomize "${DEPLOY_OVERLAY_DIR}" |
  kubectl apply -f -
