#!/usr/bin/env bash
# Focused Playwright MCP isolation and NetworkPolicy acceptance. This runs
# against the Calico kind cluster prepared by e2e-kind-network-policy.sh.
set -euo pipefail

# shellcheck source=scripts/lib/common.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib/common.sh"
ROOT_DIR="$(repo_root)"
NAMESPACE="${KONTEXT_NETWORK_POLICY_NAMESPACE:-kontext-network-policy-e2e}"
APPLY_EXAMPLE="${ROOT_DIR}/scripts/apply-example.sh"
WORK_DIR="${KONTEXT_NETWORK_POLICY_WORK_DIR:-}"
OWNS_WORK_DIR=false

if [[ -z "${WORK_DIR}" ]]; then
  WORK_DIR="$(mktemp -d)"
  OWNS_WORK_DIR=true
fi

cleanup() {
  if [[ "${OWNS_WORK_DIR}" == "true" ]]; then
    rm -rf "${WORK_DIR}"
  fi
}
trap cleanup EXIT

wait_for_pod_absent() {
  local name="$1"
  wait_until 60 1 "Pod ${name} deletion" \
    pod_absent "${name}"
}

pod_absent() {
  local name="$1"
  ! kubectl get pod "${name}" -n "${NAMESPACE}" >/dev/null 2>&1
}

no_browser_processes() {
  browser_process_details="$(
    kubectl exec deployment/playwright-mcp -n "${NAMESPACE}" -- \
      node -e '
        const fs = require("fs");
        const active = [];
        for (const entry of fs.readdirSync("/proc")) {
          if (!/^[0-9]+$/.test(entry) || Number(entry) === process.pid) continue;
          try {
            const command = fs.readFileSync(`/proc/${entry}/cmdline`, "utf8").replace(/\0/g, " ");
            if (command.includes("/ms-playwright/") && /(chrome|chromium)/i.test(command)) {
              active.push(`${entry}:${command}`);
            }
          } catch {}
        }
        if (active.length) {
          console.error(active.join("\n"));
          process.exit(1);
        }
      ' 2>&1
  )"
}

wait_for_no_browser_processes() {
  browser_process_details=""
  if wait_until 60 1 "Playwright browser process cleanup" \
    no_browser_processes; then
    return 0
  fi
  echo "${browser_process_details}" >&2
  return 1
}

browser_processes_present() {
  browser_process_count="$(
    kubectl exec deployment/playwright-mcp -n "${NAMESPACE}" -- \
      node -e '
        const fs = require("fs");
        let count = 0;
        for (const entry of fs.readdirSync("/proc")) {
          if (!/^[0-9]+$/.test(entry) || Number(entry) === process.pid) continue;
          try {
            const command = fs.readFileSync(`/proc/${entry}/cmdline`, "utf8").replace(/\0/g, " ");
            if (command.includes("/ms-playwright/") && /(chrome|chromium)/i.test(command)) count++;
          } catch {}
        }
        process.stdout.write(String(count));
      ' 2>/dev/null || true
  )"
  [[ "${browser_process_count}" =~ ^[0-9]+$ && "${browser_process_count}" -gt 0 ]]
}

wait_for_browser_processes() {
  browser_process_count=""
  wait_until 80 0.25 "Chromium process before wallclock cancellation" \
    browser_processes_present
  echo "observed ${browser_process_count} active Chromium processes before cancellation"
}

runtime_log_contains() {
  local pod="$1"
  local pattern="$2"
  local logs=""
  logs="$(kubectl logs "${pod}" -n "${NAMESPACE}" -c runtime 2>/dev/null || true)"
  [[ "${logs}" == *"${pattern}"* ]]
}

wait_for_runtime_log_pattern() {
  local pod="$1"
  local pattern="$2"
  wait_until 80 0.25 "${pod} tool state ${pattern}" \
    runtime_log_contains "${pod}" "${pattern}"
}

validate_tool_events() {
  "${ROOT_DIR}/scripts/validators/validate-tool-events.py" "$@"
}

assert_strict_pod_isolation() {
  local pod="$1"
  local container="$2"
  local mode="$3"
  local pod_file="${WORK_DIR}/${mode}-pod.json"
  local process_names=""
  local service_account=""
  local token_automount=""

  kubectl get pod "${pod}" -n "${NAMESPACE}" -o json >"${pod_file}"
  "${ROOT_DIR}/scripts/validators/validate-pod-isolation.py" \
    "${pod_file}" "${container}" "${mode}"

  service_account="$(
    kubectl get pod "${pod}" -n "${NAMESPACE}" \
      -o jsonpath='{.spec.serviceAccountName}'
  )"
  token_automount="$(
    kubectl get serviceaccount "${service_account}" -n "${NAMESPACE}" \
      -o jsonpath='{.automountServiceAccountToken}'
  )"
  if [[ "${token_automount}" != "false" ]]; then
    echo "${pod}/${container} ServiceAccount permits token automount" >&2
    return 1
  fi

  if [[ "${container}" == "mcp" ]]; then
    process_names="$(
      kubectl exec "${pod}" -n "${NAMESPACE}" -c "${container}" -- \
        node -e 'for (const entry of require("fs").readFileSync("/proc/1/environ", "utf8").split("\0")) { const index = entry.indexOf("="); if (index > 0) console.log(entry.slice(0, index)); }'
    )"
  else
    process_names="$(
      kubectl exec "${pod}" -n "${NAMESPACE}" -c "${container}" -- \
        /bin/sh -c 'tr "\000" "\n" </proc/1/environ | cut -d= -f1'
    )"
  fi
  while read -r name; do
    if [[ "${mode}" == "runtime" && "${name}" == KONTEXT_* ]]; then
      continue
    fi
    if [[ "${name}" =~ (API_KEY|TOKEN|SECRET|PASSWORD|CREDENTIAL) ||
      "${name}" == AWS_* ||
      "${name}" == AZURE_* ||
      "${name}" == GOOGLE_* ||
      "${name}" == ANTHROPIC_* ||
      "${name}" == OPENAI_* ]]; then
      echo "${pod}/${container} process environment contains credential-shaped variable ${name}" >&2
      return 1
    fi
  done <<<"${process_names}"
}

