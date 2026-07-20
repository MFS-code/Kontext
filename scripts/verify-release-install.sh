#!/usr/bin/env bash
# Verify install, upgrade, retained-CRD removal, and complete removal on a clean cluster.
set -euo pipefail

# shellcheck source=scripts/lib/common.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib/common.sh"
ROOT_DIR="$(repo_root)"
CURRENT_MANIFEST="${1:-}"
CURRENT_VERSION="${2:-}"
PREVIOUS_MANIFEST="${3:-}"
PREVIOUS_VERSION="${4:-}"
APPLY_EXAMPLE="${ROOT_DIR}/scripts/apply-example.sh"

if [[ -z "${CURRENT_MANIFEST}" || ! -f "${CURRENT_MANIFEST}" || -z "${CURRENT_VERSION}" ]]; then
  echo "usage: $0 <current-install.yaml> <current-version> [previous-install.yaml previous-version]" >&2
  exit 2
fi
if [[ -n "${PREVIOUS_MANIFEST}" && (! -f "${PREVIOUS_MANIFEST}" || -z "${PREVIOUS_VERSION}") ]]; then
  echo "previous manifest and version must be supplied together" >&2
  exit 2
fi

install_release() {
  local manifest="$1"
  kubectl apply -f "${manifest}"
  kubectl rollout status deployment/controller-manager \
    --namespace kontext-system \
    --timeout=180s

  local operator_image=""
  local reporter_image=""
  operator_image="$(
    kubectl get deployment controller-manager -n kontext-system \
      -o jsonpath='{.spec.template.spec.containers[?(@.name=="manager")].image}'
  )"
  reporter_image="$(
    kubectl get deployment controller-manager -n kontext-system \
      -o jsonpath='{.spec.template.spec.containers[?(@.name=="manager")].env[?(@.name=="KONTEXT_REPORTER_IMAGE")].value}'
  )"
  if [[ "${operator_image}" != *@sha256:* || "${reporter_image}" != *@sha256:* ]]; then
    echo "install manifest did not deploy digest-pinned images" >&2
    return 1
  fi
}

smoke_release_runtime() {
  local version="$1"
  kubectl delete agentrun echo-review --ignore-not-found=true --wait=true
  KONTEXT_RELEASE_TAG="${version}" "${APPLY_EXAMPLE}" echo-task-run.yaml
  wait_for_run_phase echo-review Succeeded default 180 1

  local runtime_image=""
  runtime_image="$(
    kubectl get agentrun echo-review -o jsonpath='{.spec.runtime.image}'
  )"
  if [[ "${runtime_image}" != "$(kontext_image kontext-echo):${version}" ]]; then
    echo "unexpected echo runtime image: ${runtime_image}" >&2
    return 1
  fi
  kubectl delete agentrun echo-review --wait=true
}

if [[ -n "${PREVIOUS_MANIFEST}" ]]; then
  echo "==> installing previous release ${PREVIOUS_VERSION}"
  install_release "${PREVIOUS_MANIFEST}"
  smoke_release_runtime "${PREVIOUS_VERSION}"
fi

echo "==> installing current release ${CURRENT_VERSION}"
install_release "${CURRENT_MANIFEST}"
smoke_release_runtime "${CURRENT_VERSION}"

echo "==> running registry-backed keyless acceptance"
KONTEXT_RELEASE_TAG="${CURRENT_VERSION}" "${ROOT_DIR}/scripts/e2e-kind.sh"

echo "==> verifying control-plane removal with CR retention"
cat <<EOF | kubectl apply -f -
apiVersion: kontext.dev/v1alpha1
kind: Agent
metadata:
  name: retained-install-check
  namespace: default
spec:
  mode: Task
  goal: Verify custom-resource retention during control-plane removal.
  model: echo-model
  runtime:
    image: $(kontext_image kontext-echo):${CURRENT_VERSION}
EOF

kubectl delete clusterrolebinding manager-rolebinding --ignore-not-found=true
kubectl delete clusterrole manager-role --ignore-not-found=true
kubectl delete namespace kontext-system --ignore-not-found=true --wait=true

kubectl get crd agents.kontext.dev agentruns.kontext.dev >/dev/null
kubectl get agent retained-install-check -n default >/dev/null

echo "==> reinstalling after retained-CRD removal"
install_release "${CURRENT_MANIFEST}"
kubectl get agent retained-install-check -n default >/dev/null
kubectl delete agent retained-install-check -n default --wait=true

echo "==> verifying complete uninstall"
kubectl delete -f "${CURRENT_MANIFEST}" --ignore-not-found=true --wait=true

if kubectl get crd agents.kontext.dev >/dev/null 2>&1 ||
  kubectl get crd agentruns.kontext.dev >/dev/null 2>&1 ||
  kubectl get namespace kontext-system >/dev/null 2>&1 ||
  kubectl get clusterrole manager-role >/dev/null 2>&1 ||
  kubectl get clusterrolebinding manager-rolebinding >/dev/null 2>&1; then
  echo "complete uninstall left Kontext resources behind" >&2
  exit 1
fi

echo "release install, upgrade, retention, and uninstall verification passed"
