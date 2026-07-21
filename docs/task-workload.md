---
title: Task workload
description: Create a reusable Task Agent and invoke it through a sparse, user-named AgentRun.
sidebarTitle: Task workload
---

# Run a Task workload

Task mode separates a reusable definition from each execution. Applying a Task
`Agent` creates no Pod. Each sparse `AgentRun` invocation passes through
Kontext admission, becomes a complete immutable snapshot, and starts one Pod.

> **Note**
> Task invocation requires the `MutatingWebhookConfiguration` installed with
> Kontext. The controller manages the webhook certificate and CA bundle.

## Create the Task Agent

Save this as `echo-task.yaml`:

```yaml
apiVersion: kontext.dev/v1alpha1
kind: Agent
metadata:
  name: echo-task
spec:
  mode: Task
  goalTemplate: "Summarize ${subject}; preserve the literal $${subject}."
  provider: echo
  model: echo-model
  runtime:
    image: ghcr.io/mfs-code/kontext-echo:v0.1.0-alpha.2
  budget:
    wallclock: 2m
```

Apply it and wait for the template to become ready:

```bash
kubectl apply -f echo-task.yaml
kubectl wait agent/echo-task \
  --for=condition=Ready --timeout=60s
kubectl get agentrun -l kontext.dev/agent=echo-task
```

The last command returns no runs. A Task Agent is a definition, not an
execution.

## Invoke the Task

Save this as `echo-task-run.yaml`:

```yaml
apiVersion: kontext.dev/v1alpha1
kind: AgentRun
metadata:
  name: echo-task-docs
spec:
  agentRef:
    name: echo-task
  parameters:
    subject: Task admission
```

Create the invocation and inspect the stored snapshot:

```bash
kubectl create -f echo-task-run.yaml
kubectl get agentrun echo-task-docs -o yaml
kubectl get agentrun echo-task-docs -w
kubectl logs -f run-echo-task-docs
kubectl get agentrun echo-task-docs \
  -o jsonpath='{.status.result}{"\n"}'
```

Admission renders `goalTemplate`, copies the Agent's execution fields, records
the supplied parameters, and adds an owner reference. The stored
`AgentRun.spec` is complete and immutable. Create another user-named
`AgentRun` to invoke the template again; concurrent runs are allowed.

## Status and retained runs

The Task Agent reports `Ready=True` with reason `TemplateReady` when the
template can accept invocations. `Progressing=False` with reason `Idle` means
that Task work starts only when a caller creates an `AgentRun`.

`status.lastRunName` points to the newest retained owned run by creation time.
`status.runsCreated` is the number of retained owned Task runs, not a lifetime
counter. Deleting a run can decrease the count and move or clear
`lastRunName`.

## Admission failures

Rejected invocations include one stable resolution class in the API error:

- `MissingAgent`: the referenced Agent does not exist in the namespace.
- `WrongMode`: the reference names a Service or Scheduled Agent.
- `InvalidTemplate`: the Task Agent has invalid template syntax.
- `MissingParameters`: one or more placeholders have no value.
- `UnusedParameters`: the request supplies values the goal does not consume.
- `ConflictingFields`: the sparse request tries to override an execution field.

Matching sparse requests fail closed if the webhook is unavailable or its TLS
trust is unhealthy. Complete standalone `AgentRun` requests do not match the
webhook.

## Clean up

```bash
kubectl delete agent echo-task --ignore-not-found=true
```

The Agent owns its resolved runs, so Kubernetes garbage collection removes
`echo-task-docs` and its Pod. Delete a run directly when you want to discard
only that invocation.

## Related

- [Scheduled workload](/docs/scheduled-workload)
- [Resource model](/docs/resources)
- [Operations](/docs/operations)
- [API specification](/SPEC)
