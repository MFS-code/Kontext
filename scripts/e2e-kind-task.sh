#!/usr/bin/env bash
# Focused Task admission, execution, status, and ownership acceptance.
set -euo pipefail

# shellcheck source=scripts/lib/common.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib/common.sh"
ROOT_DIR="$(repo_root)"
APPLY_EXAMPLE="${ROOT_DIR}/scripts/apply-example.sh"
TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/kontext-task-e2e.XXXXXX")"

cleanup() {
  kubectl delete agent echo-task task-static task-invalid task-wrong-mode \
    --ignore-not-found=true --wait=false >/dev/null 2>&1 || true
  kubectl delete agentrun \
    echo-task-docs task-static-run task-concurrent-a task-concurrent-b \
    --ignore-not-found=true --wait=false >/dev/null 2>&1 || true
  rm -rf "${TMP_DIR}"
}
trap cleanup EXIT

need kubectl jq
cleanup
mkdir -p "${TMP_DIR}"

wait_for_agent_count() {
  local expected="$1"
  for _ in $(seq 1 120); do
    local count=""
    count="$(kubectl get agent echo-task -o jsonpath='{.status.runsCreated}' 2>/dev/null || true)"
    [[ "${count:-0}" == "${expected}" ]] && return 0
    sleep 1
  done
  echo "timed out waiting for Task runsCreated=${expected}" >&2
  return 1
}

expect_rejected() {
  local name="$1"
  local text="$2"
  shift 2
  if "$@" >"${TMP_DIR}/${name}.out" 2>&1; then
    echo "${name} unexpectedly passed Task admission" >&2
    return 1
  fi
  if ! grep -q "${text}" "${TMP_DIR}/${name}.out"; then
    echo "${name} did not report ${text}" >&2
    cat "${TMP_DIR}/${name}.out" >&2
    return 1
  fi
}

echo "==> creating parameterized Task Agent"
"${APPLY_EXAMPLE}" echo-task-agent.yaml
for _ in $(seq 1 60); do
  ready="$(
    kubectl get agent echo-task \
      -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null || true
  )"
  [[ "${ready}" == "True" ]] && break
  sleep 1
done
[[ "${ready}" == "True" ]]
if [[ "$(kubectl get agentrun -l kontext.dev/agent=echo-task -o name)" != "" ]]; then
  echo "Task Agent creation minted an AgentRun" >&2
  exit 1
fi

echo "==> invoking sparse Task through API admission"
"${APPLY_EXAMPLE}" echo-task-invocation.yaml
wait_for_run_phase echo-task-docs Succeeded
goal="$(kubectl get agentrun echo-task-docs -o jsonpath='{.spec.goal}')"
result="$(kubectl get agentrun echo-task-docs -o jsonpath='{.status.result}')"
parameter="$(kubectl get agentrun echo-task-docs -o jsonpath='{.spec.parameters.subject}')"
label="$(kubectl get agentrun echo-task-docs -o jsonpath='{.metadata.labels.kontext\.dev/agent}')"
owner="$(kubectl get agentrun echo-task-docs -o jsonpath='{.metadata.ownerReferences[?(@.controller==true)].name}')"
if [[ "${goal}" != 'Summarize Task admission; preserve the literal ${subject}.' ||
  "${result}" != *"${goal}"* ||
  "${parameter}" != "Task admission" ||
  "${label}" != "echo-task" ||
  "${owner}" != "echo-task" ]]; then
  echo "sparse Task did not persist and execute the resolved snapshot" >&2
  exit 1
fi

echo "==> proving resolved snapshots are immutable"
expect_rejected immutable 'AgentRun spec is immutable' \
  kubectl patch agentrun echo-task-docs --type=merge -p='{"spec":{"goal":"changed"}}'

echo "==> proving static and concurrent user-named invocations"
cat <<EOF | kubectl apply -f -
apiVersion: kontext.dev/v1alpha1
kind: Agent
metadata:
  name: task-static
spec:
  mode: Task
  goal: Run the static Task.
  provider: echo
  model: echo-model
  runtime:
    image: ${KONTEXT_ECHO_IMAGE:-kontext-echo:dev}
---
apiVersion: kontext.dev/v1alpha1
kind: AgentRun
metadata:
  name: task-static-run
