#!/usr/bin/env bash
# NetworkPolicy acceptance needs an enforcing CNI; kindnet does not implement
# NetworkPolicy. Calico is pinned and checksum-verified so this evidence cannot
# silently change with a mutable remote manifest.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CLUSTER_NAME="${KIND_CLUSTER_NAME:-kontext-network-policy}"
NAMESPACE="kontext-network-policy-e2e"
DIAG_DIR="${KONTEXT_DIAG_DIR:-${ROOT_DIR}/.ci-diagnostics}"
CALICO_VERSION="v3.30.3"
CALICO_MANIFEST_URL="https://raw.githubusercontent.com/projectcalico/calico/${CALICO_VERSION}/manifests/calico.yaml"
CALICO_MANIFEST_SHA256="9382d2b27a76f40c170454b408653e6d71e2205ef0aef069e942bb690e7381d0"
KIND_CONFIG=""
CALICO_MANIFEST=""
WORK_DIR=""
CLUSTER_CREATED=false
PREVIOUS_CONTEXT=""

need() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "missing required command: $1" >&2
    exit 1
  fi
}

cluster_exists() {
  local cluster
  while read -r cluster; do
    if [[ "${cluster}" == "${CLUSTER_NAME}" ]]; then
      return 0
    fi
  done < <(kind get clusters)
  return 1
}

verify_sha256() {
  local file="$1"
  local actual=""
  if command -v sha256sum >/dev/null 2>&1; then
    actual="$(sha256sum "${file}")"
  elif command -v shasum >/dev/null 2>&1; then
    actual="$(shasum -a 256 "${file}")"
  else
    echo "missing required command: sha256sum or shasum" >&2
    return 1
  fi
  actual="${actual%% *}"
  if [[ "${actual}" != "${CALICO_MANIFEST_SHA256}" ]]; then
    echo "Calico manifest checksum mismatch: expected ${CALICO_MANIFEST_SHA256}, got ${actual}" >&2
    return 1
  fi
}

require_ipv4() {
  local name="$1"
  local value="$2"
  if [[ ! "${value}" =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}$ ]]; then
    echo "${name} is not a single IPv4 address: ${value}" >&2
    return 1
  fi
}

wait_for_run_phase() {
  local name="$1"
  local expected="$2"
  local phase=""
  for _ in $(seq 1 90); do
    phase="$(
      kubectl get agentrun "${name}" -n "${NAMESPACE}" \
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

wait_for_pod_absent() {
  local name="$1"
  for _ in $(seq 1 60); do
    if ! kubectl get pod "${name}" -n "${NAMESPACE}" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  echo "timed out waiting for Pod ${name} to be deleted" >&2
  return 1
}

wait_for_no_browser_processes() {
  local details=""
  for _ in $(seq 1 60); do
    if details="$(
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
    )"; then
      return 0
    fi
    sleep 1
  done
  echo "Playwright retained browser processes after bounded cleanup polling:" >&2
  echo "${details}" >&2
  return 1
}

wait_for_browser_processes() {
  local count=""
  for _ in $(seq 1 80); do
    count="$(
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
    if [[ "${count}" =~ ^[0-9]+$ && "${count}" -gt 0 ]]; then
      echo "observed ${count} active Chromium processes before cancellation"
      return 0
    fi
    sleep 0.25
  done
  echo "no Chromium process was observed before wallclock cancellation" >&2
  return 1
}

wait_for_runtime_log_pattern() {
  local pod="$1"
  local pattern="$2"
  local logs=""
  for _ in $(seq 1 80); do
    logs="$(
      kubectl logs "${pod}" -n "${NAMESPACE}" -c runtime 2>/dev/null || true
    )"
    if [[ "${logs}" == *"${pattern}"* ]]; then
      return 0
    fi
    sleep 0.25
  done
  echo "${pod} did not reach expected tool state: ${pattern}" >&2
  return 1
}

