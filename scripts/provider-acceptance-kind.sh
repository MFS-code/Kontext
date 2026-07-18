#!/usr/bin/env bash
set -euo pipefail

need() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "missing required command: $1" >&2
    exit 1
  fi
}

provider="${KONTEXT_PROVIDER:?KONTEXT_PROVIDER is required}"
model="${KONTEXT_MODEL:?KONTEXT_MODEL is required}"
scenario="${KONTEXT_ACCEPTANCE_SCENARIO:-tool}"
endpoint="${KONTEXT_PROVIDER_ENDPOINT:-}"
namespace="${KONTEXT_ACCEPTANCE_NAMESPACE:-provider-acceptance}"
runtime_image="${KONTEXT_REFERENCE_IMAGE:-kontext-reference:dev}"
run_name="provider-${scenario}"
secret_name="provider-acceptance-credentials"
knowledge_name="provider-acceptance-knowledge"

case "${provider}" in
  anthropic)
    credential_env="ANTHROPIC_API_KEY"
    other_credential_env="OPENAI_API_KEY"
    ;;
  openai | openai-compatible)
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

need jq

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
  render_run_manifest
  exit 0
fi

need kubectl

if [[ -z "${KONTEXT_PROVIDER_API_KEY:-}" ]]; then
  echo "the selected provider credential is empty" >&2
  exit 1
fi

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

echo "==> creating ${provider} ${scenario} AgentRun"
render_run_manifest | kubectl apply -f - >/dev/null

echo "==> waiting for AgentRun success"
phase=""
for _ in $(seq 1 75); do
  phase="$(
    kubectl get agentrun "${run_name}" \
      --namespace "${namespace}" \
      -o jsonpath='{.status.phase}' 2>/dev/null || true
  )"
  case "${phase}" in
    Succeeded)
      break
      ;;
    Failed | BudgetExceeded)
      echo "AgentRun reached terminal phase ${phase}" >&2
      exit 1
      ;;
    "" | Pending | Running) ;;
    *)
      echo "AgentRun reached unexpected phase ${phase}" >&2
      exit 1
      ;;
  esac
  sleep 2
done

if [[ "${phase}" != "Succeeded" ]]; then
  echo "timed out waiting for AgentRun; last phase=${phase}" >&2
  exit 1
fi

run_json="$(
  kubectl get agentrun "${run_name}" \
    --namespace "${namespace}" \
    -o json
)"
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

logs="$(
  kubectl logs "${pod_name}" \
    --namespace "${namespace}" \
    --container runtime
)"
envelope="$(
  jq -R -s -c '
    [
      split("\n")[]
      | select(startswith("KONTEXT_RESULT:"))
      | sub("^KONTEXT_RESULT:[[:space:]]*"; "")
      | fromjson
    ]
    | last
  ' <<<"${logs}"
)"
if [[ "${envelope}" == "null" ]]; then
  echo "runtime logs did not contain a terminal result envelope" >&2
  exit 1
fi

if [[ "${scenario}" == "tool" ]]; then
  tool_event_count="$(
    jq -R -s '
      [
        split("\n")[]
        | fromjson?
        | select(
            .apiVersion == "kontext.dev/event/v1alpha1"
            and .type == "tool"
            and .data.name == "read_knowledge"
          )
      ]
      | length
    ' <<<"${logs}"
  )"
  if [[ "${tool_event_count}" != "1" ]] ||
    ! jq -e '
      .outcome == "Succeeded"
      and .execution.turns == 2
      and .execution.toolCalls == 1
    ' <<<"${envelope}" >/dev/null; then
    echo "tool scenario did not record exactly one read_knowledge call" >&2
    exit 1
  fi
else
  if ! jq -e '
    .outcome == "Succeeded"
    and .execution.turns == 1
    and .execution.toolCalls == 0
  ' <<<"${envelope}" >/dev/null; then
    echo "text scenario had unexpected execution counts" >&2
    exit 1
  fi
fi

echo "provider acceptance passed: provider=${provider} scenario=${scenario}"
