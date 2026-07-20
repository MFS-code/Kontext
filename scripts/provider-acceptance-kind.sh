#!/usr/bin/env bash
set -euo pipefail

# shellcheck source=scripts/lib/common.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib/common.sh"
ROOT_DIR="$(repo_root)"

provider="${KONTEXT_PROVIDER:?KONTEXT_PROVIDER is required}"
model="${KONTEXT_MODEL:?KONTEXT_MODEL is required}"
scenario="${KONTEXT_ACCEPTANCE_SCENARIO:-tool}"
endpoint="${KONTEXT_PROVIDER_ENDPOINT:-}"
namespace="${KONTEXT_ACCEPTANCE_NAMESPACE:-provider-acceptance}"
runtime_image="${KONTEXT_REFERENCE_IMAGE:-kontext-reference:dev}"
run_name="provider-${scenario}"
secret_name="provider-acceptance-credentials"
knowledge_name="provider-acceptance-knowledge"
record_path="${KONTEXT_ACCEPTANCE_RECORD:-${KONTEXT_EVAL_DIR:-eval-results}/provider-acceptance.json}"
started_epoch="$(date +%s)"
phase="NotStarted"
failure_stage="initialization"
pass=false
usage_json='{}'
turns_json='null'
tool_calls_json='null'
commit_sha="${GITHUB_SHA:-}"
run_id="${GITHUB_RUN_ID:-local}"
run_attempt="${GITHUB_RUN_ATTEMPT:-1}"

need jq

if [[ -z "${commit_sha}" ]]; then
  commit_sha="$(git -C "${ROOT_DIR}" rev-parse HEAD 2>/dev/null || printf 'unknown')"
fi

write_acceptance_record() {
  local completed_epoch duration_millis record_dir temp_path
  completed_epoch="$(date +%s)"
  duration_millis="$(( (completed_epoch - started_epoch) * 1000 ))"
  record_dir="$(dirname "${record_path}")"
  mkdir -p "${record_dir}"
  temp_path="${record_path}.tmp"
  jq -n \
    --arg apiVersion "kontext.dev/eval/v1alpha1" \
    --arg commitSHA "${commit_sha:0:64}" \
    --arg failureStage "${failure_stage:0:128}" \
    --arg kind "ProviderAcceptanceRecord" \
    --arg model "${model:0:256}" \
    --arg phase "${phase:0:64}" \
    --arg provider "${provider:0:64}" \
    --arg runAttempt "${run_attempt:0:32}" \
    --arg runID "${run_id:0:128}" \
    --arg scenario "${scenario:0:64}" \
    --argjson durationMillis "${duration_millis}" \
    --argjson measuredUsage "${usage_json}" \
    --argjson pass "${pass}" \
    --argjson toolCalls "${tool_calls_json}" \
    --argjson turns "${turns_json}" \
    '{
      apiVersion: $apiVersion,
      kind: $kind,
      provider: $provider,
      model: $model,
      scenario: $scenario,
      commitSHA: $commitSHA,
      runID: $runID,
      runAttempt: $runAttempt,
      phase: $phase,
      durationMillis: $durationMillis,
      measuredUsage: $measuredUsage,
      turns: $turns,
      toolCalls: $toolCalls,
      pass: $pass
    }
    | if $pass then . else .failureStage = $failureStage end' >"${temp_path}"
  mv "${temp_path}" "${record_path}"
}

on_exit() {
  local status=$?
  trap - EXIT
  if [[ "${status}" -eq 0 ]]; then
    pass=true
    failure_stage=""
  fi
  if ! write_acceptance_record; then
    echo "failed to write provider acceptance record: ${record_path}" >&2
    if [[ "${status}" -eq 0 ]]; then
      status=1
    fi
  fi
  exit "${status}"
}

trap on_exit EXIT

if [[ "${KONTEXT_ACCEPTANCE_INITIALIZE_ONLY:-false}" == "true" ]]; then
  failure_stage="workflow_not_started"
  write_acceptance_record
  trap - EXIT
  exit 0
fi

if [[ -n "${endpoint}" ]]; then
  if [[ "${endpoint}" != http://* && "${endpoint}" != https://* ]]; then
    echo "provider endpoint must be an absolute HTTP(S) URL" >&2
    exit 1
  fi
  if [[ "${endpoint}" == *"@"* ||
    "${endpoint}" == *"?"* ||
    "${endpoint}" == *"#"* ||
    "${endpoint}" =~ [[:space:]] ]]; then
    echo "provider acceptance endpoint cannot contain credentials, query parameters, fragments, or whitespace" >&2
    exit 1
  fi
fi

case "${provider}" in
  anthropic)
    normalized_provider="anthropic"
    credential_env="ANTHROPIC_API_KEY"
    other_credential_env="OPENAI_API_KEY"
    ;;
  openai)
    normalized_provider="openai"
    credential_env="OPENAI_API_KEY"
    other_credential_env="ANTHROPIC_API_KEY"
    ;;
  openai-compatible)
    normalized_provider="openai-compatible"
    credential_env="OPENAI_API_KEY"
    other_credential_env="ANTHROPIC_API_KEY"
    ;;
  *)
    echo "unsupported acceptance provider: ${provider}" >&2
    exit 1
    ;;
