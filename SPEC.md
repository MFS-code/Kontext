# Kontext API Spec (DRAFT — v1alpha1)

> Status: draft for review. This contract keeps Kontext a **general** agent runtime. Features that require application-specific vocabulary or behavior belong in the consumer's runtime image, not the control plane.

Kontext exposes two custom resources and one runtime-image contract.

- `Agent` — the reusable **definition / desired state** of an agent.
- `AgentRun` — one bounded **execution** of an agent. Owns exactly one Pod.
- **Runtime image contract** — how any container becomes a Kontext agent.

API group/version: `kontext.dev/v1alpha1` (alpha on purpose — the shape is allowed to evolve).

---

## Mental model (map to core Kubernetes)


| Kontext                  | Core Kubernetes analogue | Behavior                                                                               |
| ------------------------ | ------------------------ | -------------------------------------------------------------------------------------- |
| `Agent` mode `Service`   | `Deployment`             | Always-on. Controller keeps one live `AgentRun`; re-casts on exit/failure.             |
| `Agent` mode `Task`      | reusable template        | Does not run on its own. Each invocation mints an `AgentRun`.                          |
| `Agent` mode `Scheduled` | `CronJob`                | Mints an `AgentRun` on a cron schedule. *(Reserved; build later.)*                     |
| `AgentRun`               | `Pod` / `Job`            | The single execution unit. Owns one Pod. Holds `status.result`, usage, immutable spec. |


`AgentRun` is the one execution engine every mode reuses. The `Agent` controller manages `AgentRun`s the way `Deployment`/`CronJob` manage their children.

---

## `Agent`

The reusable definition. Cluster-namespaced. Has a status subresource.

### `spec`


| Field                | Type                                  | Req | Notes                                                          |
| -------------------- | ------------------------------------- | --- | -------------------------------------------------------------- |
| `mode`               | enum `Task` | `Service` | `Scheduled` | yes | Discriminator. `Scheduled` reserved.                           |
| `runtime.image`      | string                                | yes | Container image implementing the runtime contract.             |
| `runtime.command`    | []string                              | no  | Override entrypoint.                                           |
| `runtime.args`       | []string                              | no  |                                                                |
| `runtime.result`     | object                                | no  | Optional stdout result capture policy.                         |
| `runtime.securityContext` | restricted security context     | no  | Portable non-root, capability-drop, filesystem, and seccomp settings. |
| `goal`               | string                                | no* | Default/persistent goal. Required for `Service`/`Scheduled`.   |
| `goalTemplate`       | string                                | no  | Parameterized goal for templated runs (`Task`).                |
| `provider`           | string                                | no  | Default `anthropic`.                                           |
| `model`              | string                                | yes |                                                                |
| `tools`              | []string                              | no  | Declared tool allowlist (semantics live in the runtime image). |
| `budget.tokens`      | int ≥ 1                               | no  |                                                                |
| `budget.wallclock`   | duration string                       | no  | e.g. `5m`. Enforced by controller.                             |
| `budget.dollars`     | number ≥ 0                            | no  |                                                                |
| `secretRef.name`     | string                                | no  | Provider credentials Secret.                                   |
| `knowledgeConfigMapRef.name` | string                       | no  | Static ConfigMap context mounted read-only at `/kontext/knowledge`. |
| `serviceAccountName` | string                                | no  | Per-agent identity.                                            |
| `env`                | []EnvVar                              | no  | Extra literal or Secret-backed env passed to the Pod.          |
| `schedule`           | string (cron)                         | no  | Only for `Scheduled`.                                          |
| `backoff`            | object                                | no  | Re-cast backoff policy for `Service`. Controller-defaulted.    |


 required depending on `mode`.

### `status`


| Field                | Type        | Notes                                |
| -------------------- | ----------- | ------------------------------------ |
| `conditions`         | []Condition | `Ready`, `Progressing`.              |
| `currentRunName`     | string      | `Service`: the live run.             |
| `lastRunName`        | string      | `Task`/`Scheduled`: most recent run. |
| `runsCreated`        | int         | Counter.                             |
| `restarts`           | int         | `Service`: re-cast count.            |
| `observedGeneration` | int         |                                      |