validate_tool_events() {
  local logfile="$1"
  local expected_count="$2"
  local max_each="$3"
  local max_total="$4"
  local expected_names="$5"
  local expected_errors="$6"
  local minimum_truncations="$7"
  python3 - "${logfile}" "${expected_count}" "${max_each}" "${max_total}" \
    "${expected_names}" "${expected_errors}" "${minimum_truncations}" <<'PY'
import json
import sys

path, count, max_each, max_total, names, errors, minimum_truncations = sys.argv[1:]
events = []
with open(path, encoding="utf-8") as stream:
    for line in stream:
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            continue
        if event.get("type") == "tool":
            events.append(event["data"])

expected_count = int(count)
if len(events) != expected_count:
    raise SystemExit(f"expected {expected_count} tool events, got {len(events)}: {events!r}")
expected_names = names.split(",") if names else []
actual_names = [event.get("name") for event in events]
if actual_names != expected_names:
    raise SystemExit(f"expected tool order {expected_names!r}, got {actual_names!r}")
if any("output" in event for event in events):
    raise SystemExit("tool event content was emitted without KONTEXT_EMIT_TOOL_OUTPUT")
byte_counts = [int(event.get("outputBytes", -1)) for event in events]
if any(value < 0 or value > int(max_each) for value in byte_counts):
    raise SystemExit(f"tool output byte counts exceed per-result bound: {byte_counts!r}")
if sum(byte_counts) > int(max_total):
    raise SystemExit(f"tool output byte counts exceed cumulative bound: {byte_counts!r}")
error_count = sum(bool(event.get("isError")) for event in events)
if error_count != int(errors):
    raise SystemExit(f"expected {errors} tool errors, got {error_count}: {events!r}")
truncation_count = sum(bool(event.get("truncated")) for event in events)
if truncation_count < int(minimum_truncations):
    raise SystemExit(
        f"expected at least {minimum_truncations} truncated tool results, "
        f"got {truncation_count}: {events!r}"
    )
PY
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
  python3 - "${pod_file}" "${container}" "${mode}" <<'PY'
import json
import sys

path, container_name, mode = sys.argv[1:]
with open(path, encoding="utf-8") as stream:
    pod = json.load(stream)
containers = pod["spec"]["containers"]
selected = next((item for item in containers if item["name"] == container_name), None)
if selected is None:
    raise SystemExit(f"container {container_name!r} is missing")
if selected.get("envFrom"):
    raise SystemExit(f"{mode} container must not use envFrom")
environment = selected.get("env", [])
if any(item.get("valueFrom") is not None for item in environment):
    raise SystemExit(f"{mode} container has a Secret or other valueFrom environment source")

if mode == "browser":
    expected_environment = {
        "HOME": "/tmp/home",
        "TMPDIR": "/tmp",
        "XDG_CACHE_HOME": "/tmp/xdg-cache",
        "XDG_CONFIG_HOME": "/tmp/xdg-config",
        "PLAYWRIGHT_MCP_PING_TIMEOUT_MS": "30000",
    }
    actual_environment = {item["name"]: item.get("value", "") for item in environment}
    if actual_environment != expected_environment:
        raise SystemExit(
            "browser environment names differ from the explicit allowlist: "
            f"{sorted(actual_environment)}"
        )
    expected_volumes = {"tmp", "dev-shm"}
    volumes = pod["spec"].get("volumes", [])
    if {item["name"] for item in volumes} != expected_volumes:
        raise SystemExit(f"browser volume names differ from allowlist: {volumes!r}")
    if any(set(item) != {"name", "emptyDir"} for item in volumes):
        raise SystemExit("browser volume uses a source other than emptyDir")
    expected_mounts = {"tmp": "/tmp", "dev-shm": "/dev/shm"}
    actual_mounts = {
        item["name"]: item["mountPath"] for item in selected.get("volumeMounts", [])
    }
    if actual_mounts != expected_mounts:
        raise SystemExit(f"browser volume mounts differ from allowlist: {actual_mounts!r}")
    if pod["spec"].get("automountServiceAccountToken") is not False:
        raise SystemExit("browser Pod must explicitly disable ServiceAccount token automount")
elif mode == "runtime":
    expected_names = {
        "KONTEXT_RUN_NAME",
        "KONTEXT_AGENT_NAME",
        "KONTEXT_GOAL",
        "KONTEXT_PROVIDER",
        "KONTEXT_MODEL",
        "KONTEXT_TOOLS",
        "KONTEXT_BUDGET_TOKENS",
        "KONTEXT_BUDGET_WALLCLOCK",
        "KONTEXT_BUDGET_DOLLARS",
        "KONTEXT_MCP_CONFIG",
        "KONTEXT_FAKE_SCENARIO",
        "KONTEXT_FAKE_TOOL_SEQUENCE",
        "KONTEXT_MAX_TURNS",
        "KONTEXT_MAX_TOOL_CALLS",
        "KONTEXT_MAX_TOOL_RESULT_BYTES",
        "KONTEXT_MAX_TOTAL_TOOL_OUTPUT_BYTES",
    }
    actual_names = {item["name"] for item in environment}
    if actual_names != expected_names:
        raise SystemExit(
            "fake runtime environment names differ from expected literals: "
            f"{sorted(actual_names)}"
        )
    if pod["spec"].get("volumes"):
        raise SystemExit("fake runtime Pod must not contain Secret, projected, or other volumes")
    if selected.get("volumeMounts"):
        raise SystemExit("fake runtime container must not contain volume mounts")
else:
    raise SystemExit(f"unsupported isolation mode {mode!r}")
PY

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

collect_network_diagnostics() {
  mkdir -p "${DIAG_DIR}"
  "${ROOT_DIR}/scripts/collect-kind-diagnostics.sh" "${DIAG_DIR}" || true
  {
    echo "# network policies"
    kubectl get networkpolicies -A -o yaml || true
    echo
    echo "# Calico workloads"
    kubectl get pods -n kube-system -l k8s-app=calico-node -o wide || true
    kubectl describe daemonset -n kube-system calico-node || true
    kubectl logs -n kube-system -l k8s-app=calico-node \
      --all-containers --tail=300 || true
    echo
    echo "# fixture endpoints"
    kubectl get pod,service,endpoints -n "${NAMESPACE}" -o wide || true
  } >"${DIAG_DIR}/network-policy.txt" 2>&1
}

on_exit() {
  local status=$?
  trap - EXIT
  set +e
  if [[ "${status}" -ne 0 && "${CLUSTER_CREATED}" == "true" ]]; then
    echo "==> collecting NetworkPolicy diagnostics" >&2
    collect_network_diagnostics
  fi
  if [[ "${CLUSTER_CREATED}" == "true" ]]; then
    echo "==> deleting kind cluster ${CLUSTER_NAME}"
    kind delete cluster --name "${CLUSTER_NAME}"
  fi
  if [[ -n "${PREVIOUS_CONTEXT}" ]]; then
    kubectl config use-context "${PREVIOUS_CONTEXT}" >/dev/null || true
  fi
  [[ -z "${KIND_CONFIG}" ]] || rm -f "${KIND_CONFIG}"
  [[ -z "${CALICO_MANIFEST}" ]] || rm -f "${CALICO_MANIFEST}"
  [[ -z "${WORK_DIR}" ]] || rm -rf "${WORK_DIR}"
  exit "${status}"
}

need curl
need docker
need kind
need kubectl
need python3
trap on_exit EXIT
PREVIOUS_CONTEXT="$(kubectl config current-context 2>/dev/null || true)"

if cluster_exists; then
  echo "kind cluster ${CLUSTER_NAME} already exists; choose a disposable KIND_CLUSTER_NAME" >&2
  exit 1
fi

KIND_CONFIG="$(mktemp)"
CALICO_MANIFEST="$(mktemp)"
WORK_DIR="$(mktemp -d)"
cat >"${KIND_CONFIG}" <<'EOF'
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
networking:
  disableDefaultCNI: true
  podSubnet: 192.168.0.0/16
nodes:
  - role: control-plane
EOF

echo "==> creating CNI-less kind cluster ${CLUSTER_NAME}"
CLUSTER_CREATED=true
kind create cluster --name "${CLUSTER_NAME}" --config "${KIND_CONFIG}"

echo "==> installing checksum-verified Calico ${CALICO_VERSION}"
curl --fail --location --silent --show-error --retry 3 \
  "${CALICO_MANIFEST_URL}" \
  --output "${CALICO_MANIFEST}"
verify_sha256 "${CALICO_MANIFEST}"
kubectl apply -f "${CALICO_MANIFEST}"
kubectl rollout status daemonset/calico-node -n kube-system --timeout=300s
kubectl rollout status deployment/calico-kube-controllers -n kube-system --timeout=300s
kubectl wait --for=condition=Ready nodes --all --timeout=300s

echo "==> installing focused Kontext images"
KONTEXT_KIND_IMAGE_SET=network-policy \
  KIND_CLUSTER_NAME="${CLUSTER_NAME}" \
  "${ROOT_DIR}/scripts/install-go-kind.sh"

echo "==> applying NetworkPolicy fixture and policy"
kubectl apply -f \
  "${ROOT_DIR}/deploy/examples/v1alpha1/reference-kind-network-policy.yaml"
kubernetes_service_ip="$(
  kubectl get service kubernetes -n default -o jsonpath='{.spec.clusterIP}'
)"
control_plane_ip="$(
  docker inspect \
    --format '{{(index .NetworkSettings.Networks "kind").IPAddress}}' \
    "${CLUSTER_NAME}-control-plane"
)"
require_ipv4 "Kubernetes Service IP" "${kubernetes_service_ip}"
require_ipv4 "kind control-plane IP" "${control_plane_ip}"
api_egress_patch="$(
  cat <<EOF
