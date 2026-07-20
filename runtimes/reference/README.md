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
stdout. Internally, it gives its final envelope to the bundled reporter through
the `KONTEXT_RESULT:` capture prefix. The reporter preserves process semantics,
compacts the envelope, and writes the authoritative Pod termination message.
Consumers use `AgentRun.status` or that termination message for terminal data;
raw logs remain events and diagnostics.

No private chain-of-thought is emitted.

## Configuration

| Environment variable | Required | Meaning |
|---|---|---|
| `KONTEXT_GOAL` | yes | Concrete task for this run |
| `KONTEXT_PROVIDER` | yes | `fake`, `anthropic`, `openai`, or `openai-compatible` |
| `KONTEXT_MODEL` | yes | Opaque provider model identifier |
| `KONTEXT_RUN_NAME` | no | Run metadata; defaults to `unknown-run` |
| `KONTEXT_AGENT_NAME` | no | Agent metadata; defaults to run name |
| `KONTEXT_TOOLS` | no | Comma-separated allowlist of built-in or discovered MCP tools |
| `KONTEXT_MCP_CONFIG` | no | Inline JSON MCP server configuration |
| `KONTEXT_MCP_CONFIG_FILE` | no | Path to a JSON MCP server configuration file |
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
| `KONTEXT_FAKE_SCENARIO` | no | `success`, `failure`, `malformed`, `delay`, `tool`, or test-only `tool_sequence` |
| `KONTEXT_FAKE_DELAY` | delay only | Positive Go duration such as `250ms` |
| `KONTEXT_FAKE_TOOL_NAME` | tool only | Deterministic fake-provider tool name |
| `KONTEXT_FAKE_TOOL_ARGUMENTS` | tool only | Deterministic fake-provider JSON arguments |
| `KONTEXT_FAKE_TOOL_SEQUENCE` | tool_sequence only | Test-only JSON array of deterministic `{name,arguments}` calls |

There is deliberately no hidden five-minute deadline. The Kubernetes
controller remains authoritative for `KONTEXT_BUDGET_WALLCLOCK`; the runtime
parses the value but does not start a competing timer. It reacts promptly when
the reporter forwards controller cancellation signals.

Model identifiers pass through unchanged.

`KONTEXT_MCP_CONFIG` and `KONTEXT_MCP_CONFIG_FILE` are mutually exclusive. If
both are set, startup fails instead of choosing one. If neither is set (or the
selected value/file is empty), the runtime starts with no MCP servers.

The MCP document has the form `{"servers":[...]}` and rejects unknown fields.
Every server needs a unique `name` and one transport:

- `stdio` requires an absolute `command`; `args`, literal `env`, `envFrom`,
  and a non-negative Go `timeout` are optional. `envFrom` maps child variable
  names to runtime variable names. The child receives only these explicit
  values; the runtime environment is not inherited wholesale.
- `http` requires an absolute HTTP(S) `endpoint` without userinfo, query, or
  fragment. Literal `headers`, `headersFromEnv`, a non-negative Go `timeout`,
  and non-negative `maxRetries` are optional. A configured `maxRetries: 0`
  disables SDK reconnect attempts; omission keeps the SDK default. Configured
  headers are injected only for that exact origin, and cross-origin redirects
  are rejected.

HTTP/MCP-owned and hop-by-hop headers cannot be configured through `headers`
or `headersFromEnv`, case-insensitively. This includes `Host`,
`Content-Length`, `Transfer-Encoding`, `Connection`, `Upgrade`, `Accept`,
`Content-Type`, `Mcp-Session-Id`, `Mcp-Protocol-Version`, `Mcp-Method`,
`Mcp-Name`, `Last-Event-ID`, `Keep-Alive`, `TE`, `Trailer`, and proxy
authentication/connection headers.

