#!/usr/bin/env bash
# Focused Scheduled-mode acceptance. Exactly one path waits for a real cron
# tick; overlap policy is forced through status so it is minute-boundary safe.
set -euo pipefail

# shellcheck source=scripts/lib/common.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib/common.sh"
ROOT_DIR="$(repo_root)"
APPLY_EXAMPLE="${ROOT_DIR}/scripts/apply-example.sh"
ECHO_IMAGE="${KONTEXT_ECHO_IMAGE:-kontext-echo:dev}"

cleanup() {
  kubectl delete agent echo-scheduled echo-scheduled-forbid \
    --ignore-not-found=true --wait=true >/dev/null 2>&1 || true
  kubectl delete agentrun echo-scheduled-forbid-active \
    --ignore-not-found=true --wait=true >/dev/null 2>&1 || true
}

on_exit() {
  local status=$?
  if [[ "${status}" -eq 0 ]]; then
    cleanup
  else
    echo "preserving failed Scheduled-mode resources for diagnostics" >&2
  fi
  return "${status}"
}

need kubectl
trap on_exit EXIT
cleanup

scheduled_run_exists() {
  scheduled_run="$(
    kubectl get agent echo-scheduled \
      -o jsonpath='{.status.lastRunName}' 2>/dev/null || true
  )"
  [[ -n "${scheduled_run}" ]]
}

agent_generation_observed() {
  observed_generation="$(
    kubectl get agent echo-scheduled-forbid \
      -o jsonpath='{.status.observedGeneration}' 2>/dev/null || true
  )"
  generation="$(
    kubectl get agent echo-scheduled-forbid \
      -o jsonpath='{.metadata.generation}' 2>/dev/null || true
  )"
  [[ -n "${generation}" && "${observed_generation}" == "${generation}" ]]
}

forbid_fixture_settled() {
  settled_reason="$(
    kubectl get agent echo-scheduled-forbid \
      -o jsonpath='{.status.conditions[?(@.type=="Progressing")].reason}' \
      2>/dev/null || true
  )"
  [[ "${settled_reason}" == "WaitingForSchedule" ]]
}

due_slot_advanced() {
  local due_slot="$1"
  evaluated_next="$(
    kubectl get agent echo-scheduled-forbid \
      -o jsonpath='{.status.nextScheduleTime}' 2>/dev/null || true
  )"
  [[ -n "${evaluated_next}" && "${evaluated_next}" != "${due_slot}" ]]
}

resource_absent() {
  ! kubectl get "$@" >/dev/null 2>&1
}

echo "==> waiting for one real Scheduled cron tick"
"${APPLY_EXAMPLE}" echo-scheduled-agent.yaml

scheduled_run=""
if ! wait_until 90 2 "Scheduled Agent to mint a run" scheduled_run_exists; then
  echo "Scheduled Agent did not mint a run from its controller requeue" >&2
  exit 1
fi

wait_for_run_phase "${scheduled_run}" Succeeded default 120
runs_created="$(kubectl get agent echo-scheduled -o jsonpath='{.status.runsCreated}')"
last_schedule_time="$(kubectl get agent echo-scheduled -o jsonpath='{.status.lastScheduleTime}')"
next_schedule_time="$(kubectl get agent echo-scheduled -o jsonpath='{.status.nextScheduleTime}')"
current_run="$(kubectl get agent echo-scheduled -o jsonpath='{.status.currentRunName}')"
restarts="$(kubectl get agent echo-scheduled -o jsonpath='{.status.restarts}')"
if [[ "${runs_created}" != "1" ||
  -z "${last_schedule_time}" ||
  -z "${next_schedule_time}" ||
  -n "${current_run}" ||
  -n "${restarts}" ]]; then
  echo "Scheduled status contract failed for ${scheduled_run}" >&2
  exit 1
fi

echo "==> proving Forbid without waiting for a minute boundary"
cat <<EOF | kubectl apply -f -
apiVersion: kontext.dev/v1alpha1
kind: Agent
metadata:
  name: echo-scheduled-forbid
spec:
  mode: Scheduled
  goal: Keep one owned run active while overlap is evaluated.
  provider: echo
  model: echo-model
  runtime:
    image: ${ECHO_IMAGE}
    command: ["/entrypoint.sh"]
  schedule:
    expression: "0 0 1 1 *"
    timeZone: Etc/UTC
    concurrencyPolicy: Forbid
    startingDeadlineSeconds: 3600
EOF

if ! wait_until 30 1 "Forbid fixture Agent initialization" \
  agent_generation_observed; then
  echo "Forbid fixture Agent was not initialized" >&2
  exit 1
fi

agent_uid="$(kubectl get agent echo-scheduled-forbid -o jsonpath='{.metadata.uid}')"
cat <<EOF | kubectl apply -f -
apiVersion: kontext.dev/v1alpha1
kind: AgentRun
metadata:
  name: echo-scheduled-forbid-active
  labels:
    kontext.dev/agent: echo-scheduled-forbid
  ownerReferences:
    - apiVersion: kontext.dev/v1alpha1
      kind: Agent
      name: echo-scheduled-forbid
      uid: ${agent_uid}
      controller: true
      blockOwnerDeletion: true
spec:
  agentRef:
    name: echo-scheduled-forbid
  goal: Stay active for deterministic Forbid verification.
  provider: echo
  model: echo-model
  runtime:
    image: ${ECHO_IMAGE}
    command: ["/entrypoint.sh"]
  env:
    - name: KONTEXT_MODE
      value: service
EOF

wait_for_run_phase echo-scheduled-forbid-active Running
if ! wait_until 30 1 "Forbid fixture to settle" forbid_fixture_settled; then
  echo "Forbid fixture did not settle before forced due slot" >&2
  exit 1
fi
if due_slot="$(date -u -d '1 minute ago' '+%Y-%m-%dT%H:%M:00Z' 2>/dev/null)"; then
  :
else
  due_slot="$(date -u -v-1M '+%Y-%m-%dT%H:%M:00Z')"
fi
kubectl patch agent echo-scheduled-forbid --subresource=status --type=merge \
  -p "{\"status\":{\"observedGeneration\":${generation},\"nextScheduleTime\":\"${due_slot}\"}}"
kubectl annotate agent echo-scheduled-forbid \
  "kontext.dev/force-reconcile=$(date +%s)" --overwrite

evaluated_next=""
if ! wait_until 30 1 "Forbid to advance the forced due slot" \
  due_slot_advanced "${due_slot}"; then
  echo "Forbid did not advance the forced due slot" >&2
  exit 1
fi
forbid_run_count="$(
  kubectl get agentruns -l kontext.dev/agent=echo-scheduled-forbid \
    -o jsonpath='{.items[*].metadata.name}' | wc -w | tr -d ' '
)"
if [[ "${forbid_run_count}" != "1" ]]; then
  echo "Forbid minted an overlapping run" >&2
  exit 1
fi

echo "==> verifying Agent deletion cascades to scheduled runs"
kubectl delete agent echo-scheduled --wait=true
if ! wait_until 30 1 "scheduled run deletion" \
  resource_absent agentrun "${scheduled_run}"; then
  echo "scheduled run survived Agent deletion" >&2
  exit 1
fi

echo "Scheduled-mode acceptance passed"
