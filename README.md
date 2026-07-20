# Kontext

A Kubernetes-native control plane for running, governing, and observing AI agents as production workloads.

The thesis: **agents are workloads.** An agent should not live in a screen session or behind a bespoke orchestration service. It should be a resource your cluster understands — created with `kubectl apply`, observed with `kubectl logs -f`, governed by RBAC, budgets, and owner references, and restarted by a controller when it dies.

Public sites:

- Marketing site: [kontext.run](https://kontext.run)
- Docs: [docs.kontext.run](https://docs.kontext.run)

After the [quickstart](#quickstart-on-kind) below, this is the whole workflow:

```bash
./scripts/apply-example.sh deploy/examples/v1alpha1/echo-task-run.yaml
kubectl get agentrun echo-review -w
kubectl logs -f run-echo-review
kubectl get agentrun echo-review -o jsonpath='{.status.result}'
```

## Concepts

Kontext adds two custom resources under `kontext.dev/v1alpha1`, deliberately mirroring how core Kubernetes splits definition from execution:

| Kontext | Kubernetes analogue | Behavior |
|---|---|---|
| `Agent` (mode `Service`) | `Deployment` | Always-on. The controller keeps one live `AgentRun` and re-casts it with backoff when it exits. |
| `Agent` (mode `Task`) | reusable template | Schema available; the controller reports `UnsupportedMode`. Create a standalone `AgentRun` for one-shot work. |
| `Agent` (mode `Scheduled`) | `CronJob` | Schema available; the controller reports `UnsupportedMode` and does not schedule runs. |
| `AgentRun` | `Job` / `Pod` | One bounded execution. Owns exactly one Pod, holds the immutable spec snapshot, the final `.status.result`, and usage. |

An `AgentRun` can also be created standalone, without any owning `Agent` — useful for ad-hoc dispatch and demos.

Agents themselves are **bring-your-own-runtime**. A plain Linux image can run
with ordinary logs and no structured result; images that need result status can
write the termination envelope natively or opt into stdout capture. Kontext
never inspects what the agent does beyond that I/O boundary. Structured
consumers should read `.status.output`; `.status.result` remains its
backward-compatible text projection. The full versioned contract is in
[`SPEC.md`](SPEC.md).

## Install a tagged release

An existing Kubernetes cluster needs only kubectl and permission to create
CRDs, cluster-scoped RBAC, a Namespace, and a Deployment. Install the published
`v0.1.0-alpha.1` release directly from GitHub:

```bash
VERSION=v0.1.0-alpha.1
kubectl apply -f \
  "https://github.com/MFS-code/Kontext/releases/download/${VERSION}/install.yaml"
kubectl rollout status deployment/controller-manager \
  --namespace kontext-system \
  --timeout=120s
```

The release manifest pins the operator and trusted reporter images by digest.
It does not require Docker, kind, or a repository clone. See
[`docs/releases.md`](docs/releases.md) (also on [docs.kontext.run](https://docs.kontext.run/docs/releases))
before upgrading or uninstalling because deleting the CRDs also deletes every
`Agent` and `AgentRun`.

Before operating an alpha installation, read the
[alpha support, security, and troubleshooting contract](docs/operations.md).

## Quickstart on kind

Requires Docker, [kind](https://kind.sigs.k8s.io/), and kubectl.

```bash
make kind-install       # build operator + echo images, load into kind, install controller
./scripts/e2e-kind.sh   # end-to-end verification
./scripts/eval-kind.sh  # deterministic evals against the same cluster
```

The e2e script proves four core behaviors:

- a standalone `AgentRun` runs to completion and lands `.status.result`
- unmodified images can expose last-line or structured stdout results
- child exit codes and SIGTERM behavior survive reporter injection
- a `Service`-mode `Agent` re-casts its run after the Pod is deleted

### Run a one-shot task

```bash
./scripts/apply-example.sh deploy/examples/v1alpha1/echo-task-run.yaml
kubectl get agentrun echo-review -w
kubectl logs -f run-echo-review
kubectl get agentrun echo-review -o jsonpath='{.status.result}'
```

### Run a persistent service agent

```bash
./scripts/apply-example.sh deploy/examples/v1alpha1/echo-service-agent.yaml
kubectl get agent echo-service -w
kubectl logs -f $(kubectl get pod -l kontext.dev/agent=echo-service -o jsonpath='{.items[0].metadata.name}')
```

Delete the Pod and watch the controller mint a replacement run:

```bash
kubectl delete pod -l kontext.dev/agent=echo-service
kubectl get agentruns -w
```

## Runtime images

| Image | Purpose |
|---|---|
| [`runtimes/echo/`](runtimes/echo) | Keyless control-plane conformance oracle. It still emits the accepted legacy termination payload during the v1alpha1 transition. |
| [`runtimes/reference/`](runtimes/reference) | Maintained provider-neutral Go runtime with fake, Anthropic, and OpenAI-compatible transports plus a bounded built-in tool loop. |
| [`runtimes/reporter/`](runtimes/reporter) | Reusable PID 1 supervisor. Preserves child logs and process semantics while producing the versioned result envelope. |

Provider credentials are wired by the controller from a Kubernetes Secret into
the env vars each provider expects. The maintained reference transports use
`ANTHROPIC_API_KEY` and `OPENAI_API_KEY`; see its
[compatibility and Secret documentation](runtimes/reference/README.md).
Credentialed acceptance is dispatch-only and protected from pull-request CI.
The reference runtime exposes only tools listed in `spec.tools`; Kubernetes
RBAC, mounts, security context, and NetworkPolicy remain the authority for
what those tools may access.

The reference runtime also discovers allowlisted stdio and Streamable HTTP MCP
tools. The maintained browser example deploys pinned Playwright MCP as a
separate restricted Deployment/Service and connects to it from keyless fake
provider AgentRuns. Its Calico-backed acceptance covers deterministic browser
interaction, fresh profiles, denied metadata/internal egress, wallclock
cleanup, omitted tool event content, and absent provider credentials and
ServiceAccount tokens:

```bash
./scripts/e2e-kind-network-policy.sh
```

kindnet does not enforce NetworkPolicy, so browser policy results are asserted
only in that disposable Calico cluster. See
[`runtimes/reference/README.md`](runtimes/reference/README.md) for the pinned
image, command, resource checkpoint, and sandbox caveat.

Mounted `knowledgeConfigMapRef` data is static context, not production RAG.
Making a tool available does not guarantee model use; versioned tool events
are the execution evidence. See [`docs/runtimes.md`](docs/runtimes.md).

### Capture results from an existing image

An existing Linux container can opt into stdout result capture without adding
Kontext code:

```yaml
runtime:
  image: example/agent:v1
  command: ["python", "-m", "agent"]
  result:
    source: Stdout
    format: LastLine
```

The command is explicit because Kubernetes cannot recover an image entrypoint
after Kontext replaces it with the reporter. `LastLine` captures the final
non-empty stdout line as text; `KontextEnvelope` accepts a
`KONTEXT_RESULT:`-prefixed structured envelope. Logs remain ordinary
`kubectl logs` output. If `result` is absent, the image runs unchanged. Cluster
install manifests declare the trusted reporter image alongside the operator
image. The development overlay selects the locally built reporter used by
`make kind-install`.

The complete four-path example index (plain logs, final-line capture,
structured capture, and native envelope) is in
[`deploy/examples/v1alpha1/README.md`](deploy/examples/v1alpha1/README.md).

## Evaluations

`kontext-eval` evaluates workloads from outside the Pod. Deterministic graders
run before any optional model judge. JSONL records retain only explicitly
graded status/model output, projected envelope fields, bounded failure
messages, and requested event metadata—never raw logs, Pod environments, or
Secret values:

```bash
make build-eval
./bin/kontext-eval --suite evals/suites/keyless.yaml
```

Pull-request CI runs the keyless suite in the existing kind cluster and uploads
its machine-readable records. Authenticated transport acceptance remains a
dispatch-only, environment-protected workflow; its short-lived artifact is the
release evidence. A keyless or render-only run is not an authenticated
provider acceptance. See [`docs/evals.md`](docs/evals.md).

## Releases

A SemVer-compatible git tag publishes version-matched operator, echo, reporter,
and reference images for `linux/amd64` and `linux/arm64`. Release artifacts
include immutable digests; mutable `latest` and `dev` tags are not published.
See [`docs/releases.md`](docs/releases.md) for the tag, image, and `v1alpha1`
compatibility contract.

## Governance

Each `AgentRun` is isolated and auditable. Budgets and runtime limits are
optional: task examples configure finite limits, while a long-running Service
may intentionally omit `budget.wallclock`.

- **Optional budgets** — when configured, `spec.budget.wallclock` is enforced by the controller (the Pod is killed and the run marked `BudgetExceeded`); token and dollar usage are reported by the runtime and recorded in `.status.usage`. The reference runtime checks configured token budgets against cumulative measured usage across provider requests. Tool follow-ups resend conversation context, and reasoning usage can exceed the visible output, so live model budgets need provider-specific headroom.
- **Immutable run specs** — an `AgentRun` snapshots its configuration at creation, so history doesn't drift when the `Agent` changes.
- **Standard lifecycle** — owner references give you GC cascade, conditions record transitions, and `kubectl` is the debugging surface.

## Contributing and support

- Read [SUPPORT.md](SUPPORT.md) before opening an issue.
- Report vulnerabilities privately according to [SECURITY.md](SECURITY.md).
- Development and pull request guidance lives in
  [CONTRIBUTING.md](CONTRIBUTING.md).

## Development

```bash
make verify            # CRD/deepcopy generation + gofmt drift check
make test              # unit + envtest reconciler tests
make vulncheck         # scan reachable Go code for known vulnerabilities
make docker-build-all  # operator + all maintained runtime images
make docker-build-reporter  # build the reusable result reporter image
make docker-build-reference # build the model-agnostic reference runtime
make kind-install      # build operator + echo, load into kind, install controller
make build-eval        # compile the external evaluation runner
make build             # compile operator binary to bin/manager
make run               # run the controller locally against your kubeconfig
```

Pull requests and pushes to `main` run validate (including `make vulncheck`), Dockerfile smoke for every shipped image, and keyless kind e2e via `.github/workflows/ci.yml`. After a failed local kind run, collect cluster state with `./scripts/collect-kind-diagnostics.sh`.

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
runtimes/                  Runtime images and reusable result reporter
scripts/                   kind install + e2e
```

## Docs

| Doc | Purpose |
|---|---|
| [`SPEC.md`](SPEC.md) | API and runtime-image contract |
| [`docs/operations.md`](docs/operations.md) | Alpha support matrix, security boundaries, troubleshooting, and lifecycle |
| [`docs/releases.md`](docs/releases.md) | Release tags, installation, upgrades, and uninstall |
| [`docs/runtimes.md`](docs/runtimes.md) | Runtime roles, result paths, static context, and tools |
| [`docs/evals.md`](docs/evals.md) | Deterministic evals and provider acceptance records |
| [`docs/when-not-to-use-agents.md`](docs/when-not-to-use-agents.md) | Choosing a deterministic Job or script instead |
| [`CONTRIBUTING.md`](CONTRIBUTING.md) | Development and pull request workflow |
| [`SECURITY.md`](SECURITY.md) | Supported versions and private vulnerability reporting |
| [`SUPPORT.md`](SUPPORT.md) | Alpha support scope and useful bug reports |
| [`DEPRECATED.md`](DEPRECATED.md) | The earlier Python prototype, preserved on `deprecated/hackathon-python` |

## Status

The current public release is `v0.1.0-alpha.1`. Its GitHub release contains
the digest-pinned install manifest and versioned multi-architecture images.
`v1alpha1` is alpha on purpose: the API shape is allowed to evolve. `Service`
mode and standalone `AgentRun`s are implemented and covered by envtest and
kind e2e; `Task` and `Scheduled` modes exist in the schema but are not
reconciled.

## License

[MIT](LICENSE)