[
  {
    "op": "add",
    "path": "/spec/egress/-",
    "value": {
      "to": [{"ipBlock": {"cidr": "${kubernetes_service_ip}/32"}}],
      "ports": [{"protocol": "TCP", "port": 443}]
    }
  },
  {
    "op": "add",
    "path": "/spec/egress/-",
    "value": {
      "to": [{"ipBlock": {"cidr": "${control_plane_ip}/32"}}],
      "ports": [{"protocol": "TCP", "port": 6443}]
    }
  }
]
EOF
)"
kubectl patch networkpolicy reference-runtime-egress \
  -n "${NAMESPACE}" \
  --type=json \
  --patch "${api_egress_patch}"
kubectl wait --for=condition=Ready pod/network-policy-http \
  -n "${NAMESPACE}" \
  --timeout=120s

echo "==> starting policy-selected runtime probes"
kubectl apply -f \
  "${ROOT_DIR}/deploy/examples/v1alpha1/reference-kind-network-policy-runs.yaml"
wait_for_run_phase reference-network-http Succeeded
wait_for_run_phase reference-network-kubernetes Succeeded

http_logs="$(
  kubectl logs run-reference-network-http -n "${NAMESPACE}" -c runtime
)"
kubernetes_logs="$(
  kubectl logs run-reference-network-kubernetes -n "${NAMESPACE}" -c runtime
)"
selected_runtime_count="$(
  kubectl get pods -n "${NAMESPACE}" \
    -l app.kubernetes.io/name=kontext-agentrun \
    -o name | wc -l | tr -d ' '
)"

