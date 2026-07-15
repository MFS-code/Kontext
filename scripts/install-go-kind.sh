#!/usr/bin/env bash
# Load prebuilt operator + echo images into kind and install the controller.
# Build images first, e.g. `make docker-build docker-build-echo` or `make kind-install`.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CLUSTER_NAME="${KIND_CLUSTER_NAME:-kontext}"
OPERATOR_IMAGE="${KONTEXT_OPERATOR_IMAGE:-kontext-operator:dev}"
ECHO_IMAGE="${KONTEXT_ECHO_IMAGE:-kontext-echo:dev}"

need() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "missing required command: $1" >&2
    exit 1
  fi
}

need docker
need kind
need kubectl

if ! docker image inspect "${OPERATOR_IMAGE}" >/dev/null 2>&1; then
  echo "missing local image ${OPERATOR_IMAGE}; build with: make docker-build" >&2
  exit 1
fi
if ! docker image inspect "${ECHO_IMAGE}" >/dev/null 2>&1; then
  echo "missing local image ${ECHO_IMAGE}; build with: make docker-build-echo" >&2
  exit 1
fi

if ! kind get clusters | grep -qx "${CLUSTER_NAME}"; then
  echo "==> creating kind cluster ${CLUSTER_NAME}"
  kind create cluster --name "${CLUSTER_NAME}"
else
  echo "==> using existing kind cluster ${CLUSTER_NAME}"
fi

echo "==> loading images into kind/${CLUSTER_NAME}"
kind load docker-image "${OPERATOR_IMAGE}" --name "${CLUSTER_NAME}"
kind load docker-image "${ECHO_IMAGE}" --name "${CLUSTER_NAME}"

echo "==> installing v1alpha1 CRDs and Go controller"
if kubectl get crd agents.kontext.dev >/dev/null 2>&1; then
  STORED_VERSION="$(kubectl get crd agents.kontext.dev -o jsonpath='{.spec.versions[?(@.storage==true)].name}' 2>/dev/null || true)"
  if [[ "${STORED_VERSION}" == "v1" ]]; then
    echo "==> removing v1 Agent resources before v1alpha1 CRD install"
    kubectl delete agents --all --all-namespaces --wait=false --ignore-not-found=true || true
    kubectl delete crd agents.kontext.dev --wait=true --ignore-not-found=true || true
  fi
fi
kubectl delete deployment kontext-controller -n default --ignore-not-found=true
kubectl apply -k "${ROOT_DIR}/config/default"

echo "==> waiting for controller rollout"
kubectl rollout status deployment/controller-manager -n kontext-system --timeout=120s

echo "==> ready"
kubectl get crd agents.kontext.dev agentruns.kontext.dev
kubectl get deploy -n kontext-system controller-manager
