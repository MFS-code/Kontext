# Kontext reference runtime

The maintained reference runtime is a small provider-neutral execution loop.
It demonstrates how a model-backed agent behaves as a Kubernetes workload
without adopting an agent framework.

The runtime includes a deterministic `fake` provider plus direct Anthropic
Messages and OpenAI-compatible Chat Completions HTTP transports.

## Execution model

The image bundles two static binaries:

```text
kontext-reporter (PID 1)
└─ kontext-reference
```

The runtime emits versioned JSONL lifecycle, usage, output, and error events to
stdout. Its final line is a `KONTEXT_RESULT:`-prefixed result envelope. The
reporter preserves the logs and process exit status, compacts the envelope, and
writes `/dev/termination-log`.

No private chain-of-thought is emitted.

## Configuration

| Environment variable | Required | Meaning |
|---|---|---|
| `KONTEXT_GOAL` | yes | Concrete task for this run |
| `KONTEXT_PROVIDER` | yes | `fake`, `anthropic`, `openai`, or `openai-compatible` |
| `KONTEXT_MODEL` | yes | Opaque provider model identifier |
| `KONTEXT_RUN_NAME` | no | Run metadata; defaults to `unknown-run` |
| `KONTEXT_AGENT_NAME` | no | Agent metadata; defaults to run name |
| `KONTEXT_TOOLS` | no | Comma-separated built-in tool allowlist |
| `KONTEXT_BUDGET_TOKENS` | no | Cumulative provider-reported token limit across all requests |
| `KONTEXT_BUDGET_WALLCLOCK` | no | Optional Go duration; omitted means disabled |
| `KONTEXT_BUDGET_DOLLARS` | no | Optional non-negative recorded budget |
| `KONTEXT_MAX_TURNS` | no | Maximum provider completions in one run |
| `KONTEXT_MAX_TOOL_CALLS` | no | Maximum executed tool calls in one run |
| `KONTEXT_MAX_TOOL_RESULT_BYTES` | no | Maximum bytes returned from one tool call |
| `KONTEXT_MAX_TOTAL_TOOL_OUTPUT_BYTES` | no | Maximum tool-result bytes returned across the run |
| `KONTEXT_EMIT_TOOL_OUTPUT` | no | Include bounded tool content in events; defaults to `false` |
| `KONTEXT_PROVIDER_ENDPOINT` | no | Exact absolute HTTP(S) request endpoint |
| `KONTEXT_PROVIDER_BASE_URL` | no | Absolute HTTP(S) base URL; provider path is appended |
| `ANTHROPIC_API_KEY` | Anthropic only | Anthropic API key, normally injected from a Secret |
| `OPENAI_API_KEY` | OpenAI-compatible only | Bearer token, normally injected from a Secret |
| `KONTEXT_FAKE_SCENARIO` | no | `success`, `failure`, `malformed`, `delay`, or `tool` |
| `KONTEXT_FAKE_DELAY` | delay only | Positive Go duration such as `250ms` |
| `KONTEXT_FAKE_TOOL_NAME` | tool only | Deterministic fake-provider tool name |
| `KONTEXT_FAKE_TOOL_ARGUMENTS` | tool only | Deterministic fake-provider JSON arguments |

There is deliberately no hidden five-minute deadline. The Kubernetes
controller remains authoritative for `KONTEXT_BUDGET_WALLCLOCK`; the runtime
parses the value but does not start a competing timer. It reacts promptly when
the reporter forwards controller cancellation signals.

Model identifiers pass through unchanged.

`KONTEXT_PROVIDER_ENDPOINT` and `KONTEXT_PROVIDER_BASE_URL` are mutually
exclusive. A base URL may contain a path prefix. The runtime appends
`/v1/messages` for Anthropic or `/chat/completions` for OpenAI-compatible
providers. An exact endpoint is used without modification.

## Token accounting

The token budget covers the whole run, not one provider request. After each
response, the runtime adds that request's reported input and output usage. If
the provider also reports a larger total, the runtime uses that total for the
budget check. Missing usage is not estimated.

Tool loops resend the goal, prior assistant messages, tool calls, bounded tool
results, and tool definitions. Providers therefore count some conversation
and tool context again on every follow-up request. Reasoning models may also
include reasoning tokens in completion usage even when the visible answer is
short.