---

## `AgentRun`

One bounded execution. Maps to exactly one Pod. **Spec is immutable after creation** (snapshot semantics) so a run is self-contained and auditable.

### `spec`


| Field                | Type     | Req | Notes                                            |
| -------------------- | -------- | --- | ------------------------------------------------ |
| `agentRef.name`      | string   | no  | Owning `Agent`. Omitted = standalone ad-hoc run. |
| `goal`               | string   | yes | Concrete, fully-resolved goal.                   |
| `provider`           | string   | no  | Resolved from Agent at creation.                 |
| `model`              | string   | yes |                                                  |
| `tools`              | []string | no  |                                                  |
| `budget`             | object   | no  | Resolved snapshot.                               |
| `secretRef.name`     | string   | no  |                                                  |
| `knowledgeConfigMapRef.name` | string | no | Static ConfigMap context mounted read-only at `/kontext/knowledge`. |
| `serviceAccountName` | string   | no  |                                                  |
| `env`                | []EnvVar | no  | Resolved literal or Secret-backed env snapshot.  |
| `runtime.image`      | string   | yes | Resolved from Agent.                             |
| `runtime.command`    | []string | no  | Required when stdout capture is configured.      |
| `runtime.args`       | []string | no  | Appended to the declared command.                |
| `runtime.result`     | object   | no  | Optional stdout result capture policy.           |
| `runtime.securityContext` | restricted security context | no | Portable non-root, capability-drop, filesystem, and seccomp settings. |


When created from an `Agent`, the controller snapshots/resolves these fields so the run does not drift if the `Agent` changes later.

### `status`


| Field                | Type                                                           | Notes                                                       |
| -------------------- | -------------------------------------------------------------- | ----------------------------------------------------------- |
| `phase`              | enum `Pending`|`Running`|`Succeeded`|`Failed`|`BudgetExceeded` |                                                             |
| `podName`            | string                                                         |                                                             |
| `output.mediaType`   | string                                                         | Media type for the structured terminal output.              |
| `output.value`       | arbitrary JSON                                                 | Authoritative structured output.                            |
| `result`             | string                                                         | Backward-compatible deterministic projection of `output`.   |
| `usage.tokens`       | int                                                            | Total tokens when measured.                                 |
| `usage.inputTokens`  | int                                                            | Input tokens when measured.                                 |
| `usage.outputTokens` | int                                                            | Output tokens when measured.                                |
| `usage.dollars`      | number                                                         | Authoritative reported cost when measured.                  |
| `startTime`          | time                                                           |                                                             |
| `completionTime`     | time                                                           |                                                             |
| `message`            | string                                                         | Human-readable status/error.                                |
| `conditions`         | []Condition                                                    |                                                             |

Usage fields are optional independently. A missing field means the runtime or
provider did not measure it; a present zero means it measured zero.


### Ownership

`AgentRun` created from an `Agent` carries an owner reference to it → standard GC cascade. Standalone `AgentRun`s are allowed for ad-hoc execution, demos, and direct task dispatch.

---

## Runtime image contract

Any container can be a Kontext agent if it follows this. Kontext never inspects what the agent *does* — only this I/O boundary.

### Inputs

The controller injects, on the Pod:

- Env vars: `KONTEXT_GOAL`, `KONTEXT_MODEL`, `KONTEXT_PROVIDER`, `KONTEXT_TOOLS` (comma-separated), `KONTEXT_BUDGET_TOKENS`, `KONTEXT_BUDGET_WALLCLOCK`, `KONTEXT_BUDGET_DOLLARS`, `KONTEXT_AGENT_NAME`, `KONTEXT_RUN_NAME`.
- Provider credentials mounted from `secretRef` as env (e.g. `ANTHROPIC_API_KEY`).
- Optional static ConfigMap context from `knowledgeConfigMapRef`, mounted
  read-only at `/kontext/knowledge`. This is not RAG: the control plane does no
  ingestion, embedding, ranking, or retrieval.
- Generic `spec.env` entries. Each entry selects exactly one literal `value` or
  `valueFrom.secretKeyRef{name,key}`. Controller-managed variables cannot be
  overridden through either form. Secret values remain Kubernetes Secret data;
  they are not copied into Agent/AgentRun fields or controller logs.

