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
  exit "${status}"
}

need curl
need docker
need kind
need kubectl
trap on_exit EXIT
PREVIOUS_CONTEXT="$(kubectl config current-context 2>/dev/null || true)"

if cluster_exists; then
  echo "kind cluster ${CLUSTER_NAME} already exists; choose a disposable KIND_CLUSTER_NAME" >&2
  exit 1
fi

KIND_CONFIG="$(mktemp)"
CALICO_MANIFEST="$(mktemp)"
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

echo "NetworkPolicy acceptance passed with Calico ${CALICO_VERSION}"
