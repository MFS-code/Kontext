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
- **Task** — reserved reusable template. The schema is available, but the
  controller reports `UnsupportedMode`. Create a standalone `AgentRun` for
  one-shot work.
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
