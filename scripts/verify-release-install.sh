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

control_plane_resources() {
  local prefix="$1"
  printf '%s\n' \
    "deployment/${prefix}controller-manager" \
    "mutatingwebhookconfiguration/${prefix}task-agentrun-mutator.kontext.dev" \
    "clusterrolebinding/${prefix}manager-rolebinding" \
    "clusterrolebinding/${prefix}webhook-registration-manager" \
    "clusterrole/${prefix}manager-role" \
    "clusterrole/${prefix}webhook-registration-manager" \
    "service/${prefix}webhook-service" \
    "serviceaccount/${prefix}controller-manager" \
    "rolebinding/${prefix}leader-election-manager" \
    "rolebinding/${prefix}webhook-certificate-manager" \
    "role/${prefix}leader-election-manager" \
    "role/${prefix}webhook-certificate-manager" \
    "networkpolicy/${prefix}controller-manager-webhook"
}

remove_control_plane() {
  local prefix="$1"
  local resources=()
  while IFS= read -r resource; do
    resources+=("${resource}")
  done < <(control_plane_resources "${prefix}")
  kubectl delete "${resources[@]}" -n kontext-system \
    --ignore-not-found=true --wait=true
}

control_plane_resources_exist() {
  local prefix="$1"
  local resource=""
  while IFS= read -r resource; do
    if kubectl get "${resource}" -n kontext-system >/dev/null 2>&1; then
      return 0
    fi
  done < <(control_plane_resources "${prefix}")
  return 1
}

resource_absent() {
  ! kubectl get "$@" >/dev/null 2>&1
}

manifest_identity() {
  local manifest="$1"
  if grep -Fxq "  name: kontext-controller-manager" "${manifest}"; then
    printf '%s\n' current
  elif grep -Fxq "  name: controller-manager" "${manifest}"; then
    printf '%s\n' legacy
  else
    echo "manifest does not contain a recognized controller Deployment: ${manifest}" >&2
    return 1
  fi
}

install_release() {
  local manifest="$1"
  local expect_webhook="${2:-true}"
  local identity="${3:-current}"
  local deployment=""

  case "${identity}" in
    current) deployment="kontext-controller-manager" ;;
    legacy) deployment="controller-manager" ;;
    *)
      echo "unsupported control-plane identity: ${identity}" >&2
      return 2
      ;;
  esac

  kubectl apply -f "${manifest}"
  if [[ "${identity}" == "current" ]]; then
    remove_control_plane ""
  else
    remove_control_plane "kontext-"
  fi
  kubectl rollout status "deployment/${deployment}" \
    --namespace kontext-system \
    --timeout=180s

  local operator_image=""
  local reporter_image=""
  operator_image="$(
    kubectl get deployment "${deployment}" -n kontext-system \
      -o jsonpath='{.spec.template.spec.containers[?(@.name=="manager")].image}'
  )"
  reporter_image="$(
    kubectl get deployment "${deployment}" -n kontext-system \
      -o jsonpath='{.spec.template.spec.containers[?(@.name=="manager")].env[?(@.name=="KONTEXT_REPORTER_IMAGE")].value}'
  )"
  if [[ "${operator_image}" != *@sha256:* || "${reporter_image}" != *@sha256:* ]]; then
    echo "install manifest did not deploy digest-pinned images" >&2
    return 1
  fi

  if [[ "${expect_webhook}" != "true" ]]; then
    return 0
  fi

  kubectl get service kontext-webhook-service -n kontext-system >/dev/null
  kubectl get secret webhook-server-cert -n kontext-system >/dev/null
  kubectl get mutatingwebhookconfiguration \
    kontext-task-agentrun-mutator.kontext.dev >/dev/null
  kubectl get role kontext-webhook-certificate-manager -n kontext-system >/dev/null
  kubectl get role kontext-leader-election-manager -n kontext-system >/dev/null
  kubectl get clusterrole kontext-webhook-registration-manager >/dev/null

  local secret_ca=""
  local registered_ca=""
  secret_ca="$(
    kubectl get secret webhook-server-cert -n kontext-system \
      -o jsonpath='{.data.ca\.crt}'
  )"
  registered_ca="$(
    kubectl get mutatingwebhookconfiguration \
      kontext-task-agentrun-mutator.kontext.dev \
      -o jsonpath='{.webhooks[0].clientConfig.caBundle}'
  )"
  if [[ -z "${secret_ca}" || "${secret_ca}" != "${registered_ca}" ]]; then
    echo "release webhook registration does not trust the bootstrapped Secret CA" >&2
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

CURRENT_IDENTITY="$(manifest_identity "${CURRENT_MANIFEST}")"
if [[ "${CURRENT_IDENTITY}" != "current" ]]; then
  echo "current manifest does not use the prefixed control-plane identity" >&2
  exit 1