esac

case "${scenario}" in
  tool)
    goal='Call read_knowledge exactly once with {"path":"acceptance.txt"}. You must use the tool before answering. Then reply with exactly the file contents and no quotes, markdown, punctuation, or explanation.'
    expected_result="KONTEXT_TOOL_ACCEPTANCE_OK"
    max_turns="2"
    ;;
  text)
    goal="Reply with exactly the word kontext and nothing else."
    expected_result="kontext"
    max_turns="1"
    ;;
  *)
    echo "unsupported acceptance scenario: ${scenario}" >&2
    exit 1
    ;;
esac

render_run_manifest() {
  jq -n \
    --arg endpoint "${endpoint}" \
    --arg goal "${goal}" \
    --arg knowledgeName "${knowledge_name}" \
    --arg maxTurns "${max_turns}" \
    --arg model "${model}" \
    --arg name "${run_name}" \
    --arg namespace "${namespace}" \
    --arg provider "${provider}" \
    --arg runtimeImage "${runtime_image}" \
    --arg scenario "${scenario}" \
    --arg secretName "${secret_name}" \
    '{
      apiVersion: "kontext.dev/v1alpha1",
      kind: "AgentRun",
      metadata: {
        name: $name,
        namespace: $namespace
      },
      spec: {
        goal: $goal,
        provider: $provider,
        model: $model,
        secretRef: {
          name: $secretName
        },
        runtime: {
          image: $runtimeImage
        },
        env: [
          {
            name: "KONTEXT_MAX_TURNS",
            value: $maxTurns
          },
          {
            name: "KONTEXT_MAX_TOOL_CALLS",
            value: "1"
          },
          {
            name: "KONTEXT_MAX_TOOL_RESULT_BYTES",
            value: "256"
          },
          {
            name: "KONTEXT_MAX_TOTAL_TOOL_OUTPUT_BYTES",
            value: "256"
          }
        ],
        budget: {
          tokens: 2048,
          wallclock: "90s"
        }
      }
    }
    | if $scenario == "tool" then
        .spec.tools = ["read_knowledge"]
        | .spec.knowledgeConfigMapRef = {name: $knowledgeName}
      else
        .
      end
    | if $endpoint != "" then
        .spec.env += [{
          name: "KONTEXT_PROVIDER_ENDPOINT",
          value: $endpoint
        }]
      else
        .
      end'
}

if [[ "${KONTEXT_ACCEPTANCE_RENDER_ONLY:-false}" == "true" ]]; then
  trap - EXIT
  render_run_manifest
  exit 0
fi

need kubectl

failure_stage="credential_check"
if [[ -z "${KONTEXT_PROVIDER_API_KEY:-}" ]]; then
  echo "the selected provider credential is empty" >&2
  exit 1
fi

phase="Preparing"
failure_stage="namespace_setup"
echo "==> preparing isolated namespace"
kubectl create namespace "${namespace}" \
  --dry-run=client \
  -o yaml |
  kubectl apply -f - >/dev/null

# Stream the key into kubectl instead of placing it in a command argument,
# generated manifest, checked-in file, or workflow output.
printf '%s' "${KONTEXT_PROVIDER_API_KEY}" |
  kubectl create secret generic "${secret_name}" \
    --namespace "${namespace}" \
    --from-file="${credential_env}=/dev/stdin" \
    --dry-run=client \
    -o yaml |
  kubectl apply -f - >/dev/null

if [[ "${scenario}" == "tool" ]]; then
  kubectl create configmap "${knowledge_name}" \
    --namespace "${namespace}" \
    --from-literal="acceptance.txt=${expected_result}" \
    --dry-run=client \
    -o yaml |
    kubectl apply -f - >/dev/null
fi

phase="Pending"
failure_stage="create_agentrun"
echo "==> creating ${provider} ${scenario} AgentRun"
render_run_manifest | kubectl apply -f - >/dev/null

failure_stage="wait_for_terminal_phase"
echo "==> waiting for AgentRun success"
if ! wait_for_run_phase "${run_name}" Succeeded "${namespace}" 150 2; then
  exit 1
fi
phase="Succeeded"

