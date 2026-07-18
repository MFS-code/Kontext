#!/usr/bin/env bash
# Keyless kind scenarios. On failure, CI collects diagnostics separately via
# scripts/collect-kind-diagnostics.sh; locally run that script after a failed e2e.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TOOLS_NAMESPACE="kontext-e2e-tools"

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

wait_for_run_phase() {
  local name="$1"
  local expected="$2"
  local namespace="${3:-default}"
  local phase=""
  for _ in $(seq 1 60); do
    phase="$(
      kubectl get agentrun "${name}" -n "${namespace}" \
        -o jsonpath='{.status.phase}' 2>/dev/null || true
    )"
    if [[ "${phase}" == "${expected}" ]]; then
      return 0
    fi
    case "${phase}" in
      ""|Pending|Running) ;;
      *)
        echo "expected ${name} phase=${expected}, got phase=${phase}" >&2
        return 1
        ;;
    esac
    sleep 2
  done
  echo "timed out waiting for ${name} phase=${expected}; last phase=${phase}" >&2
  return 1
}

cleanup_e2e_resources() {
  local status=0
  kubectl delete agent echo-service --ignore-not-found=true --wait=true || status=1
  kubectl delete agentrun \
    echo-review \
    stdout-last-line \
    stdout-envelope \
    stdout-failure \
    stdout-signal \
    reference-fake \
    reference-fake-tool \
    --ignore-not-found=true \
    --wait=true || status=1
  kubectl delete configmap reference-tool-knowledge \
    --ignore-not-found=true || status=1
  kubectl delete namespace "${TOOLS_NAMESPACE}" \
    --ignore-not-found=true \
    --wait=true \
    --timeout=60s || status=1
  return "${status}"
}

on_exit() {
  local status=$?
  if [[ "${status}" -eq 0 ]]; then
    echo "==> cleaning completed e2e resources"
    set +e
    cleanup_e2e_resources
    set -e
  else
    echo "==> preserving failed e2e resources for diagnostics" >&2
  fi
  return "${status}"
}

need kubectl
trap on_exit EXIT

echo "==> cleaning previous e2e resources"
cleanup_e2e_resources

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

echo "==> verifying last-line capture from an unmodified image"
kubectl apply -f "${ROOT_DIR}/deploy/examples/v1alpha1/stdout-last-line-run.yaml"
wait_for_run_phase stdout-last-line Succeeded
last_line_result="$(kubectl get agentrun stdout-last-line -o jsonpath='{.status.result}')"
last_line_media_type="$(kubectl get agentrun stdout-last-line -o jsonpath='{.status.output.mediaType}')"
last_line_logs="$(kubectl logs run-stdout-last-line -c runtime)"
if [[ "${last_line_result}" != "final answer from busybox" ]]; then
  echo "unexpected last-line result: ${last_line_result}" >&2
  exit 1
fi
if [[ "${last_line_media_type}" != "text/plain" ]]; then
  echo "unexpected last-line media type: ${last_line_media_type}" >&2
  exit 1
fi
if [[ "${last_line_logs}" != *"ordinary workload log"* ]]; then
  echo "ordinary workload log was not preserved" >&2
  exit 1
fi

echo "==> verifying structured envelope capture"
kubectl apply -f "${ROOT_DIR}/deploy/examples/v1alpha1/stdout-envelope-run.yaml"
wait_for_run_phase stdout-envelope Succeeded
structured_result="$(kubectl get agentrun stdout-envelope -o jsonpath='{.status.result}')"
input_tokens="$(kubectl get agentrun stdout-envelope -o jsonpath='{.status.usage.inputTokens}')"
output_tokens="$(kubectl get agentrun stdout-envelope -o jsonpath='{.status.usage.outputTokens}')"
if [[ "${structured_result}" != '{"answer":"structured"}' ]]; then
  echo "unexpected structured result: ${structured_result}" >&2
  exit 1
fi
if [[ "${input_tokens}" != "0" || "${output_tokens}" != "7" ]]; then
  echo "unexpected structured usage: input=${input_tokens} output=${output_tokens}" >&2
  exit 1
fi

