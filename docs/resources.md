---
title: Resource model
description: How Agent and AgentRun map to familiar Kubernetes objects.
sidebarTitle: Resource model
---

# Core resource model

Kontext adds two custom resources under `kontext.dev/v1alpha1`. The split
mirrors how core Kubernetes separates definition from execution.

## Agent

`Agent` is the reusable definition. It declares mode, runtime image, goal,
model, budgets, and related fields.

- **Service** — always-on. The controller keeps one live child `AgentRun` and
  re-casts it with backoff after exit or failure. In-memory conversation is not
  restored across recasts.
- **Task** — reusable one-shot template. Creating the Agent does not execute
  it. A user explicitly triggers work by creating a named `AgentRun` that
  references it. Admission resolves the immutable execution snapshot before
  storage, and the controller projects retained child status.
- **Scheduled** — cron-style one-shot execution. The controller evaluates a
  standard five-field expression in the configured IANA time zone and mints at
  most the latest eligible slot. `Forbid` is the default overlap policy;
  `Allow`, suspension, starting deadlines, and bounded success/failure history
  are supported.

## AgentRun

`AgentRun` is one bounded execution. It owns exactly one Pod, snapshots its
immutable spec, and holds terminal status including `.status.result` and usage
fields when available.

You can create an `AgentRun` standalone, without an owning `Agent`. That path
is the fastest way to prove install health with the echo runtime.

Scheduled child names are derived from their cron slot, so retries and leader
changes converge on the same object. Schedule edits and resume operations
anchor at the current time and wait for a future slot instead of backfilling.
`status.lastScheduleTime` and `status.nextScheduleTime` expose scheduler
progress; `currentRunName`, `restarts`, and backoff apply only to Service mode.
`status.lastRunName` points to the newest retained scheduled child and is
cleared when history limits prune every child. `lastScheduleTime` remains the
historical latest observed slot even after that child is pruned.
`status.runsCreated` is a monotonic Scheduled creation sequence recovered from
retained child metadata. Pruning or manually deleting children never decreases
it.

### Task invocation requests

A Task Agent configures exactly one static `goal` or parameterized
`goalTemplate`. Its sparse request shape contains only `agentRef` and optional
string `parameters`:

```yaml
apiVersion: kontext.dev/v1alpha1
kind: AgentRun
metadata:
  name: summarize-frontend
spec:
  agentRef:
    name: summarizer
  parameters:
    area: frontend
```

Templates use strict `${area}` placeholders. `$${area}` produces the literal
text `${area}`. Missing and unused parameters are rejected, and static goals
accept no parameters. Runtime, provider, model, tools, budget, identity,
references, environment, and the concrete goal are locked to the Agent
template. Use a standalone run or another Agent definition when those fields
must differ.

Kubernetes runs mutating admission before final CRD validation. The webhook
resolves this request against the same-namespace Task Agent, and the API server
validates and stores only the complete snapshot. Persisted AgentRuns always
contain `goal`, `model`, and `runtime.image`. If admission cannot resolve a
matching sparse request, creation fails closed with an actionable error.

Task runs are user-named and can execute concurrently. For Task status,
`lastRunName` means the newest retained owned run by creation time, while
`runsCreated` is the number of currently retained owned runs. It is not a
lifetime counter. Deleting the newest run moves `lastRunName` to the next
newest retained run; deleting every run clears it and resets `runsCreated` to
zero. Lexically greater run name breaks equal creation-time ties.

## Status and logs

| Surface | Role |
|---|---|
| `kubectl logs` on the run Pod | Detailed operational stream from the container |
| `AgentRun.status` | Bounded terminal summary projected onto the CR |
| Termination message | ≤ 4096 bytes; envelopes may include truncation metadata |

Do not put transcripts or large artifacts into status or etcd. Store them
outside the cluster and keep bounded references in the result envelope.

## Budgets

- `budget.wallclock` is **enforced** by the controller. After expiry the run
  becomes `BudgetExceeded` and the Pod is deleted. Omit it for long-running
  Service agents that must not receive a task-style deadline.
- Token and dollar fields are **reported** by the runtime when measurements
  exist. They are not hard stops unless your runtime enforces them.

## Where to read more

- Full field contract: [SPEC](/SPEC)
- Failure boundaries: [Operations](/docs/operations)
- Image and result capture choices: [Runtimes](/docs/runtimes)
