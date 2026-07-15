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


When created from an `Agent`, the controller snapshots/resolves these fields so the run does not drift if the `Agent` changes later.

### `status`


| Field            | Type                                                           | Notes                        |
| ---------------- | -------------------------------------------------------------- | ---------------------------- |
| `phase`          | enum `Pending`|`Running`|`Succeeded`|`Failed`|`BudgetExceeded` |                              |
| `podName`        | string                                                         |                              |
| `result`         | string                                                         | Final answer/output.         |
| `usage.tokens`   | int                                                            |                              |
| `usage.dollars`  | number                                                         |                              |
| `startTime`      | time                                                           |                              |
| `completionTime` | time                                                           |                              |
| `message`        | string                                                         | Human-readable status/error. |
| `conditions`     | []Condition                                                    |                              |


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

### Output — thoughts

- Write reasoning/progress to **stdout**, line-buffered. This is the `kubectl logs -f` magic moment.

### Output — result

- On completion, write JSON to `/dev/termination-log` (and optionally `/kontext/result.json`):

```json
{ "result": "<final output>", "tokensUsed": 1234, "dollarsUsed": 0.0 }
```

- Exit `0` = success, non-zero = failure. The controller reconciles the termination message into `AgentRun.status`.

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

