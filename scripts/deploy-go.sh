#!/usr/bin/env bash
# Apply the development overlay with caller-selected operator and reporter images.
set -euo pipefail

# shellcheck source=scripts/lib/common.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib/common.sh"
OPERATOR_IMAGE="${KONTEXT_OPERATOR_IMAGE:-kontext-operator:dev}"
REPORTER_IMAGE="${KONTEXT_REPORTER_IMAGE:-kontext-reporter:dev}"

need kubectl

DEPLOY_OVERLAY_DIR="$(mktemp -d "${TMPDIR:-/tmp}/kontext-deploy-overlay.XXXXXX")"
cleanup() {
  rm -rf "${DEPLOY_OVERLAY_DIR}"
}
trap cleanup EXIT

write_development_overlay \
  "${DEPLOY_OVERLAY_DIR}" \
  "${OPERATOR_IMAGE}" \
  "${REPORTER_IMAGE}"

kubectl kustomize "${DEPLOY_OVERLAY_DIR}" |
  kubectl apply -f -