Environment references are resolved before connecting. Missing references,
invalid names, malformed durations, transport-specific field mismatches,
header newlines, and endpoint credentials fail startup without logging
referenced values.

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
  are rejected or truncated. The mounted ConfigMap is static context for the
  run, not production RAG.
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
tool can actually access. Availability also does not guarantee model use;
versioned tool events are the evidence that a call occurred.

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

## MCP tools

The runtime uses `github.com/modelcontextprotocol/go-sdk` v1.6.1 for stdio and
Streamable HTTP MCP sessions. It initializes each configured server, follows
all discovery pages, canonicalizes object input schemas, combines the
discovered definitions with built-ins, rejects name collisions, and then
applies `KONTEXT_TOOLS`. Definitions are frozen for the run.

MCP calls use the same turn, call-count, per-result, cumulative-output, and
wallclock controls as built-ins. Structured content is compact JSON; otherwise
text blocks are joined in order. Image, audio, embedded resource, and resource
link fallback results currently return `mcp_unsupported_content`; valid
structured content takes precedence over fallback blocks. A fixed 8 MiB
capture ceiling applies before the engine's configured output limits.

HTTP protocol responses stream through a counting body under a 64 MiB wire
ceiling before SDK decoding. This preserves incremental Streamable HTTP/SSE
delivery while leaving room for an 8 MiB normalized result plus JSON/SSE
framing and worst-case JSON escaping. Declared `Content-Length` is rejected
early; chunked and streaming bodies fail with `mcp_http_wire_limit_exceeded`
when their count crosses the ceiling. Stdio uses the SDK's `IOTransport` with
a 64 MiB limit on each newline-delimited frame before SDK JSON decoding; an
oversized frame fails with `mcp_stdio_frame_limit_exceeded`. Each non-empty
physical line must itself be one complete JSON value; split or multiline JSON
fails with `mcp_stdio_invalid_frame`. Limits apply per line, not cumulatively
across independent messages. External error text returned to the model or
events is capped at 4 KiB, and values resolved through `headersFromEnv` or
stdio `envFrom` are redacted. Structured content is redacted recursively at
string-value level before JSON is re-encoded.

Discovery is bounded before `KONTEXT_TOOLS` is applied: at most 256 tools,
16 KiB per description, 256 KiB per schema, 280 KiB per complete definition,
and 4 MiB of definitions in total. Tool names must satisfy MCP's 1–128
character `[A-Za-z0-9_.-]+` constraint. The runtime retains each resolved
frozen schema and rejects non-object or schema-invalid provider arguments as
`mcp_invalid_arguments` without contacting or staling the server session.

A protocol or transport failure is returned to the model and marks that
session stale. The failed call is never repeated because it may have side
effects. Before a later call, the runtime creates a new session and verifies
that its complete tool names, descriptions, and schemas match the frozen
definitions.

SDK v1.6.1 negotiates MCP protocol `2025-11-25` by default and accepts
`2025-06-18`, `2025-03-26`, and `2024-11-05`. The SDK requires Go 1.25; this
repository declares Go 1.26.5. Its runtime dependency footprint adds
`google/jsonschema-go`, `segmentio/encoding`, `yosida95/uritemplate`,
`golang.org/x/oauth2`, and their small transitive dependencies. Kontext uses
the same JSON Schema package to reject malformed discovered schemas while
returning the original canonicalized keywords to providers.

Sessions are per run. Cleanup uses a fresh bounded context even after
cancellation. Stdio servers run in their own process group; shutdown escalates
from graceful close to TERM and KILL for the full group. A cleanup failure
fails an otherwise successful run with `tool_cleanup_failed`, but does not
replace an earlier run failure. Servers close concurrently under the shared
cleanup deadline, and HTTP DELETE requests have their own two-second ceiling.
Terminal output is emitted only after cleanup succeeds.

Assistant text from a turn that requests tools is conversation context, not
the run's final output. The runtime publishes output only from a terminal
provider response with no tool calls and a successful terminal stop reason. If
a configured limit is reached before that response, the run fails without
promoting earlier assistant text to final output.

