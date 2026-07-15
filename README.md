# Kontext

Kubernetes-native control plane for running, governing, and observing AI agents as
production workloads.

**API:** `kontext.dev/v1alpha1` — `Agent` (definition) + `AgentRun` (execution).

## Quickstart (kind)

```bash
./scripts/install-go-kind.sh
./scripts/e2e-kind.sh
```

This builds `kontext-operator:dev` and `kontext-echo:dev`, installs CRDs and the Go
controller, and verifies:

- a standalone `AgentRun` completes with `.status.result`
- a `Service` `Agent` re-casts after its Pod is deleted

### Standalone task

```bash
kubectl apply -f deploy/examples/v1alpha1/echo-task-run.yaml
kubectl get agentrun echo-review -w
kubectl logs -f run-echo-review
kubectl get agentrun echo-review -o jsonpath='{.status.result}'
```

### Persistent service owner

```bash
kubectl apply -f deploy/examples/v1alpha1/echo-service-agent.yaml
kubectl get agent echo-service -w
kubectl logs -f $(kubectl get pod -l kontext.dev/agent=echo-service -o jsonpath='{.items[0].metadata.name}')
```



```bash
```

## Local development

```bash
make test    # unit + envtest reconciler tests
make build   # compile operator binary to bin/manager
make run     # run controller locally (needs kubeconfig)
```

## Docs

| Doc | Purpose |
|-----|---------|
| [`SPEC.md`](SPEC.md) | API + runtime-image contract |
| [`ROADMAP.md`](ROADMAP.md) | Milestones and decisions |
| [`DEPRECATED.md`](DEPRECATED.md) | Hackathon stack on `deprecated/hackathon-python` |

## Layout

```
api/v1alpha1/          CRD Go types
cmd/                   Operator entrypoint
internal/controller/   Agent + AgentRun reconcilers
internal/podbuilder/   Pod construction
internal/runtimepolicy/ Provider credential wiring
config/                Kustomize install (CRDs, RBAC, manager)
deploy/examples/v1alpha1/  Sample manifests
runtimes/echo/         Keyless test runtime
runtimes/python-anthropic/ Optional Anthropic runtime image
scripts/               kind install + e2e
```
