---
title: Scheduled workload
description: Run one-shot AgentRuns from cron slots with deadlines, overlap policy, suspension, and retained history.
sidebarTitle: Scheduled workload
---

# Run a Scheduled workload

Scheduled mode creates one-shot `AgentRun` children from a standard five-field
cron expression. The controller derives each run name from its slot, so
retries and leader changes converge on the same object.

## Create a Scheduled Agent

Save this as `echo-scheduled.yaml`:

```yaml
apiVersion: kontext.dev/v1alpha1
kind: Agent
metadata:
  name: echo-scheduled
spec:
  mode: Scheduled
  goal: Emit one deterministic echo result for this scheduled slot.
  provider: echo
  model: echo-model
  runtime:
    image: ghcr.io/mfs-code/kontext-echo:v0.1.0-alpha.2
    command: ["/entrypoint.sh"]
  schedule:
    expression: "* * * * *"
    timeZone: Etc/UTC
    concurrencyPolicy: Forbid
    startingDeadlineSeconds: 60
    suspend: false
    successfulRunsHistoryLimit: 3
    failedRunsHistoryLimit: 1
```

Apply it and watch the first future minute slot:

```bash
kubectl apply -f echo-scheduled.yaml
kubectl get agent echo-scheduled -w
kubectl get agentruns -l kontext.dev/agent=echo-scheduled -w
```

Creating or changing the Agent anchors the schedule at the current time. It
waits for the next future slot instead of immediately running or backfilling.
After the controller creates a run, inspect it with:

```bash
RUN_NAME="$(
  kubectl get agent echo-scheduled \
    -o jsonpath='{.status.lastRunName}'
)"
kubectl get agentrun "${RUN_NAME}" -w
kubectl logs -f "run-${RUN_NAME}"
kubectl get agentrun "${RUN_NAME}" \
  -o jsonpath='{.status.result}{"\n"}'
```

## Scheduling behavior

The expression has minute, hour, day-of-month, month, and day-of-week fields.
Seconds and descriptors are rejected. `timeZone` accepts an IANA name and
defaults to `Etc/UTC`.

`Forbid`, the default concurrency policy, skips a due slot while any owned run
is Pending or Running. `Allow` permits overlap. Skipped work is not queued.
After controller downtime, Kontext considers only the latest due slot and
creates it only when it remains inside `startingDeadlineSeconds`.

Set `schedule.suspend: true` to stop new runs. Existing runs continue.
Resuming waits for the next future slot.

## Status and history

Use these fields to inspect scheduler progress:

- `lastScheduleTime` records the latest slot that minted a run and remains
  after that child is pruned.
- `nextScheduleTime` is the next slot the controller will evaluate.
- `lastRunName` points to the newest retained owned child and clears when no
  child remains.
- `runsCreated` is a monotonic creation sequence. Pruning or deleting
  Scheduled runs does not reduce it.

Successful and failed history limits apply separately. `BudgetExceeded` counts
as failed history, and active runs are never pruned.

Common condition reasons are `ScheduleInitialized`, `ScheduleUpdated`,
`WaitingForSchedule`, `RunCreated`, `Suspended`, `MissedDeadline`,
`OverlapSkipped`, and `InvalidSchedule`.

## Failure and cleanup

If no run appears, inspect the Agent before the workload Pods:

```bash
kubectl get agent echo-scheduled -o yaml
kubectl describe agent echo-scheduled
kubectl get events --sort-by=.lastTimestamp
```

`InvalidSchedule` means the expression, time zone, or policy is invalid.
`MissedDeadline` and `OverlapSkipped` are deliberate skips, not queued work.

Delete the Agent to stop scheduling and garbage-collect its retained runs and
Pods:

```bash
kubectl delete agent echo-scheduled --ignore-not-found=true
```

## Related

- [Task workload](/docs/task-workload)
- [Resource model](/docs/resources)
- [Operations](/docs/operations)
- [API specification](/SPEC)
