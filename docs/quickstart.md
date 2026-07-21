---
title: Quickstart
description: Install Kontext from a published release and run a keyless echo AgentRun without cloning the repository.
sidebarTitle: Quickstart
---

# Quickstart (no clone)

Install a tagged release on an existing Kubernetes cluster, then run a keyless
echo `AgentRun`. You need kubectl and permission to create CRDs, cluster-scoped
RBAC, a Namespace, a Deployment, a Service, a NetworkPolicy, and a
MutatingWebhookConfiguration. You do not need Docker, kind, or a repository
clone.

## Install

```bash
VERSION=v0.1.0-alpha.1
kubectl apply -f \
  "https://github.com/MFS-code/Kontext/releases/download/${VERSION}/install.yaml"
kubectl rollout status deployment/controller-manager \
  --namespace kontext-system \
  --timeout=120s
```

The release manifest pins the operator and trusted reporter images by digest.
It installs the admission registration and webhook Service. The controller
creates and rotates the namespaced TLS Secret and repairs the registration's
CA bundle. See [Releases](/docs/releases) for upgrade and uninstall procedures.

## Run an echo AgentRun

Create a file named `run.yaml`:

```yaml
apiVersion: kontext.dev/v1alpha1
kind: AgentRun
metadata:
  name: review
spec:
  goal: "Summarize the failure modes in this release"
  provider: echo
  model: echo-model
  runtime:
    image: ghcr.io/mfs-code/kontext-echo:v0.1.0-alpha.1
  budget:
    wallclock: 5m
```

Apply it and watch the workload:

```bash
kubectl apply -f run.yaml
kubectl get agentrun review -w
kubectl logs -f run-review
kubectl get agentrun review -o jsonpath='{.status.result}{"\n"}'
```

`kubectl logs` is the operational stream from the runtime container. It is not
a special "reasoning" channel. `.status.result` is the bounded terminal
summary projected onto the custom resource.

## Reuse a Task template

Create a Task Agent and a sparse invocation:

```yaml
apiVersion: kontext.dev/v1alpha1
kind: Agent
metadata:
  name: echo-task
spec:
  mode: Task
  goalTemplate: "Summarize ${subject}"
  provider: echo
  model: echo-model
  runtime:
    image: ghcr.io/mfs-code/kontext-echo:v0.1.0-alpha.1
---
apiVersion: kontext.dev/v1alpha1
kind: AgentRun
metadata:
  name: echo-task-release
spec:
  agentRef:
    name: echo-task
  parameters:
    subject: this release
```

Creating the Agent alone starts no Pod. Creating the sparse `AgentRun` causes
admission to render the goal and copy the Agent's execution fields into one
immutable, owned snapshot. Inspect the stored form with
`kubectl get agentrun echo-task-release -o yaml`.

## Run on a schedule

Save this Scheduled Agent as `scheduled.yaml`. It runs on the next future
minute:

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
    image: ghcr.io/mfs-code/kontext-echo:v0.1.0-alpha.1
    command: ["/entrypoint.sh"]
  schedule:
    expression: "* * * * *"
    timeZone: Etc/UTC
    concurrencyPolicy: Forbid
    startingDeadlineSeconds: 60
```

```bash
kubectl apply -f scheduled.yaml
kubectl get agent echo-scheduled -w
kubectl get agentruns -l kontext.dev/agent=echo-scheduled
```

Creating the Agent anchors scheduling at the current time, so the first run
arrives on a future cron slot. The [Scheduled workload
guide](/docs/scheduled-workload) covers suspension, history limits, overlap,
and scheduler status.

## Clean up this demo

```bash
kubectl delete agentrun review --ignore-not-found=true
kubectl delete agentrun echo-task-release --ignore-not-found=true
kubectl delete agent echo-task --ignore-not-found=true
kubectl delete agent echo-scheduled --ignore-not-found=true
```

Uninstalling the control plane is covered in [Releases](/docs/releases).
Deleting the CRDs also deletes every `Agent` and `AgentRun`.

## Next

- [Core resource model](/docs/resources)
- [Task workload](/docs/task-workload)
- [Scheduled workload](/docs/scheduled-workload)
- [First Service workload](/docs/service-workload)
- [Runtime choices](/docs/runtimes)
