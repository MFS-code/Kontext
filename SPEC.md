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
| `goal`               | string                                | no* | Default/persistent goal. Required for `Service`/`Scheduled`.   |
| `goalTemplate`       | string                                | no  | Parameterized goal for templated runs (`Task`).                |
| `provider`           | string                                | no  | Default `anthropic`.                                           |
| `model`              | string                                | yes |                                                                |
| `tools`              | []string                              | no  | Declared tool allowlist (semantics live in the runtime image). |
| `budget.tokens`      | int ≥ 1                               | no  |                                                                |
| `budget.wallclock`   | duration string                       | no  | e.g. `5m`. Enforced by controller.                             |
| `budget.dollars`     | number ≥ 0                            | no  |                                                                |
| `secretRef.name`     | string                                | no  | Provider credentials Secret.                                   |
| `serviceAccountName` | string                                | no  | Per-agent identity.                                            |
| `env`                | []EnvVar                              | no  | Extra env passthrough to the Pod.                              |
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
| `serviceAccountName` | string   | no  |                                                  |
| `runtime.image`      | string   | yes | Resolved from Agent.                             |
| `runtime.command`    | []string | no  | Required when stdout capture is configured.      |
| `runtime.args`       | []string | no  | Appended to the declared command.                |
| `runtime.result`     | object   | no  | Optional stdout result capture policy.           |


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
- Optionally a mounted `/kontext/input.json` with the same data (richer/structured payloads).
- Provider credentials mounted from `secretRef` as env (e.g. `ANTHROPIC_API_KEY`).

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

- On completion, write a compact versioned envelope to
  `/dev/termination-log` (and optionally `/kontext/result.json`):

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
    "totalTokens": 1234
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
- `usage` — optional typed metrics. Missing values are never inferred as zero.
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

Exit `0` plus a successful envelope means success. A non-zero process exit
always fails the run. A failed envelope also fails the run even if the process
exits zero. Malformed or partially written JSON is an actionable failure, not
a successful plain-text result.

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

### Maintained reference runtime

`runtimes/reference` is the optional maintained Go runtime. It owns a small
provider-neutral completion loop behind a normalized provider interface; it is
not an agent framework. The runtime image bundles the reporter as PID 1 and
emits its final versioned envelope through the `KONTEXT_RESULT:` stream
contract.

The initial keyless `fake` provider is deterministic and exercises the same
configuration, conversation, event, result, cancellation, and failure paths
that real HTTP transports use. `KONTEXT_MODEL` remains opaque and is never
aliased or rewritten.

Runtime wallclock cancellation is enabled only when
`KONTEXT_BUDGET_WALLCLOCK` is present. Omission means no runtime deadline; the
runtime does not invent a five-minute default. Declared tools are recorded in
lifecycle events but are not exposed to the provider or executed until the
bounded tool loop is implemented.

The reference runtime emits versioned JSONL lifecycle, usage, output, and error
events to stdout. It retains conversation state only in memory for one run and
does not provide retries, planning, memory, retrieval, subagents, or background
orchestration.

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