### Output — logs and execution events

- Write operational progress to **stdout** and diagnostics to **stderr**,
  line-buffered. Both remain ordinary container logs observable with
  `kubectl logs`.
- Runtimes that expose detailed execution events write one JSON object per line.
  Event objects use `apiVersion: kontext.dev/event/v1alpha1`, an RFC3339
  `timestamp`, a `type` (`lifecycle`, `output`, `usage`, `tool`, or `error`),
  and type-specific `data`. Events may include tool calls, bounded tool output,
  timing, provider usage, errors, and final responses.
- Do not emit private chain-of-thought. Operational summaries and explicit
  model/tool outputs are sufficient.

### Output — result

The terminal envelope is optional at the control-plane boundary. An unchanged
plain image that exits `0` without writing `/dev/termination-log` succeeds with
absent `status.output` and an empty `status.result`. Native runtimes and images
using injected stdout capture write richer structured output, usage, timing,
and execution metadata.

On completion, a runtime that provides a structured result writes a compact
versioned envelope to `/dev/termination-log` (and may also write
`/kontext/result.json`):

```json
{
  "apiVersion": "kontext.dev/result/v1alpha1",
  "outcome": "Succeeded",
  "output": {
    "mediaType": "application/json",
    "value": { "answer": "final output" }
  },
  "usage": {
    "inputTokens": 1000,
    "outputTokens": 234,
    "totalTokens": 1234,
    "reasoningTokens": 180
  },
  "timing": {
    "durationMillis": 1750
  },
  "execution": {
    "provider": "anthropic",
    "model": "provider-defined-model-id",
    "requestId": "request-id"
  }
}
```

The envelope fields are:

- `apiVersion` — exactly `kontext.dev/result/v1alpha1`.
- `outcome` — `Succeeded` or `Failed`. A failed outcome includes
  `error.message` and may include a stable `error.code` and `error.retryable`.
- `output` — an optional media type plus any valid JSON value.
- `usage` — optional typed metrics. `reasoningTokens` records a
  provider-reported reasoning-token breakdown of output/completion usage.
  Missing values are never inferred as zero, including when the provider does
  not expose that breakdown; a present zero means the provider measured zero.
  Visible response text can therefore tokenize to fewer tokens than
  `outputTokens`: provider completion totals may also include hidden reasoning
  or other provider-defined completion accounting. The count does not expose
  reasoning content.
- `timing` — optional start/completion timestamps and measured durations.
- `execution` — optional non-secret provider, model, request, turn, and tool
  metadata.
- `artifacts` — optional references to data stored outside Kubernetes status.
- `extensions` — optional provider/runtime-specific JSON under namespaced keys
  such as `anthropic.com/request`.
- `truncation` — explicit metadata added when fields were removed to fit the
  Kubernetes termination-message limit.

The termination message is a terminal summary, not a transcript. Producers
must compact it below 4096 bytes while preserving valid JSON and setting
`truncation`. Full execution events remain in the JSONL log stream.

The controller projects the stable aggregate usage fields into
`AgentRun.status.usage`. Optional usage breakdowns such as `reasoningTokens`
remain in the versioned result envelope and usage events, avoiding
provider-detail growth in Kubernetes status.

`status.output` preserves `output`. The compatibility field `status.result` is
projected deterministically: JSON strings with a `text/*` media type become
their unquoted value; every other JSON value becomes compact JSON text; absent
output becomes an empty string.

The controller continues to accept the legacy payload during the v1alpha1
transition:

```json
{ "result": "<final output>", "tokensUsed": 1234, "dollarsUsed": 0.0 }
```

Legacy text becomes `text/plain` structured output. Legacy usage fields are
recorded only when they are present in the payload. For compatibility, the
process exit code remains authoritative for legacy payloads; a legacy `error`
field is retained as diagnostic text but does not turn exit `0` into failure.

Exit `0` with no termination payload or with a successful envelope means
success. A non-zero process exit always fails the run. A failed envelope also
fails the run even if the process exits zero. If a JSON-looking termination
payload is present, malformed or partially written JSON remains an actionable
failure rather than silently becoming a successful plain-text result.

