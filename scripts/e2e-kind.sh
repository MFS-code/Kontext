#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CLUSTER_NAME="${KIND_CLUSTER_NAME:-kontext}"

need() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "missing required command: $1" >&2
    exit 1
  fi
}

need kubectl

echo "==> applying standalone echo task run"
kubectl apply -f "${ROOT_DIR}/deploy/examples/v1alpha1/echo-task-run.yaml"

echo "==> waiting for AgentRun to succeed"
for _ in $(seq 1 60); do
  phase="$(kubectl get agentrun echo-review -o jsonpath='{.status.phase}' 2>/dev/null || true)"
  if [[ "${phase}" == "Succeeded" ]]; then
    break
  fi
  sleep 2
done

phase="$(kubectl get agentrun echo-review -o jsonpath='{.status.phase}')"
result="$(kubectl get agentrun echo-review -o jsonpath='{.status.result}')"
if [[ "${phase}" != "Succeeded" ]]; then
  echo "expected echo-review to succeed, got phase=${phase}" >&2
  kubectl describe agentrun echo-review || true
  kubectl logs "$(kubectl get pod -l kontext.dev/run=echo-review -o jsonpath='{.items[0].metadata.name}')" || true
  exit 1
fi
echo "task result: ${result}"

echo "==> applying service echo owner"
kubectl apply -f "${ROOT_DIR}/deploy/examples/v1alpha1/echo-service-agent.yaml"

echo "==> waiting for live service run"
for _ in $(seq 1 60); do
  current_run="$(kubectl get agent echo-owner -o jsonpath='{.status.currentRunName}' 2>/dev/null || true)"
  pod_phase="$(kubectl get pod -l kontext.dev/agent=echo-owner -o jsonpath='{.items[0].status.phase}' 2>/dev/null || true)"
  if [[ -n "${current_run}" && "${pod_phase}" == "Running" ]]; then
    break
  fi
  sleep 2
done

owner_pod="$(kubectl get pod -l kontext.dev/agent=echo-owner -o jsonpath='{.items[0].metadata.name}')"
if [[ -z "${owner_pod}" ]]; then
  echo "service owner pod not found" >&2
  kubectl describe agent echo-owner || true
  exit 1
fi

echo "==> deleting owner pod to verify recast"
before_run="$(kubectl get agent echo-owner -o jsonpath='{.status.currentRunName}')"
kubectl delete pod "${owner_pod}" --wait=true

for _ in $(seq 1 60); do
  after_run="$(kubectl get agent echo-owner -o jsonpath='{.status.currentRunName}')"
  pod_phase="$(kubectl get pod -l kontext.dev/agent=echo-owner -o jsonpath='{.items[0].status.phase}' 2>/dev/null || true)"
  if [[ "${after_run}" != "${before_run}" && "${pod_phase}" == "Running" ]]; then
    echo "recast verified: ${before_run} -> ${after_run}"
    exit 0
  fi
  sleep 2
done

echo "service recast did not complete in time" >&2
kubectl describe agent echo-owner || true
exit 1
