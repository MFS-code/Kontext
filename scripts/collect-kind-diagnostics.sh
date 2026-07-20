#!/usr/bin/env bash
set -euo pipefail

# shellcheck source=scripts/lib/common.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib/common.sh"
ROOT_DIR="$(repo_root)"
OUT_DIR="${1:-${ROOT_DIR}/.ci-diagnostics}"
NAMESPACE="${KONTEXT_NAMESPACE:-kontext-system}"

dump_logs_for_label() {
  local label="$1"
  local out="$2"
  {
    echo "# pods with label ${label}"
    kubectl get pods -A -l "${label}" -o wide || true
    echo
    echo "# logs"
    while read -r ns name; do
      [[ -z "${ns}" || -z "${name}" ]] && continue
      echo "----- ${ns}/${name} -----"
      kubectl logs -n "${ns}" "${name}" --all-containers --tail=200 || true
    done < <(kubectl get pods -A -l "${label}" -o jsonpath='{range .items[*]}{.metadata.namespace}{" "}{.metadata.name}{"\n"}{end}' 2>/dev/null || true)
  } >>"${out}" 2>&1 || true
}

need kubectl

mkdir -p "${OUT_DIR}"

echo "==> collecting kind diagnostics into ${OUT_DIR}"

{
  echo "# cluster-info"
  kubectl cluster-info || true
  echo
  echo "# nodes"
  kubectl get nodes -o wide || true
  echo
  echo "# namespaces"
  kubectl get ns || true
} >"${OUT_DIR}/cluster.txt" 2>&1 || true

{
  echo "# pods -A"
  kubectl get pods -A -o wide || true
  echo
  echo "# deployments -A"
  kubectl get deploy -A -o wide || true
  echo
  echo "# events -A (newest first)"
  kubectl get events -A --sort-by=.lastTimestamp || true
} >"${OUT_DIR}/workload-overview.txt" 2>&1 || true

{
  echo "# controller-manager deployment"
  kubectl get deploy -n "${NAMESPACE}" controller-manager -o yaml || true
  echo
  echo "# controller-manager pods"
  kubectl get pods -n "${NAMESPACE}" -l control-plane=controller-manager -o wide || true
  echo
  echo "# controller-manager describe"
  kubectl describe deploy -n "${NAMESPACE}" controller-manager || true
  kubectl describe pods -n "${NAMESPACE}" -l control-plane=controller-manager || true
} >"${OUT_DIR}/controller.txt" 2>&1 || true

kubectl logs -n "${NAMESPACE}" -l control-plane=controller-manager --all-containers --tail=500 >"${OUT_DIR}/controller.log" 2>&1 || true
kubectl logs -n "${NAMESPACE}" -l control-plane=controller-manager --all-containers --previous --tail=500 >"${OUT_DIR}/controller-previous.log" 2>&1 || true

{
  echo "# agents"
  kubectl get agents -A -o wide || true
  echo
  echo "# scheduled agents"
  kubectl get agents -A \
    -o custom-columns='NAMESPACE:.metadata.namespace,NAME:.metadata.name,SUSPEND:.spec.schedule.suspend,LAST:.status.lastScheduleTime,NEXT:.status.nextScheduleTime,RUN:.status.lastRunName' \
    2>/dev/null || true
  echo
  echo "# agentruns"
  kubectl get agentruns -A -o wide || true
  echo
  echo "# agent descriptions"
  kubectl describe agents -A || true
  echo
  echo "# agentrun descriptions"
  kubectl describe agentruns -A || true
} >"${OUT_DIR}/kontext-resources.txt" 2>&1 || true

: >"${OUT_DIR}/agent-workloads.txt"
dump_logs_for_label 'kontext.dev/run' "${OUT_DIR}/agent-workloads.txt"
dump_logs_for_label 'kontext.dev/agent' "${OUT_DIR}/agent-workloads.txt"

{
  echo "# CRDs"
  kubectl get crd agents.kontext.dev agentruns.kontext.dev -o yaml || true
} >"${OUT_DIR}/crds.txt" 2>&1 || true

echo "==> diagnostics written to ${OUT_DIR}"
ls -la "${OUT_DIR}" || true