echo "==> verifying non-zero child exit propagation"
kubectl apply -f "${ROOT_DIR}/deploy/examples/v1alpha1/stdout-failure-run.yaml"
wait_for_run_phase stdout-failure Failed
failure_exit_code="$(
  kubectl get pod run-stdout-failure \
    -o jsonpath='{.status.containerStatuses[?(@.name=="runtime")].state.terminated.exitCode}'
)"
if [[ "${failure_exit_code}" != "7" ]]; then
  echo "expected child exit code 7, got ${failure_exit_code}" >&2
  exit 1
fi

echo "==> verifying SIGTERM forwarding to the child"
kubectl apply -f "${ROOT_DIR}/deploy/examples/v1alpha1/stdout-signal-run.yaml"
for _ in $(seq 1 60); do
  signal_phase="$(kubectl get pod run-stdout-signal -o jsonpath='{.status.phase}' 2>/dev/null || true)"
  if [[ "${signal_phase}" == "Running" ]]; then
    break
  fi
  sleep 2
done
if [[ "${signal_phase}" != "Running" ]]; then
  echo "stdout signal pod did not reach Running" >&2
  exit 1
fi
kubectl exec run-stdout-signal -c runtime -- kill -TERM 1 || true
wait_for_run_phase stdout-signal Succeeded
signal_result="$(kubectl get agentrun stdout-signal -o jsonpath='{.status.result}')"
if [[ "${signal_result}" != "signal reached child" ]]; then
  echo "SIGTERM did not reach child; result=${signal_result}" >&2
  exit 1
fi

echo "==> verifying model-agnostic reference runtime"
kubectl apply -f "${ROOT_DIR}/deploy/examples/v1alpha1/reference-fake-run.yaml"
wait_for_run_phase reference-fake Succeeded
reference_result="$(kubectl get agentrun reference-fake -o jsonpath='{.status.result}')"
reference_input_tokens="$(kubectl get agentrun reference-fake -o jsonpath='{.status.usage.inputTokens}')"
reference_output_tokens="$(kubectl get agentrun reference-fake -o jsonpath='{.status.usage.outputTokens}')"
reference_logs="$(kubectl logs run-reference-fake -c runtime)"
if [[ "${reference_result}" != "Fake provider completed goal: Explain the Kontext runtime contract in one sentence." ]]; then
  echo "unexpected reference result: ${reference_result}" >&2
  exit 1
fi
# The fake provider reports word counts: input = words in the goal string from
# reference-fake-run.yaml (8), output = words in "Fake provider completed
# goal: <goal>" (12). Adjust both if the example goal changes.
if [[ "${reference_input_tokens}" != "8" || "${reference_output_tokens}" != "12" ]]; then
  echo "unexpected reference usage: input=${reference_input_tokens} output=${reference_output_tokens}" >&2
  exit 1
fi
if [[ "${reference_logs}" != *'"apiVersion":"kontext.dev/event/v1alpha1"'* ]]; then
  echo "reference runtime did not emit versioned JSONL events" >&2
  exit 1
fi

echo "==> verifying bounded reference-runtime tool loop"
kubectl apply -f "${ROOT_DIR}/deploy/examples/v1alpha1/reference-fake-tool-run.yaml"
wait_for_run_phase reference-fake-tool Succeeded
tool_result="$(kubectl get agentrun reference-fake-tool -o jsonpath='{.status.result}')"
tool_logs="$(kubectl logs run-reference-fake-tool -c runtime)"
if [[ "${tool_result}" != "Fake provider received read_knowledge result: tool loop works" ]]; then
  echo "unexpected tool-loop result: ${tool_result}" >&2
  exit 1
fi
if [[ "${tool_logs}" != *'"type":"tool"'* ||
  "${tool_logs}" != *'"name":"read_knowledge"'* ]]; then
  echo "tool loop did not emit the expected execution event" >&2
  exit 1
fi

echo "==> verifying Kubernetes read policy and restricted shell execution"
kubectl apply -f "${ROOT_DIR}/deploy/examples/v1alpha1/reference-kind-policy-runs.yaml"