Tool events include identity, timing, byte count, error code, and truncation
metadata. Tool content is omitted from events unless
`KONTEXT_EMIT_TOOL_OUTPUT=true` because event logs may leave the workload's
namespace or retention boundary.

The fake provider's `tool_sequence` scenario is acceptance-test support, not a
model simulation. It requests one configured call per turn, verifies that each
tool is exposed, gives calls unique deterministic IDs, and emits a terminal
response containing the bounded results in order. Invalid JSON, empty
sequences, missing names or arguments, non-object arguments, and unknown
fields fail provider configuration. The original single-call `tool` scenario
remains unchanged.

The turn, tool-call, per-result, and total-output limits are configurable run
limits. They are disabled when their environment variables are omitted or set
to `0`; examples use finite values. The result limits bound content returned to
the model. Separately, each built-in tool has a fixed 8 MiB capture safety
ceiling. Disabling a configured result limit does not remove that ceiling, and
the per-result setting cannot raise it. Shell stdout and stderr continue to
stream to container logs after model-facing capture reaches the ceiling.

## Playwright MCP deployment checkpoint

The maintained browser example in
`deploy/examples/v1alpha1/reference-playwright-mcp.yaml` runs Playwright MCP as
a separate Deployment and Service. It is not an `AgentRun` sidecar and does
not require browser fields in the Kontext CRDs. The reference runtime connects
to its standalone Streamable HTTP endpoint at `/mcp` on port `8931`.

The example pins Playwright MCP `0.0.78` to the official immutable multiarch
image:

```text
mcr.microsoft.com/playwright/mcp@sha256:3d871c22ea2d4cca0966e2cfb1860e1cb03eb7353725a3d6cffd133296fb04eb
```

The image is roughly 416 MB compressed and publishes amd64 and arm64 variants.
The vendor does not publish a fixed resource minimum. The example's tested
defaults are a `250m` CPU / `512Mi` memory request and a `1` CPU / `1Gi` memory
limit; these are operational starting points, not vendor guarantees.

The container overrides the entrypoint with `node /app/cli.js` and passes:

```text
--headless --browser chromium --no-sandbox --isolated --image-responses omit --port 8931 --host 0.0.0.0
```

Because the server is reached through a Kubernetes Service, the example also
adds `--allowed-hosts
playwright-mcp.kontext-network-policy-e2e.svc.cluster.local:8931`. Playwright
MCP otherwise returns HTTP 403 for that Host header; the example does not use
the unrestricted `*` value.

The pinned official image disables the Chromium sandbox in this restricted
container setup. Kubernetes isolation and NetworkPolicy therefore remain
security boundaries; Playwright MCP itself is not a security boundary. The
container still runs non-root under restricted Pod Security, drops all
capabilities, forbids privilege escalation, uses RuntimeDefault seccomp, and
has a read-only root filesystem. Explicit bounded `emptyDir` mounts provide
only `/tmp` and `/dev/shm`; `HOME`, `TMPDIR`, and XDG paths point into `/tmp`.

The Playwright ServiceAccount disables token automount and receives no model
provider credentials. Calico policy allows browser egress only to DNS and the
fixture Service, allows MCP ingress only from the named AgentRun Pods, and
allows those Pods to reach only DNS and Playwright. Cloud metadata
`169.254.169.254` and unrelated cluster destinations have no allow rule.
NetworkPolicy enforcement is tested only in the Calico-backed
`scripts/e2e-kind-network-policy.sh` job; kindnet does not enforce these
policies.