spec:
  agentRef:
    name: task-static
EOF
wait_for_run_phase task-static-run Succeeded

for name in task-concurrent-a task-concurrent-b; do
  kubectl create -f - >"${TMP_DIR}/${name}.create" <<EOF &
apiVersion: kontext.dev/v1alpha1
kind: AgentRun
metadata:
  name: ${name}
spec:
  agentRef:
    name: echo-task
  parameters:
    subject: ${name}
EOF
done
wait
wait_for_run_phase task-concurrent-a Succeeded
wait_for_run_phase task-concurrent-b Succeeded
wait_for_agent_count 3

newest="$(
  kubectl get agentrun -l kontext.dev/agent=echo-task -o json |
    jq -r '.items | sort_by(.metadata.creationTimestamp, .metadata.name) | last.metadata.name'
)"
last_run="$(kubectl get agent echo-task -o jsonpath='{.status.lastRunName}')"
if [[ "${last_run}" != "${newest}" ]]; then
  echo "lastRunName=${last_run}, newest retained run=${newest}" >&2
  exit 1
fi
kubectl delete agentrun "${newest}" --wait=true
wait_for_agent_count 2

echo "==> proving actionable Task admission rejection"
cat <<EOF | kubectl apply -f -
apiVersion: kontext.dev/v1alpha1
kind: Agent
metadata:
  name: task-invalid
spec:
  mode: Task
  goalTemplate: 'broken \${name'
  model: echo-model
  runtime:
    image: ${KONTEXT_ECHO_IMAGE:-kontext-echo:dev}
---
apiVersion: kontext.dev/v1alpha1
kind: Agent
metadata:
  name: task-wrong-mode
spec:
  mode: Service
  goal: Stay running.
  model: echo-model
  runtime:
    image: ${KONTEXT_ECHO_IMAGE:-kontext-echo:dev}
EOF
expect_rejected missing-agent MissingAgent kubectl create -f - <<'EOF'
apiVersion: kontext.dev/v1alpha1
kind: AgentRun
metadata:
  name: task-reject-missing-agent
spec:
  agentRef:
    name: missing
EOF
expect_rejected wrong-mode WrongMode kubectl create -f - <<'EOF'
apiVersion: kontext.dev/v1alpha1
kind: AgentRun
metadata:
  name: task-reject-wrong-mode
spec:
  agentRef:
    name: task-wrong-mode
EOF
expect_rejected missing-parameter MissingParameters kubectl create -f - <<'EOF'
apiVersion: kontext.dev/v1alpha1
kind: AgentRun
metadata:
  name: task-reject-missing-parameter
spec:
  agentRef:
    name: echo-task
EOF
expect_rejected unused-parameter UnusedParameters kubectl create -f - <<'EOF'
apiVersion: kontext.dev/v1alpha1
kind: AgentRun
metadata:
  name: task-reject-unused-parameter
spec:
  agentRef:
    name: echo-task
  parameters:
    subject: valid
    extra: rejected
EOF
expect_rejected invalid-template InvalidTemplate kubectl create -f - <<'EOF'
apiVersion: kontext.dev/v1alpha1
kind: AgentRun
metadata:
  name: task-reject-invalid-template
spec:
  agentRef:
    name: task-invalid
  parameters:
    name: value
EOF
expect_rejected locked-field ConflictingFields kubectl create -f - <<'EOF'
apiVersion: kontext.dev/v1alpha1
kind: AgentRun
metadata:
  name: task-reject-locked-field
spec:
  agentRef:
    name: echo-task
  parameters:
    subject: valid
  goal: override
EOF

echo "==> proving Agent ownership cascades retained Task runs"
kubectl delete agent echo-task --wait=true
for _ in $(seq 1 120); do
  [[ "$(kubectl get agentrun -l kontext.dev/agent=echo-task -o name 2>/dev/null || true)" == "" ]] && break
  sleep 1
done
if [[ "$(kubectl get agentrun -l kontext.dev/agent=echo-task -o name 2>/dev/null || true)" != "" ]]; then
  echo "deleting the Task Agent did not garbage-collect retained runs" >&2
  exit 1
fi

echo "focused Task mode acceptance passed"