The runtime sends the remaining budget as the provider's completion limit
where the API supports one, then checks measured cumulative usage after the
response. Provider completion limits do not include every kind of input usage,
so a response may push the measured run total over budget. In that case the
run fails with `token_limit_exceeded`; the token budget is not a provider-side
hard cap.

Leave headroom for repeated context, tool results, and provider-specific
reasoning usage. Exact usage can vary between otherwise identical live runs as
models and provider accounting change. The example budgets are starting
points, not sizing guarantees.

## Maintained HTTP compatibility

### Anthropic

- Sends one non-streaming `POST` to the Messages API with
  `anthropic-version: 2023-06-01`.
- Defaults to `https://api.anthropic.com/v1/messages`.
- Sends `KONTEXT_BUDGET_TOKENS` as `max_tokens`. Because Anthropic requires
  that field, the transport uses `4096` when the budget is omitted.
- Normalizes text and client `tool_use` blocks; current documented stop
  reasons; input/output token usage; and the `request-id` header.

### OpenAI-compatible

- `openai` and `openai-compatible` use the same transport.
- Sends one non-streaming `POST` to the Chat Completions API and defaults to
  `https://api.openai.com/v1/chat/completions`.
- Treats the base URL as the OpenAI API root (normally ending in `/v1`) and
  appends `/chat/completions`.
- Sends `model`, text `messages`, and optional `max_completion_tokens`.
- Normalizes assistant text, function `tool_calls`, documented finish reasons,
  optional prompt/completion/total usage, optional
  `completion_tokens_details.reasoning_tokens`, and the `x-request-id` header.

OpenAI completion totals can exceed the tokenized visible text because they
may include hidden reasoning tokens. When the API reports that breakdown, the
runtime preserves it as optional `usage.reasoningTokens` in the usage event
and final versioned result envelope. It never estimates the value when the
detail is absent, and a reported zero remains distinct from absence.

`OpenAI-compatible` means compatibility with that request, response, auth, and
tool-call shape. It does not include the Responses API, streaming, embeddings,
realtime transports, Azure deployment URL construction, or vendor-specific
extensions. Endpoint operators are responsible for accepting a bearer token
in `Authorization`; a non-secret placeholder may be used only when an
in-cluster endpoint ignores auth.

Both transports receive only the tools explicitly listed in `KONTEXT_TOOLS`.
The runtime executes normalized calls, returns normalized results to the
provider, and continues until final output or a configured limit.

HTTP failures use stable result error codes including
`authentication_failed`, `rate_limited`, `provider_timeout`,
`provider_network_error`, `provider_endpoint_error`,
`provider_request_rejected`, and `invalid_provider_response`. Retryability,
and provider request IDs are retained where available. HTTP status informs the
normalized error code but is not included in the terminal envelope. Response
bodies are bounded to 4 MiB.

## Built-in tools

The maintained runtime includes three tools:

- `read_knowledge` reads one UTF-8 file below `/kontext/knowledge`. Absolute
  paths, parent traversal, escaping symlinks, directories, and oversized reads
  are rejected or truncated.
- `kubernetes_read` performs current-namespace `get` or `list` operations for
  a fixed resource allowlist. It has no namespace argument and never permits
  Secrets. Kubernetes RBAC remains authoritative.
- `shell` runs `/bin/sh -c` as the runtime container user in a required
  absolute working directory. Its direct child environment excludes provider,
  Kubernetes, and other credential-shaped variables. Stdout and stderr stream
  to container logs while the result returned to the model remains bounded.

Allowlisting a tool makes it visible to the model; it does not grant new
infrastructure permissions. The Pod's ServiceAccount, security context,
mounted volumes, filesystem permissions, and NetworkPolicy determine what a
tool can actually access.

Environment filtering is defense in depth, not a Secret boundary. The shell
runs in the same container as the runtime. Filtering changes only the direct
child's environment; it does not isolate files, sibling process metadata, or
other resources available inside that container. Do not expose `shell` to
workloads that must not access the runtime container's credentials.

Tool errors are returned to the model so it may recover within the remaining
limits. A tool error alone does not fail the run. The model may still produce a
successful final response. Unknown or non-allowlisted calls follow this path
as structured tool errors and are never executed. Cancellation and runtime or
provider failures fail the run immediately.