need kubectl python3

echo "==> applying restricted Playwright MCP and deterministic HTML fixtures"
"${APPLY_EXAMPLE}" reference-playwright-mcp.yaml
kubectl rollout status deployment/playwright-fixture \
  -n "${NAMESPACE}" --timeout=180s
kubectl rollout status deployment/unrelated-http \
  -n "${NAMESPACE}" --timeout=180s
kubectl rollout status deployment/playwright-mcp \
  -n "${NAMESPACE}" --timeout=300s

playwright_pod="$(
  kubectl get pods -n "${NAMESPACE}" \
    -l app.kubernetes.io/name=playwright-mcp \
    -o jsonpath='{.items[0].metadata.name}'
)"
assert_strict_pod_isolation "${playwright_pod}" mcp browser
wait_for_no_browser_processes

echo "==> running deterministic browser interaction"
"${APPLY_EXAMPLE}" reference-playwright-browser-run.yaml
wait_for_run_phase reference-playwright-browser Succeeded "${NAMESPACE}" 180 2
kubectl logs run-reference-playwright-browser -n "${NAMESPACE}" -c runtime \
  >"${WORK_DIR}/playwright-browser.log"
browser_result="$(
  kubectl get agentrun reference-playwright-browser -n "${NAMESPACE}" \
    -o jsonpath='{.status.result}'
)"
browser_logs="$(<"${WORK_DIR}/playwright-browser.log")"
validate_tool_events \
  "${WORK_DIR}/playwright-browser.log" \
  5 480 2048 \
  "browser_navigate,browser_snapshot,browser_type,browser_click,browser_snapshot" \
  0 1
if [[ "${browser_result}" != *"Kontext accepted"* ||
  "${browser_logs}" != *'"toolCalls":5'* ]]; then
  echo "browser run did not complete the expected five-call interaction" >&2
  exit 1
fi
wait_for_no_browser_processes

echo "==> proving browser profile isolation across AgentRuns"
"${APPLY_EXAMPLE}" reference-playwright-fresh-run.yaml
wait_for_run_phase reference-playwright-fresh Succeeded "${NAMESPACE}" 180 2
kubectl logs run-reference-playwright-fresh -n "${NAMESPACE}" -c runtime \
  >"${WORK_DIR}/playwright-fresh.log"
fresh_result="$(
  kubectl get agentrun reference-playwright-fresh -n "${NAMESPACE}" \
    -o jsonpath='{.status.result}'
)"
validate_tool_events \
  "${WORK_DIR}/playwright-fresh.log" \
  2 1024 2048 \
  "browser_navigate,browser_snapshot" \
  0 0
if [[ "${fresh_result}" != *"State: empty"* ||
  "${fresh_result}" == *"Kontext accepted"* ]]; then
  echo "fresh AgentRun observed browser state from the prior session" >&2
  exit 1
fi
wait_for_no_browser_processes

echo "==> proving browser egress deny rules"
"${APPLY_EXAMPLE}" reference-playwright-deny-run.yaml
wait_for_run_phase reference-playwright-deny Succeeded "${NAMESPACE}" 180 2
kubectl logs run-reference-playwright-deny -n "${NAMESPACE}" -c runtime \
  >"${WORK_DIR}/playwright-deny.log"
deny_result="$(
  kubectl get agentrun reference-playwright-deny -n "${NAMESPACE}" \
    -o jsonpath='{.status.result}'
)"
validate_tool_events \
  "${WORK_DIR}/playwright-deny.log" \
  2 512 1024 \
  "browser_navigate,browser_navigate" \
  2 0
if [[ "${deny_result}" != *'"isError":true'* ||
  "${deny_result}" != *"mcp_timeout"* ]]; then
  echo "browser deny run did not return both blocked destinations as tool errors" >&2
  exit 1
fi
wait_for_no_browser_processes

echo "==> proving wallclock cancellation cleans the browser session"
"${APPLY_EXAMPLE}" reference-playwright-cancel-run.yaml
kubectl wait --for=condition=Ready pod/run-reference-playwright-cancel \
  -n "${NAMESPACE}" --timeout=90s
assert_strict_pod_isolation run-reference-playwright-cancel runtime runtime
wait_for_runtime_log_pattern \
  run-reference-playwright-cancel \
  '"stopReason":"tool_use","turn":2'
wait_for_browser_processes
wait_for_run_phase reference-playwright-cancel BudgetExceeded "${NAMESPACE}" 180 2
wait_for_pod_absent run-reference-playwright-cancel
wait_for_no_browser_processes

echo "Playwright MCP acceptance passed"
