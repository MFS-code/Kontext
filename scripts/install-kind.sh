#!/usr/bin/env bash
set -euo pipefail

CLUSTER_NAME="${KIND_CLUSTER_NAME:-kontext}"
IMAGE_NAME="kontext:dev"
SKIP_CLUSTER_CREATE="${KONTEXT_SKIP_CLUSTER_CREATE:-false}"

need() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "missing required command: $1" >&2
    exit 1
  fi
}

need docker
need kind
need kubectl

echo "==> building ${IMAGE_NAME}"
docker build -t "${IMAGE_NAME}" .

if ! kind get clusters | grep -qx "${CLUSTER_NAME}"; then
  if [[ "${SKIP_CLUSTER_CREATE}" == "true" ]]; then
    echo "kind cluster ${CLUSTER_NAME} does not exist and KONTEXT_SKIP_CLUSTER_CREATE=true" >&2
    exit 1
  fi
  echo "==> creating kind cluster ${CLUSTER_NAME}"
  kind create cluster --name "${CLUSTER_NAME}"
else
  echo "==> using existing kind cluster ${CLUSTER_NAME}"
fi

echo "==> loading ${IMAGE_NAME} into kind/${CLUSTER_NAME}"
kind load docker-image "${IMAGE_NAME}" --name "${CLUSTER_NAME}"

echo "==> installing Agent CRD and controller"
kubectl apply -f deploy/crds/agents.kontext.dev.yaml
kubectl apply -f deploy/install.yaml

echo "==> waiting for controller rollout"
kubectl rollout status deployment/kontext-controller --timeout=90s

if [[ -n "${ANTHROPIC_API_KEY:-}" ]]; then
  echo "==> creating/updating Secret/kontext-anthropic from ANTHROPIC_API_KEY"
  kubectl create secret generic kontext-anthropic \
    --from-literal=ANTHROPIC_API_KEY="${ANTHROPIC_API_KEY}" \
    --dry-run=client -o yaml | kubectl apply -f -
else
  echo "==> ANTHROPIC_API_KEY is not set; real Anthropic examples need Secret/kontext-anthropic"
  echo "    replay demo still works: kubectl apply -f deploy/examples/replay-agent.yaml"
fi

echo "==> ready"
kubectl get crd agents.kontext.dev
kubectl get deploy kontext-controller