Assistant text from a turn that requests tools is conversation context, not
the run's final output. The runtime publishes output only from a terminal
provider response with no tool calls and a successful terminal stop reason. If
a configured limit is reached before that response, the run fails without
promoting earlier assistant text to final output.

Tool events include identity, timing, byte count, error code, and truncation
metadata. Tool content is omitted from events unless
`KONTEXT_EMIT_TOOL_OUTPUT=true` because event logs may leave the workload's
namespace or retention boundary.

The turn, tool-call, per-result, and total-output limits are configurable run
limits. They are disabled when their environment variables are omitted or set
to `0`; examples use finite values. The result limits bound content returned to
the model. Separately, each built-in tool has a fixed 8 MiB capture safety
ceiling. Disabling a configured result limit does not remove that ceiling, and
the per-result setting cannot raise it. Shell stdout and stderr continue to
stream to container logs after model-facing capture reaches the ceiling.

## Local fake-provider run

```bash
KONTEXT_GOAL="Explain the runtime contract" \
KONTEXT_PROVIDER=fake \
KONTEXT_MODEL="any-opaque-model-id" \
go run ./runtimes/reference
```

Build the production image:

```bash
make docker-build-reference
```

## Kubernetes credentials and examples

The controller reads `spec.secretRef` and injects only the maintained
transport's expected environment variable. Create Secrets without putting
credentials in source control:

```bash
kubectl create secret generic kontext-anthropic \
  --from-literal=ANTHROPIC_API_KEY="$ANTHROPIC_API_KEY"

kubectl create secret generic kontext-openai \
  --from-literal=OPENAI_API_KEY="$OPENAI_API_KEY"
```

Example templates live in `deploy/examples/v1alpha1/`:

- `provider-secrets.example.yaml`
- `reference-anthropic-run.yaml`
- `reference-openai-compatible-run.yaml`
- `reference-fake-tool-run.yaml`
- `reference-tools-run.yaml`

The `Provider acceptance` GitHub Actions workflow is dispatch-only and uses
the `provider-acceptance` environment. Repository maintainers must protect
that environment with required reviewers and store `ANTHROPIC_API_KEY` and
`OPENAI_API_KEY` as environment secrets. Pull-request CI never receives or
uses these credentials.

Each dispatch builds the maintained operator and reference runtime, loads them
into an ephemeral kind cluster, and injects only the selected provider key
through a namespace-local Secret. The default `tool` scenario mounts a tiny
ConfigMap, exposes only `read_knowledge`, and requires exactly one call before
an exact final response. The `text` scenario preserves the ordinary one-turn
transport check.

The tool scenario permits at most two provider turns, one tool call, 256 bytes
per tool result and in total, 2,048 cumulative measured tokens, and 90 seconds
of wallclock time. A normal dispatch should stay below roughly 2,000 provider
tokens, but this is not a hard billing ceiling: usage is checked only after a
response, provider token accounting varies, and the repeated tool context can
cross the configured budget. Before approval, select a small non-reasoning
model, verify its current pricing and the endpoint, and do not use this
workflow for untrusted code. Failure artifacts omit workload logs,
Agent/AgentRun descriptions, and cluster event summaries that can contain
runtime-specific values. Remaining controller and cluster diagnostics are
retained briefly; maintainers should still review them before sharing. The
model and endpoint inputs are visible workflow metadata. The acceptance script
rejects endpoint userinfo, queries, fragments, and whitespace so credentials
cannot be embedded there.

## Dependency inventory

The reference binary uses only the Go standard library and in-repository
Kontext packages. The bundled reporter additionally uses `golang.org/x/sys`
for Linux process supervision. The production image contains:

- `gcr.io/distroless/static:nonroot`
- BusyBox `sh` and applets copied into the final image for the allowlisted
  shell tool
- `/kontext-reporter`
- `/kontext-reference`

It contains no package manager, agent framework, retrieval system, or
persistent storage.

## Explicit limits

- Provider turns and tool calls continue only within configured limits.
- Conversation state exists only in memory for that run.
- Tool results are bounded and full shell output remains in container logs.
- No retries, planning, memory, retrieval, subagents, background work, or
  provider-specific model aliases.
