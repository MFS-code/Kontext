#!/usr/bin/env bash
# Load prebuilt operator, runtime, reporter, and test images into kind and
# install the controller. Build images first with `make kind-install`.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CLUSTER_NAME="${KIND_CLUSTER_NAME:-kontext}"
OPERATOR_IMAGE="${KONTEXT_OPERATOR_IMAGE:-kontext-operator:dev}"
ECHO_IMAGE="${KONTEXT_ECHO_IMAGE:-kontext-echo:dev}"
REPORTER_IMAGE="${KONTEXT_REPORTER_IMAGE:-kontext-reporter:dev}"
REFERENCE_IMAGE="${KONTEXT_REFERENCE_IMAGE:-kontext-reference:dev}"
STDOUT_FIXTURE_IMAGE="${KONTEXT_STDOUT_FIXTURE_IMAGE:-kontext-stdout-fixture:dev}"

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
if ! docker image inspect "${REPORTER_IMAGE}" >/dev/null 2>&1; then
  echo "missing local image ${REPORTER_IMAGE}; build with: make docker-build-reporter" >&2
  exit 1
fi
if ! docker image inspect "${REFERENCE_IMAGE}" >/dev/null 2>&1; then
  echo "missing local image ${REFERENCE_IMAGE}; build with: make docker-build-reference" >&2
  exit 1
fi
if ! docker image inspect "${STDOUT_FIXTURE_IMAGE}" >/dev/null 2>&1; then
  echo "missing local image ${STDOUT_FIXTURE_IMAGE}; build with: make docker-build-stdout-fixture" >&2
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
kind load docker-image "${REPORTER_IMAGE}" --name "${CLUSTER_NAME}"
kind load docker-image "${REFERENCE_IMAGE}" --name "${CLUSTER_NAME}"
kind load docker-image "${STDOUT_FIXTURE_IMAGE}" --name "${CLUSTER_NAME}"

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
kubectl set env deployment/controller-manager -n kontext-system \
  "KONTEXT_REPORTER_IMAGE=${REPORTER_IMAGE}"

echo "==> waiting for controller rollout"
kubectl rollout status deployment/controller-manager -n kontext-system --timeout=120s

echo "==> ready"
kubectl get crd agents.kontext.dev agentruns.kontext.dev
kubectl get deploy -n kontext-system controller-manager
