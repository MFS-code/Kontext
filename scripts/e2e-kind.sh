#!/usr/bin/env bash
# Keyless kind scenarios. On failure, CI collects diagnostics separately via
# scripts/collect-kind-diagnostics.sh; locally run that script after a failed e2e.
set -euo pipefail

# shellcheck source=scripts/lib/common.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib/common.sh"
ROOT_DIR="$(repo_root)"
TOOLS_NAMESPACE="kontext-e2e-tools"
APPLY_EXAMPLE="${ROOT_DIR}/scripts/apply-example.sh"

cleanup_e2e_resources() {
  local status=0
  kubectl delete agent echo-service --ignore-not-found=true --wait=true || status=1
  kubectl delete agentrun \
    echo-review \
    plain-logs \
    native-envelope \
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

pod_phase_is() {
  local pod="$1"
  local expected="$2"
  signal_phase="$(
    kubectl get pod "${pod}" -o jsonpath='{.status.phase}' 2>/dev/null || true
  )"
  [[ "${signal_phase}" == "${expected}" ]]
}

echo "==> cleaning previous e2e resources"
cleanup_e2e_resources

echo "==> applying standalone echo task run"
"${APPLY_EXAMPLE}" echo-task-run.yaml

echo "==> waiting for AgentRun to succeed"
wait_for_run_phase echo-review Succeeded
phase="$(kubectl get agentrun echo-review -o jsonpath='{.status.phase}')"
result="$(kubectl get agentrun echo-review -o jsonpath='{.status.result}')"
if [[ "${phase}" != "Succeeded" ]]; then
  echo "expected echo-review to succeed, got phase=${phase}" >&2
  exit 1
fi
echo "task result: ${result}"

echo "==> verifying plain image logs without structured result capture"
"${APPLY_EXAMPLE}" plain-logs-run.yaml
wait_for_run_phase plain-logs Succeeded
plain_result="$(kubectl get agentrun plain-logs -o jsonpath='{.status.result}')"
plain_output="$(kubectl get agentrun plain-logs -o jsonpath='{.status.output}')"
plain_logs="$(kubectl logs run-plain-logs -c runtime)"
plain_init_containers="$(
  kubectl get pod run-plain-logs -o jsonpath='{.spec.initContainers[*].name}'
)"
if [[ -n "${plain_result}" ||
  -n "${plain_output}" ||
  "${plain_logs}" != *"ordinary image log"* ||
  "${plain_init_containers}" == *"inject-reporter"* ]]; then
  echo "plain image path unexpectedly captured a result or changed its logs" >&2
  exit 1
fi

echo "==> verifying a native versioned termination envelope"
"${APPLY_EXAMPLE}" native-envelope-run.yaml
wait_for_run_phase native-envelope Succeeded
native_result="$(kubectl get agentrun native-envelope -o jsonpath='{.status.result}')"
native_input_tokens="$(
  kubectl get agentrun native-envelope -o jsonpath='{.status.usage.inputTokens}'
)"
native_output_tokens="$(
  kubectl get agentrun native-envelope -o jsonpath='{.status.usage.outputTokens}'
)"
native_init_containers="$(
  kubectl get pod run-native-envelope -o jsonpath='{.spec.initContainers[*].name}'
)"
if [[ "${native_result}" != '{"answer":"native"}' ||
  "${native_input_tokens}" != "0" ||
  "${native_output_tokens}" != "1" ||
  "${native_init_containers}" == *"inject-reporter"* ]]; then
  echo "native envelope path did not preserve structured output without injection" >&2
  exit 1
fi

echo "==> verifying last-line capture from an unmodified image"
"${APPLY_EXAMPLE}" stdout-last-line-run.yaml
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
"${APPLY_EXAMPLE}" stdout-envelope-run.yaml
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
"${APPLY_EXAMPLE}" stdout-failure-run.yaml
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
"${APPLY_EXAMPLE}" stdout-signal-run.yaml
if ! wait_until 60 2 "stdout signal Pod to reach Running" \
  pod_phase_is run-stdout-signal Running; then
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
"${APPLY_EXAMPLE}" reference-fake-run.yaml
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
"${APPLY_EXAMPLE}" reference-fake-tool-run.yaml
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
"${APPLY_EXAMPLE}" reference-kind-policy-runs.yaml

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

echo "keyless kind end-to-end scenarios passed"