wait_for_run_phase reference-kubernetes-pods Succeeded "${TOOLS_NAMESPACE}"
pods_logs="$(
  kubectl logs run-reference-kubernetes-pods -n "${TOOLS_NAMESPACE}" -c runtime
)"
pods_service_account="$(
  kubectl get pod run-reference-kubernetes-pods -n "${TOOLS_NAMESPACE}" \
    -o jsonpath='{.spec.serviceAccountName}'
)"
if [[ "${pods_logs}" != *'"name":"kubernetes_read"'* ||
  "${pods_logs}" != *'"errorCode":"","isError":false'* ||
  "${pods_service_account}" != "reference-kubernetes-reader" ]]; then
  echo "current-namespace Pod list did not succeed through kubernetes_read" >&2
  exit 1
fi

wait_for_run_phase reference-kubernetes-secrets Succeeded "${TOOLS_NAMESPACE}"
secrets_logs="$(
  kubectl logs run-reference-kubernetes-secrets -n "${TOOLS_NAMESPACE}" -c runtime
)"
if [[ "${secrets_logs}" != *'"name":"kubernetes_read"'* ||
  "${secrets_logs}" != *'"errorCode":"kubernetes_resource_denied"'* ]]; then
  echo "Secrets were not rejected by the runtime resource allowlist" >&2
  exit 1
fi

wait_for_run_phase reference-kubernetes-configmaps Succeeded "${TOOLS_NAMESPACE}"
configmaps_logs="$(
  kubectl logs run-reference-kubernetes-configmaps -n "${TOOLS_NAMESPACE}" -c runtime
)"
if [[ "${configmaps_logs}" != *'"name":"kubernetes_read"'* ||
  "${configmaps_logs}" != *'"errorCode":"kubernetes_rbac_denied"'* ]]; then
  echo "ungranted ConfigMap list did not reach Kubernetes RBAC" >&2
  exit 1
fi

wait_for_run_phase reference-restricted-shell Succeeded "${TOOLS_NAMESPACE}"
shell_result="$(
  kubectl get agentrun reference-restricted-shell -n "${TOOLS_NAMESPACE}" \
    -o jsonpath='{.status.result}'
)"
shell_pod="run-reference-restricted-shell"
shell_run_as_non_root="$(
  kubectl get pod "${shell_pod}" -n "${TOOLS_NAMESPACE}" \
    -o jsonpath='{.spec.containers[?(@.name=="runtime")].securityContext.runAsNonRoot}'
)"
shell_allow_privilege_escalation="$(
  kubectl get pod "${shell_pod}" -n "${TOOLS_NAMESPACE}" \
    -o jsonpath='{.spec.containers[?(@.name=="runtime")].securityContext.allowPrivilegeEscalation}'
)"
shell_dropped_capabilities="$(
  kubectl get pod "${shell_pod}" -n "${TOOLS_NAMESPACE}" \
    -o jsonpath='{.spec.containers[?(@.name=="runtime")].securityContext.capabilities.drop[*]}'
)"
shell_seccomp_profile="$(
  kubectl get pod "${shell_pod}" -n "${TOOLS_NAMESPACE}" \
    -o jsonpath='{.spec.containers[?(@.name=="runtime")].securityContext.seccompProfile.type}'
)"
shell_volume_names="$(
  kubectl get pod "${shell_pod}" -n "${TOOLS_NAMESPACE}" \
    -o jsonpath='{.spec.volumes[*].name}'
)"
if [[ "${shell_result}" != *"restricted-shell-ok"* ||
  "${shell_run_as_non_root}" != "true" ||
  "${shell_allow_privilege_escalation}" != "false" ||
  "${shell_dropped_capabilities}" != "ALL" ||
  "${shell_seccomp_profile}" != "RuntimeDefault" ||
  "${shell_volume_names}" == *"kube-api-access-"* ]]; then
  echo "restricted shell Pod did not preserve the required security settings" >&2
  exit 1
fi

wait_for_run_phase reference-wallclock-shell BudgetExceeded "${TOOLS_NAMESPACE}"
sleep 5
wallclock_phase="$(
  kubectl get agentrun reference-wallclock-shell -n "${TOOLS_NAMESPACE}" \
    -o jsonpath='{.status.phase}'
)"
wallclock_pod="$(
  kubectl get pod run-reference-wallclock-shell -n "${TOOLS_NAMESPACE}" \
    -o name 2>/dev/null || true
)"
if [[ "${wallclock_phase}" != "BudgetExceeded" || -n "${wallclock_pod}" ]]; then
  echo "wallclock cancellation did not remain terminally BudgetExceeded" >&2
  exit 1
fi

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