### Optional result reporter

The maintained `runtimes/reporter` executable can supervise an explicit child
command and produce the versioned envelope without requiring the child to write
the termination log itself. It preserves child stdout, stderr, signals, and
process exit status.

Reporter extraction supports:

- `last-line` — the last non-empty stdout line becomes `text/plain` output.
- `kontext-envelope` — the last stdout line prefixed with
  `KONTEXT_RESULT:` supplies a complete versioned envelope.

The reporter bounds only captured result data; streamed logs remain unbounded.
It compacts every emitted envelope to the termination-message limit. Reporter
injection and workload configuration are control-plane concerns defined
separately from this executable.

An existing Linux image opts into stdout capture with:

```yaml
runtime:
  image: example/agent:v1
  command: ["python", "-m", "agent"]
  result:
    source: Stdout
    format: LastLine
```

`runtime.command` is required because reporter injection replaces the image
entrypoint; Kubernetes cannot recover that entrypoint automatically.
`KontextEnvelope` is the other supported format. Without `runtime.result`,
Kontext leaves the image entrypoint and native termination behavior unchanged.

The operator installation selects the trusted reporter image. Kontext injects
its binary with an init container and mounts it read-only into the workload
container; workloads cannot select a different reporter image. The controller
does not read Pod logs and needs no `pods/log` permission. `LastLine` is
heuristic, cannot infer usage, and is discouraged for long-running Service
agents. The trusted init container runs as UID 0 only while populating the
otherwise empty shared volume; it drops capabilities, disables privilege
escalation, and uses a read-only root filesystem. The workload container's own
user and security context remain unchanged.

### Runtime roles

`runtimes/echo` is the deterministic control-plane conformance oracle. During
the v1alpha1 transition it intentionally writes the legacy termination payload
documented above. `runtimes/reference` is the maintained model-backed runtime.
`runtimes/python-anthropic` is retained only as a legacy behavior oracle.
Arbitrary conforming images remain supported and need not use any of these
implementations.

### Maintained reference runtime

`runtimes/reference` is the optional maintained Go runtime. It owns a small
provider-neutral completion loop behind a normalized provider interface; it is
not an agent framework. The runtime image bundles the reporter as PID 1 and
uses the internal `KONTEXT_RESULT:` capture protocol so the reporter can write
the authoritative versioned envelope to the runtime container's termination
message. Raw logs remain an event and diagnostic stream.

The initial keyless `fake` provider is deterministic and exercises the same
configuration, conversation, event, result, cancellation, and failure paths
that real HTTP transports use. The maintained real transports are Anthropic
Messages (`anthropic`) and OpenAI-compatible Chat Completions (`openai` or
`openai-compatible`). They use direct non-streaming HTTP, accept an optional
exact endpoint or base-URL override, and normalize text, function tool calls,
stop reasons, measured usage, request IDs, and actionable transport errors.
The OpenAI-compatible boundary is the documented Chat Completions request,
response, bearer-auth, and function-tool-call shape; it does not imply support
for the Responses API, streaming, Azure URL construction, or every inference
protocol. `KONTEXT_MODEL` remains opaque and is never aliased or rewritten.

Credentials come from the `AgentRun` Secret policy as `ANTHROPIC_API_KEY` or
`OPENAI_API_KEY`. Authenticated acceptance is manual and environment-protected;
pull-request CI remains deterministic, keyless, and network-independent.

The controller remains authoritative for wallclock enforcement. The runtime
parses `KONTEXT_BUDGET_WALLCLOCK` but does not start a competing timer; it
reacts to cancellation when the reporter forwards controller signals.
Omission means no deadline, and the runtime does not invent a five-minute
default.

The maintained runtime exposes only tools named in `KONTEXT_TOOLS`. It owns a
bounded provider-neutral loop that returns normalized tool results until final
output, cancellation, failure, or a configured turn, token, tool-call, or
tool-output limit. Omitted or zero runtime limits are disabled. Built-in tools
are `read_knowledge`, `kubernetes_read`, and `shell`.