if [[ "${selected_runtime_count}" != "2" ||
  "${http_logs}" != *"dns-allowed-and-port-blocked"* ||
  "${http_logs}" == *"unexpected-egress-success"* ]]; then
  echo "runtime HTTP probe did not prove allowed DNS/8080 and denied 8081" >&2
  exit 1
fi
if [[ "${kubernetes_logs}" != *'"name":"kubernetes_read"'* ||
  "${kubernetes_logs}" != *'"errorCode":"","isError":false'* ||
  "${kubernetes_logs}" == *"kubernetes_request_failed"* ]]; then
  echo "kubernetes_read could not use the policy-allowed Kubernetes API path" >&2
  exit 1
fi

echo "==> applying restricted Playwright MCP and deterministic HTML fixtures"
kubectl apply -f \
  "${ROOT_DIR}/deploy/examples/v1alpha1/reference-playwright-mcp.yaml"
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
kubectl apply -f \
  "${ROOT_DIR}/deploy/examples/v1alpha1/reference-playwright-browser-run.yaml"
wait_for_run_phase reference-playwright-browser Succeeded
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
kubectl apply -f \
  "${ROOT_DIR}/deploy/examples/v1alpha1/reference-playwright-fresh-run.yaml"
wait_for_run_phase reference-playwright-fresh Succeeded
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
kubectl apply -f \
  "${ROOT_DIR}/deploy/examples/v1alpha1/reference-playwright-deny-run.yaml"
wait_for_run_phase reference-playwright-deny Succeeded
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
kubectl apply -f \
  "${ROOT_DIR}/deploy/examples/v1alpha1/reference-playwright-cancel-run.yaml"
kubectl wait --for=condition=Ready pod/run-reference-playwright-cancel \
  -n "${NAMESPACE}" --timeout=90s
assert_strict_pod_isolation run-reference-playwright-cancel runtime runtime
wait_for_runtime_log_pattern \
  run-reference-playwright-cancel \
  '"stopReason":"tool_use","turn":2'
wait_for_browser_processes
wait_for_run_phase reference-playwright-cancel BudgetExceeded
wait_for_pod_absent run-reference-playwright-cancel
wait_for_no_browser_processes

echo "NetworkPolicy and Playwright MCP acceptance passed with Calico ${CALICO_VERSION}"