run_json="$(
  kubectl get agentrun "${run_name}" \
    --namespace "${namespace}" \
    -o json
)"
usage_json="$(
  jq -c '
    (.status.usage // {})
    | with_entries(select(
        .key == "tokens"
        or .key == "inputTokens"
        or .key == "outputTokens"
        or .key == "dollars"
      ))
  ' <<<"${run_json}"
)"
failure_stage="validate_result"
result="$(jq -r '.status.result' <<<"${run_json}")"
if [[ "${result}" != "${expected_result}" ]]; then
  echo "unexpected final result: ${result}" >&2
  exit 1
fi

if ! jq -e '
  (.status.usage.inputTokens | type == "number" and . > 0)
  and (.status.usage.outputTokens | type == "number" and . > 0)
' <<<"${run_json}" >/dev/null; then
  echo "provider did not report positive measured input and output usage" >&2
  exit 1
fi

pod_name="$(jq -r '.status.podName' <<<"${run_json}")"
if [[ -z "${pod_name}" || "${pod_name}" == "null" ]]; then
  echo "AgentRun did not record a pod name in status" >&2
  exit 1
fi
pod_json="$(
  kubectl get pod "${pod_name}" \
    --namespace "${namespace}" \
    -o json
)"
if ! jq -e \
  --arg envName "${credential_env}" \
  --arg otherEnvName "${other_credential_env}" \
  --arg secretKey "${credential_env}" \
  --arg secretName "${secret_name}" '
  any(
    .spec.containers[]
    | select(.name == "runtime")
    | .env[];
    .name == $envName
    and .valueFrom.secretKeyRef.name == $secretName
    and .valueFrom.secretKeyRef.key == $secretKey
    and (has("value") | not)
  )
  and all(
    .spec.containers[]
    | select(.name == "runtime")
    | .env[];
    .name != $otherEnvName
  )
' <<<"${pod_json}" >/dev/null; then
  echo "runtime Pod did not use only the selected Secret-backed credential" >&2
  exit 1
fi

if ! kubectl get secret "${secret_name}" \
  --namespace "${namespace}" \
  -o json |
  jq -e --arg key "${credential_env}" '
    .type == "Opaque"
    and (.data | keys == [$key])
  ' >/dev/null; then
  echo "provider credential Secret had unexpected keys" >&2
  exit 1
fi

failure_stage="validate_envelope"
envelope="$(
  jq -c '
    [
      .status.containerStatuses[]?
      | select(.name == "runtime")
      | .state.terminated.message // empty
      | fromjson
    ]
    | last
  ' <<<"${pod_json}"
)"
if [[ "${envelope}" == "null" ]]; then
  echo "runtime Pod did not contain a terminal result envelope" >&2
  exit 1
fi

if ! jq -e \
  --arg expectedModel "${model}" \
  --arg expectedProvider "${normalized_provider}" '
    .apiVersion == "kontext.dev/result/v1alpha1"
    and .execution.provider == $expectedProvider
    and .execution.model == $expectedModel
  ' <<<"${envelope}" >/dev/null; then
  echo "terminal envelope did not preserve provider/model identity" >&2
  exit 1
fi

turns_json="$(jq -c '.execution.turns // null' <<<"${envelope}")"
tool_calls_json="$(jq -c '.execution.toolCalls // null' <<<"${envelope}")"

if [[ "${scenario}" == "tool" ]]; then
  failure_stage="validate_tool_event"
  logs="$(
    kubectl logs "${pod_name}" \
      --namespace "${namespace}" \
      --container runtime
  )"
  if ! jq -R -s -e '
    [
      split("\n")[]
      | fromjson?
      | select(
          .apiVersion == "kontext.dev/event/v1alpha1"
          and .type == "tool"
        )
    ] as $events
    | ($events | length) == 1
    and $events[0].data.name == "read_knowledge"
    and $events[0].data.count == 1
    and $events[0].data.isError == false
    and (($events[0].data.errorCode // "") == "")
  ' <<<"${logs}" >/dev/null ||
    ! jq -e '
      .apiVersion == "kontext.dev/result/v1alpha1"
      and .outcome == "Succeeded"
      and .execution.turns == 2
      and .execution.toolCalls == 1
    ' <<<"${envelope}" >/dev/null; then
    echo "tool scenario did not record exactly one successful read_knowledge event" >&2
    exit 1
  fi
else
  if ! jq -e '
    .apiVersion == "kontext.dev/result/v1alpha1"
    and .outcome == "Succeeded"
    and .execution.turns == 1
    and .execution.toolCalls == 0
  ' <<<"${envelope}" >/dev/null; then
    echo "text scenario had unexpected execution counts" >&2
    exit 1
  fi
fi

phase="Succeeded"
failure_stage="complete"
echo "provider acceptance passed: provider=${provider} scenario=${scenario}"