The reference runtime applies `KONTEXT_BUDGET_TOKENS` to cumulative measured
provider usage across every request in the run. Each tool follow-up resends the
conversation, including prior tool calls and bounded results, so providers may
count that context again. Provider-reported reasoning or completion usage also
counts even when it does not appear in the final text. The runtime sends the
remaining budget as a provider completion limit where supported, but checks
actual usage only after each response. A response can therefore exceed the run
budget and fail with `token_limit_exceeded`. Missing usage is not estimated.
Budgets need headroom because provider and model usage can vary between live
runs.

`read_knowledge` is confined to `/kontext/knowledge`. `kubernetes_read` has a
fixed current-namespace get/list allowlist and never reads Secrets. `shell`
uses an explicit working directory, filters its direct child environment,
streams logs, bounds captured output, and cleans up its process group on
cancellation. These runtime checks prevent accidental exposure; Kubernetes
RBAC, workload security context, mounted data, and container isolation remain
the security boundaries. In particular, `shell` shares the runtime container;
environment filtering does not isolate its filesystem or process view. Tool
events omit result content by default; the maintained runtime requires an
explicit opt-in before placing bounded tool content in the event stream.

Configured per-result and cumulative tool-output limits bound content returned
to the model. Omission or zero disables those configured limits, but built-in
tools still enforce a fixed 8 MiB capture safety ceiling per call. Tool errors
become structured results that the model may recover from; they do not fail the
run by themselves. Only a successful terminal provider response without tool
calls becomes final output. Cancellation, execution failures, or reaching a
configured limit before final output fail the run.

The reference runtime emits versioned JSONL lifecycle, usage, tool, output, and
error events to stdout. It retains conversation state only in memory for one
run and does not provide retries, planning, persistent memory, retrieval,
subagents, or background orchestration.

Configured MCP servers are a maintained-runtime concern, not control-plane
vocabulary. The reference runtime uses the official MCP Go SDK v1.6.1, which
requires Go 1.25 or newer; this repository declares Go 1.26.5. The SDK
negotiates protocol `2025-11-25` and accepts `2025-06-18`, `2025-03-26`, and
`2024-11-05`. Discovered MCP tools join the same immutable allowlisted registry
and use the same turn, call-count, output, cancellation, event, and cleanup
controls as built-ins.

The maintained Playwright MCP example is a separate restricted
Deployment/Service reached over HTTP `/mcp`, never an AgentRun sidecar.
Playwright MCP 0.0.78 is pinned to
`mcr.microsoft.com/playwright/mcp@sha256:3d871c22ea2d4cca0966e2cfb1860e1cb03eb7353725a3d6cffd133296fb04eb`.
The browser server has its own keyless identity, ephemeral profile and writable
storage, finite resources, and NetworkPolicy. It does not add MCP or browser
fields to either CRD. The example disables Chromium's sandbox because the
pinned image runs in a restricted container; Playwright MCP is not itself a
security boundary.

### Mode expectations for the image

- **Task / Scheduled run image:** does its work, writes result, exits. Pod `restartPolicy: Never`.
- **Service run image:** expected to be long-running (loop / serve / watch). When it exits for any reason, the `Agent` (Service) controller mints a fresh `AgentRun` — this is "instantly re-cast on failure".

---

## Anti-overfit firewall (read before adding a field)

Kontext's vocabulary is fixed: `Agent`, `AgentRun`, runtime image, mode, budget, secret, tools, status, result. It must not gain domain-specific fields or workflow semantics for any one consumer. Consumers encode their semantics inside runtime images and orchestrate by creating generic `Agent`/`AgentRun` objects.

---

## Open questions (resolve as real examples appear)

1. Does `Service` mode ever need >1 concurrent `AgentRun` (replicas), or is single-run-per-Agent enough for the MVP?
2. Where do tool *permissions* get enforced — admission/CEL on the CRD, or trusted to the runtime image at first?
3. Is `BudgetExceeded` a distinct phase or a `Failed` with a reason condition?
4. Do we need a `goalTemplate` + parameters object now, or defer templating until a concrete consumer needs it?
5. Standalone `AgentRun` UX: encouraged primitive, or always create via an `Agent`?