Playwright MCP `0.0.78` exposes the acceptance operations
`browser_navigate {url}`, `browser_snapshot {}`, `browser_type
{target,text,...}`, and `browser_click {target,...}`. In this version `target`
accepts a unique selector, so the deterministic fixture uses stable CSS
selectors (`#name` and `#submit`) instead of accessibility snapshot refs.
The fixture keeps its state marker before deterministic accessibility padding.
The primary browser run limits each result to 480 bytes and the cumulative
output to 2,048 bytes, asserts at least one truncated result, and still finds
`State: Kontext accepted` while tool event content remains omitted. The
wallclock case does not accept early cancellation as evidence: it observes the
second tool-use turn and at least one Chromium process before requiring
`BudgetExceeded`, runtime Pod deletion, and zero remaining Chromium processes.
The browser server sets `PLAYWRIGHT_MCP_PING_TIMEOUT_MS=30000`, above the
12-second controller budget, so its heartbeat cannot terminate the active
`browser_wait_for` first.

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

MCP authentication can use generic Secret-backed `spec.env`, then reference
that runtime variable from `headersFromEnv` or stdio `envFrom`:

```yaml
spec:
  env:
    - name: MCP_AUTH_TOKEN
      valueFrom:
        secretKeyRef:
          name: mcp-credentials
          key: token
```

The Secret value is resolved by Kubernetes directly into the runtime
container. It is not copied into the AgentRun object.

The focused Secret-auth integration test builds the runtime Pod environment
from a generic `spec.env.valueFrom.secretKeyRef`, verifies the resulting
variable name is the one referenced by `headersFromEnv`, simulates kubelet
resolution of that variable, and uses it against a real in-process
authenticated Streamable HTTP MCP server. It also verifies resolved values are
redacted from returned errors and captured stderr. This is an integration of
Pod construction, runtime configuration, MCP authentication, and redaction;
it does not create a Kubernetes Secret or run the authenticated fixture in
kind. The keyless Playwright kind scenario separately covers real Pods,
NetworkPolicy, and browser lifecycle without introducing credentials.

Example templates live in `deploy/examples/v1alpha1/`:

- `provider-secrets.example.yaml`
- `reference-anthropic-run.yaml`
- `reference-openai-compatible-run.yaml`
- `reference-fake-tool-run.yaml`
- `reference-tools-run.yaml`
- `reference-playwright-mcp.yaml` and `reference-playwright-*-run.yaml`

The `Provider acceptance` GitHub Actions workflow is dispatch-only and uses
the `provider-acceptance` environment. Repository maintainers must protect
that environment with required reviewers and store `ANTHROPIC_API_KEY` and
`OPENAI_API_KEY` as environment secrets. Pull-request CI never receives or
uses these credentials.

Each dispatch builds the maintained operator and reference runtime, loads them
into an ephemeral kind cluster, and injects only the selected provider key
through a namespace-local Secret. The default `tool` scenario mounts a tiny
ConfigMap, exposes only `read_knowledge`, and requires exactly one call before
an exact final response. Acceptance requires exactly one matching tool event
with `count: 1`, `isError: false`, and no error code. The `text` scenario
preserves the ordinary one-turn transport check.

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

The script writes a bounded `kontext.dev/eval/v1alpha1`
`ProviderAcceptanceRecord` on success and safe failure paths. It includes the
provider, opaque model, scenario, commit/run identity, phase, duration,
measured usage, turns, tool calls, and pass/fail only. It excludes API keys,
Secret values, raw logs, and model output. The workflow uploads this record on
success or failure with short retention.

For each release, dispatch the workflow for every maintained transport being
published, approve the protected environment, require a passing run, and
retain the `provider-acceptance-<run>-<attempt>` artifact with the release
evidence. Without those protected credentials, no local or keyless run counts
as authenticated acceptance. Immutable versioned image publication remains a
separate release step.

## Dependency inventory

The reference binary uses the official MCP Go SDK v1.6.1 plus the standard
library and in-repository Kontext packages. The bundled reporter additionally
uses `golang.org/x/sys` for Linux process supervision. The production image
contains:

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
- No automatic provider or tool-call retries, planning, memory, retrieval,
  subagents, background work, or provider-specific model aliases. MCP
  transport reconnection never repeats a failed tool call.
