---
title: Service workload
description: Deploy a persistent Service-mode Agent and watch the controller re-cast it after Pod deletion.
sidebarTitle: Service workload
---

# First Service workload

Service mode keeps an agent available the way a Deployment keeps a Pod
available. The controller owns one live `AgentRun` for the `Agent` and mints a
replacement when that run exits.

> **Note:** The example uses the published `v0.1.0-alpha.2` echo image. Pin a
> release tag or digest in your own manifests.

## Apply a Service Agent

```yaml
apiVersion: kontext.dev/v1alpha1
kind: Agent
metadata:
  name: echo-service
spec:
  mode: Service
  goal: Remain available and emit heartbeats for service-mode tasks.
  provider: echo
  model: echo-model
  runtime:
    image: ghcr.io/mfs-code/kontext-echo:v0.1.0-alpha.2
    command: ["/entrypoint.sh"]
  env:
    - name: KONTEXT_MODE
      value: service
  # Long-running Service: omit budget.wallclock so the controller does not
  # impose a task-style deadline.
  backoff:
    initialSeconds: 3
    maxSeconds: 15
```

```bash
kubectl apply -f echo-service.yaml
kubectl get agent echo-service -w
```

Find the live Pod and follow its logs:

```bash
kubectl get pods -l kontext.dev/agent=echo-service
kubectl logs -f -l kontext.dev/agent=echo-service
```

## Prove re-cast

Delete the Pod and watch the controller mint a replacement run:

```bash
kubectl delete pod -l kontext.dev/agent=echo-service
kubectl get agentruns -w
```

Service re-cast starts a **fresh** run. It does not restore in-memory
conversation or local process state from the previous Pod.

## Clean up

```bash
kubectl delete agent echo-service --ignore-not-found=true
```

## Related

- [Quickstart](/docs/quickstart) for a one-shot `AgentRun`
- [Operations](/docs/operations) for identity, secrets, and interruption
- [SPEC](/SPEC) for the full `Agent` schema
