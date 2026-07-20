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
  references it. Task controller status reconciliation remains reserved.
- **Scheduled** — reserved for cron-style minting. The schema is available,
  but the controller reports `UnsupportedMode` and does not schedule runs.

## AgentRun

`AgentRun` is one bounded execution. It owns exactly one Pod, snapshots its
immutable spec, and holds terminal status including `.status.result` and usage
fields when available.

You can create an `AgentRun` standalone, without an owning `Agent`. That path
is the fastest way to prove install health with the echo runtime.

### Task invocations

A Task Agent configures exactly one static `goal` or parameterized
`goalTemplate`. A sparse invocation contains only `agentRef` and optional
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

The pure resolution contract is present in this release, but sparse CREATE
admission is not registered yet. Until that follow-up lands, use fully resolved
or standalone runs for end-to-end execution.

Task runs are user-named and can execute concurrently. For Task status,
`lastRunName` means the newest retained owned run by creation time, while
`runsCreated` is the number of currently retained owned runs. It is not a
lifetime counter.

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
