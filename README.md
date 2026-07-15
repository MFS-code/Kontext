# Kontext

A Kubernetes-native control plane for running, governing, and observing AI agents as production workloads.

The thesis: **agents are workloads.** An agent should not live in a screen session or behind a bespoke orchestration service. It should be a resource your cluster understands — created with `kubectl apply`, observed with `kubectl logs -f`, governed by RBAC, budgets, and owner references, and restarted by a controller when it dies.

After the [quickstart](#quickstart-on-kind) below, this is the whole workflow:

```bash
kubectl apply -f deploy/examples/v1alpha1/echo-task-run.yaml
kubectl get agentrun echo-review -w
kubectl logs -f run-echo-review
kubectl get agentrun echo-review -o jsonpath='{.status.result}'
```

## Concepts

Kontext adds two custom resources under `kontext.dev/v1alpha1`, deliberately mirroring how core Kubernetes splits definition from execution:

| Kontext | Kubernetes analogue | Behavior |
|---|---|---|
| `Agent` (mode `Service`) | `Deployment` | Always-on. The controller keeps one live `AgentRun` and re-casts it with backoff when it exits. |
| `Agent` (mode `Task`) | reusable template | A definition that mints an `AgentRun` per invocation. *(Schema present; controller support planned.)* |
| `Agent` (mode `Scheduled`) | `CronJob` | Mints runs on a cron schedule. *(Schema present; controller support not yet planned.)* |
| `AgentRun` | `Job` / `Pod` | One bounded execution. Owns exactly one Pod, holds the immutable spec snapshot, the final `.status.result`, and usage. |

An `AgentRun` can also be created standalone, without any owning `Agent` — useful for ad-hoc dispatch and demos.

Agents themselves are **bring-your-own-runtime**: any container image that reads a few `KONTEXT_*` env vars, streams its reasoning to stdout, and writes a JSON result to `/dev/termination-log` is a valid agent. Kontext never inspects what the agent does — only that I/O boundary. The full contract is in [`SPEC.md`](SPEC.md).

## Quickstart on kind

Requires Docker, [kind](https://kind.sigs.k8s.io/), and kubectl.

```bash
./scripts/install-go-kind.sh   # build operator + echo runtime, install CRDs and controller
./scripts/e2e-kind.sh          # end-to-end verification
```

The e2e script proves the two core behaviors:

- a standalone `AgentRun` runs to completion and lands `.status.result`
- a `Service`-mode `Agent` re-casts its run after the Pod is deleted

### Run a one-shot task

```bash
kubectl apply -f deploy/examples/v1alpha1/echo-task-run.yaml
kubectl get agentrun echo-review -w
kubectl logs -f run-echo-review
kubectl get agentrun echo-review -o jsonpath='{.status.result}'
```

### Run a persistent service agent

```bash
kubectl apply -f deploy/examples/v1alpha1/echo-service-agent.yaml
kubectl get agent echo-owner -w
kubectl logs -f $(kubectl get pod -l kontext.dev/agent=echo-owner -o jsonpath='{.items[0].metadata.name}')
```

Delete the Pod and watch the controller mint a replacement run:

```bash
kubectl delete pod -l kontext.dev/agent=echo-owner
kubectl get agentruns -w
```

## Runtime images

| Image | Purpose |
|---|---|
| [`runtimes/echo/`](runtimes/echo) | Keyless test runtime. Exercises the full contract (stdout thoughts, termination-log result, service heartbeat) without any API key. |
| [`runtimes/python-anthropic/`](runtimes/python-anthropic) | Real runtime backed by the Anthropic Messages API. Needs an `ANTHROPIC_API_KEY` secret via `spec.secretRef`. |

Provider credentials are wired by the controller from a Kubernetes Secret into the env vars each provider expects (`ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, AWS credential pairs for Bedrock, and so on). See `internal/runtimepolicy/`.

## Governance

Every run is bounded and auditable:

- **Budgets** — `spec.budget.wallclock` is enforced by the controller (the Pod is killed and the run marked `BudgetExceeded`); token and dollar usage are reported by the runtime and recorded in `.status.usage`.
- **Immutable run specs** — an `AgentRun` snapshots its configuration at creation, so history doesn't drift when the `Agent` changes.
- **Standard lifecycle** — owner references give you GC cascade, conditions record transitions, and `kubectl` is the debugging surface.

## Development

```bash
make test    # unit + envtest reconciler tests
make build   # compile operator binary to bin/manager
make run     # run the controller locally against your kubeconfig
```

## Layout

```
api/v1alpha1/              CRD Go types
cmd/                       Operator entrypoint
internal/controller/       Agent + AgentRun reconcilers
internal/podbuilder/       Pod construction (env, volumes, credentials)
internal/runtimepolicy/    Provider credential wiring
internal/status/           Pod observation, termination parsing
internal/conditions/       Condition merge helpers
config/                    Kustomize install (CRDs, RBAC, manager)
deploy/examples/v1alpha1/  Sample manifests
runtimes/                  Runtime images (echo, python-anthropic)
scripts/                   kind install + e2e
```

## Docs

| Doc | Purpose |
|---|---|
| [`SPEC.md`](SPEC.md) | API and runtime-image contract |
| [`ROADMAP.md`](ROADMAP.md) | Milestones and locked decisions |
| [`DEPRECATED.md`](DEPRECATED.md) | The earlier Python prototype, preserved on `deprecated/hackathon-python` |

## Status

`v1alpha1` is alpha on purpose: the API shape is allowed to evolve. `Service` mode and standalone `AgentRun`s are implemented and covered by envtest and kind e2e; `Task` and `Scheduled` modes exist in the schema but are not reconciled yet.

## License

[MIT](LICENSE)
