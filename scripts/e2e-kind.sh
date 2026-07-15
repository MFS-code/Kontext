#!/usr/bin/env bash
# Keyless kind scenarios. On failure, CI collects diagnostics separately via
# scripts/collect-kind-diagnostics.sh; locally run that script after a failed e2e.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

need() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "missing required command: $1" >&2
    exit 1
  fi
}

is_terminal_failure_phase() {
  case "$1" in
    Failed|BudgetExceeded) return 0 ;;
    *) return 1 ;;
  esac
}

need kubectl

echo "==> applying standalone echo task run"
kubectl apply -f "${ROOT_DIR}/deploy/examples/v1alpha1/echo-task-run.yaml"

echo "==> waiting for AgentRun to succeed"
phase=""
for _ in $(seq 1 60); do
  phase="$(kubectl get agentrun echo-review -o jsonpath='{.status.phase}' 2>/dev/null || true)"
  if [[ "${phase}" == "Succeeded" ]]; then
    break
  fi
  if is_terminal_failure_phase "${phase}"; then
    echo "expected echo-review to succeed, got phase=${phase}" >&2
    exit 1
  fi
  sleep 2
done

phase="$(kubectl get agentrun echo-review -o jsonpath='{.status.phase}')"
result="$(kubectl get agentrun echo-review -o jsonpath='{.status.result}')"
if [[ "${phase}" != "Succeeded" ]]; then
  echo "expected echo-review to succeed, got phase=${phase}" >&2
  exit 1
fi
echo "task result: ${result}"

echo "==> applying service echo agent"
kubectl apply -f "${ROOT_DIR}/deploy/examples/v1alpha1/echo-service-agent.yaml"

echo "==> waiting for live service run"
for _ in $(seq 1 60); do
  current_run="$(kubectl get agent echo-service -o jsonpath='{.status.currentRunName}' 2>/dev/null || true)"
  pod_phase="$(kubectl get pod -l kontext.dev/agent=echo-service -o jsonpath='{.items[0].status.phase}' 2>/dev/null || true)"
  if [[ -n "${current_run}" && "${pod_phase}" == "Running" ]]; then
    break
  fi
  sleep 2
done

service_pod="$(kubectl get pod -l kontext.dev/agent=echo-service -o jsonpath='{.items[0].metadata.name}')"
if [[ -z "${service_pod}" ]]; then
  echo "service agent pod not found" >&2
  exit 1
fi

echo "==> deleting service pod to verify recast"
before_run="$(kubectl get agent echo-service -o jsonpath='{.status.currentRunName}')"
kubectl delete pod "${service_pod}" --wait=true

for _ in $(seq 1 60); do
  after_run="$(kubectl get agent echo-service -o jsonpath='{.status.currentRunName}')"
  pod_phase="$(kubectl get pod -l kontext.dev/agent=echo-service -o jsonpath='{.items[0].status.phase}' 2>/dev/null || true)"
  if [[ "${after_run}" != "${before_run}" && "${pod_phase}" == "Running" ]]; then
    echo "recast verified: ${before_run} -> ${after_run}"
    exit 0
  fi
  sleep 2
done

echo "service recast did not complete in time" >&2
exit 1