fi
PREVIOUS_IDENTITY=""
if [[ -n "${PREVIOUS_MANIFEST}" ]]; then
  PREVIOUS_IDENTITY="$(manifest_identity "${PREVIOUS_MANIFEST}")"
fi

if [[ -n "${PREVIOUS_MANIFEST}" ]]; then
  echo "==> installing previous release ${PREVIOUS_VERSION}"
  install_release "${PREVIOUS_MANIFEST}" false "${PREVIOUS_IDENTITY}"
  smoke_release_runtime "${PREVIOUS_VERSION}"
fi

echo "==> installing current release ${CURRENT_VERSION}"
install_release "${CURRENT_MANIFEST}" true current
smoke_release_runtime "${CURRENT_VERSION}"

if [[ -n "${PREVIOUS_MANIFEST}" ]]; then
  echo "==> rolling back to previous release ${PREVIOUS_VERSION}"
  install_release "${PREVIOUS_MANIFEST}" false "${PREVIOUS_IDENTITY}"
  smoke_release_runtime "${PREVIOUS_VERSION}"

  echo "==> restoring current release ${CURRENT_VERSION}"
  install_release "${CURRENT_MANIFEST}" true current
  smoke_release_runtime "${CURRENT_VERSION}"
fi

echo "==> running registry-backed keyless acceptance"
KONTEXT_RELEASE_TAG="${CURRENT_VERSION}" "${ROOT_DIR}/scripts/e2e-kind.sh"

echo "==> running registry-backed Task acceptance"
KONTEXT_RELEASE_TAG="${CURRENT_VERSION}" \
  KONTEXT_ECHO_IMAGE="$(kontext_image kontext-echo):${CURRENT_VERSION}" \
  "${ROOT_DIR}/scripts/e2e-kind-task.sh"

echo "==> running registry-backed Scheduled acceptance"
KONTEXT_ECHO_IMAGE="$(kontext_image kontext-echo):${CURRENT_VERSION}" \
  "${ROOT_DIR}/scripts/e2e-kind-scheduled.sh"

echo "==> running registry-backed webhook TLS and HA acceptance"
KONTEXT_ECHO_IMAGE="$(kontext_image kontext-echo):${CURRENT_VERSION}" \
  "${ROOT_DIR}/scripts/e2e-kind-webhook.sh"

echo "==> verifying Task result and control-plane removal with CR retention"
cat <<EOF | kubectl apply -f -
apiVersion: kontext.dev/v1alpha1
kind: Agent
metadata:
  name: retained-install-check
  namespace: default
spec:
  mode: Task
  goal: Verify custom-resource retention during control-plane removal.
  provider: echo
  model: echo-model
  runtime:
    image: $(kontext_image kontext-echo):${CURRENT_VERSION}
---
apiVersion: kontext.dev/v1alpha1
kind: AgentRun
metadata:
  name: retained-install-invocation
  namespace: default
spec:
  agentRef:
    name: retained-install-check
EOF
wait_for_run_phase retained-install-invocation Succeeded default 180 1
retained_result="$(
  kubectl get agentrun retained-install-invocation -n default \
    -o jsonpath='{.status.result}'
)"
if [[ "${retained_result}" != *"Verify custom-resource retention during control-plane removal."* ]]; then
  echo "release Task invocation did not produce the expected terminal result" >&2
  exit 1
fi

remove_control_plane "kontext-"
remove_control_plane ""
kubectl delete namespace kontext-system --ignore-not-found=true --wait=true

kubectl get crd agents.kontext.dev agentruns.kontext.dev >/dev/null
kubectl get agent retained-install-check -n default >/dev/null
kubectl get agentrun retained-install-invocation -n default >/dev/null

echo "==> reinstalling after retained-CRD removal"
install_release "${CURRENT_MANIFEST}" true current
kubectl get agent retained-install-check -n default >/dev/null
kubectl get agentrun retained-install-invocation -n default >/dev/null
kubectl delete agent retained-install-check -n default --wait=true
if ! wait_until 120 1 "retained Task invocation deletion" \
  resource_absent agentrun retained-install-invocation -n default; then
  echo "retained Task Agent deletion did not cascade to its invocation" >&2
  exit 1
fi

echo "==> verifying complete uninstall"
kubectl delete -f "${CURRENT_MANIFEST}" --ignore-not-found=true --wait=true

if kubectl get crd agents.kontext.dev >/dev/null 2>&1 ||
  kubectl get crd agentruns.kontext.dev >/dev/null 2>&1 ||
  kubectl get namespace kontext-system >/dev/null 2>&1 ||
  control_plane_resources_exist "kontext-" ||
  control_plane_resources_exist ""; then
  echo "complete uninstall left Kontext resources behind" >&2
  exit 1
fi

echo "release install, upgrade, retention, and uninstall verification passed"
