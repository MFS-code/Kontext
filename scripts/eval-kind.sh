#!/usr/bin/env bash
# Deterministic, keyless evaluation acceptance against an already-installed kind
# cluster. The script reuses the images loaded by scripts/install-go-kind.sh.
set -euo pipefail

# shellcheck source=scripts/lib/common.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib/common.sh"
ROOT_DIR="$(repo_root)"
EVAL_NAMESPACE="${KONTEXT_EVAL_NAMESPACE:-kontext-eval}"
EVAL_DIR="${KONTEXT_EVAL_DIR:-${ROOT_DIR}/eval-results/kind}"
RECORDS="${EVAL_DIR}/keyless.jsonl"
SUMMARY="${EVAL_DIR}/keyless.summary.json"
MARKER="${EVAL_DIR}/not-run.json"
SERVICE_AGENT="echo-service"
APPLY_EXAMPLE="${ROOT_DIR}/scripts/apply-example.sh"

service_is_running() {
  local previous_run="$1"
  local current_run=""
  local pod_name=""
  local pod_phase=""
  current_run="$(
    kubectl get agent "${SERVICE_AGENT}" -n "${EVAL_NAMESPACE}" \
      -o jsonpath='{.status.currentRunName}' 2>/dev/null || true
  )"
  pod_name="$(
    kubectl get pod -n "${EVAL_NAMESPACE}" \
      -l "kontext.dev/agent=${SERVICE_AGENT}" \
      -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true
  )"
  pod_phase="$(
    kubectl get pod "${pod_name}" -n "${EVAL_NAMESPACE}" \
      -o jsonpath='{.status.phase}' 2>/dev/null || true
  )"
  if [[ -n "${current_run}" &&
    "${current_run}" != "${previous_run}" &&
    "${pod_phase}" == "Running" ]]; then
    printf '%s\n' "${current_run}"
    return 0
  fi
  return 1
}

wait_for_service() {
  local previous_run="${1:-}"
  if wait_until 60 2 "Service to become Running with a fresh run" \
    service_is_running "${previous_run}"; then
    return 0
  fi
  kubectl get agent,agentrun,pod -n "${EVAL_NAMESPACE}" -o wide >&2 || true
  return 1
}

cleanup() {
  local status=$?
  if [[ "${status}" -eq 0 ]]; then
    if ! kubectl delete namespace "${EVAL_NAMESPACE}" \
      --ignore-not-found=true \
      --wait=true \
      --timeout=60s >/dev/null; then
      echo "warning: evaluation passed but namespace cleanup did not finish" >&2
    fi
  else
    echo "preserving namespace ${EVAL_NAMESPACE} for diagnostics" >&2
    kubectl get agent,agentrun,pod -n "${EVAL_NAMESPACE}" -o wide >&2 || true
  fi
  return "${status}"
}

need go jq kubectl
trap cleanup EXIT

mkdir -p "${EVAL_DIR}"
rm -f "${RECORDS}" "${SUMMARY}"
if [[ -f "${MARKER}" ]]; then
  marker_temporary="${MARKER}.tmp"
  jq '
    .state = "Running"
    | .failureStage = "eval_script_started"
  ' "${MARKER}" >"${marker_temporary}"
  mv "${marker_temporary}" "${MARKER}"
fi

echo "==> preparing disposable evaluation namespace"
kubectl delete namespace "${EVAL_NAMESPACE}" \
  --ignore-not-found=true \
  --wait=true \
  --timeout=60s >/dev/null
kubectl create namespace "${EVAL_NAMESPACE}" >/dev/null
kubectl apply -n "${EVAL_NAMESPACE}" \
  -f "${ROOT_DIR}/evals/fixtures/keyless-setup.yaml" >/dev/null

echo "==> running external keyless evaluation suite"
(
  cd "${ROOT_DIR}"
  go run ./cmd/kontext-eval \
    --suite evals/suites/keyless.yaml \
    --namespace "${EVAL_NAMESPACE}" \
    --records "${RECORDS}" \
    --summary "${SUMMARY}" \
    --keep-runs
)

echo "==> validating machine-readable evaluation summary"
jq -e '
  .apiVersion == "kontext.dev/eval/v1alpha1"
  and .suite == "keyless"
  and .expectedTotal == .total
  and .total > 0
  and .passed == .total
  and .failed == 0
  and .collectionErrorCount == 0
  and .assertionFailures == 0
  and .pass == true
  and ((.assertions // []) | all(.pass == true))
' "${SUMMARY}" >/dev/null

wallclock_pod="$(
  jq -r 'select(.caseId == "controller-wallclock") | .run.podName' "${RECORDS}"
)"
wallclock_run="$(
  jq -r 'select(.caseId == "controller-wallclock") | .run.name' "${RECORDS}"
)"
for _ in $(seq 1 30); do
  if ! kubectl get pod "${wallclock_pod}" -n "${EVAL_NAMESPACE}" >/dev/null 2>&1; then
    break
  fi
  sleep 1
done
wallclock_phase="$(
  kubectl get agentrun "${wallclock_run}" -n "${EVAL_NAMESPACE}" \
    -o jsonpath='{.status.phase}' 2>/dev/null || true
)"
if [[ -z "${wallclock_pod}" ||
  -z "${wallclock_run}" ||
  "${wallclock_phase}" != "BudgetExceeded" ]] ||
  kubectl get pod "${wallclock_pod}" -n "${EVAL_NAMESPACE}" >/dev/null 2>&1; then
  echo "wallclock case did not record and clean up its runtime Pod" >&2
  exit 1
fi

echo "==> proving Service mode omits wallclock deadlines and recasts"
"${APPLY_EXAMPLE}" echo-service-agent.yaml -n "${EVAL_NAMESPACE}" >/dev/null
first_run="$(wait_for_service)"
first_pod="$(
  kubectl get pod -n "${EVAL_NAMESPACE}" \
    -l "kontext.dev/agent=${SERVICE_AGENT}" \
    -o jsonpath='{.items[0].metadata.name}'
)"

if ! kubectl get agent "${SERVICE_AGENT}" -n "${EVAL_NAMESPACE}" -o json |
  jq -e '((.spec.budget // {}) | has("wallclock") | not)' >/dev/null; then
  echo "Service example unexpectedly configures a wallclock budget" >&2
  exit 1
fi
if ! kubectl get agentrun "${first_run}" -n "${EVAL_NAMESPACE}" -o json |
  jq -e '((.spec.budget // {}) | has("wallclock") | not)' >/dev/null; then
  echo "Service run unexpectedly inherited a wallclock budget" >&2
  exit 1
fi
if ! kubectl get pod "${first_pod}" -n "${EVAL_NAMESPACE}" -o json |
  jq -e '.spec.activeDeadlineSeconds == null and .status.phase == "Running"' >/dev/null; then
  echo "Service Pod has a deadline or is not Running" >&2
  exit 1
fi

kubectl delete pod "${first_pod}" -n "${EVAL_NAMESPACE}" --wait=true >/dev/null
second_run="$(wait_for_service "${first_run}")"
if [[ "${second_run}" == "${first_run}" ]]; then
  echo "Service controller did not recast a fresh AgentRun" >&2
  exit 1
fi

rm -f "${MARKER}"
echo "keyless eval acceptance passed: records=${RECORDS} summary=${SUMMARY}"
