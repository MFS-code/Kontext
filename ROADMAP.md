# Kontext Roadmap (current direction)

> Near-term execution guide. Historical hackathon planning lives on branch `deprecated/hackathon-python`.

## Thesis (unchanged)

Agents are workloads. Kontext is the Kubernetes-native control plane for running, governing, and observing AI agents as production workloads — usable by anyone, not tied to any one consumer.

## Decisions locked

- **API model: Option B — `Agent` (definition) + `AgentRun` (execution).** Idiomatic K8s (mirrors `Deployment`/`CronJob` → `Job` → `Pod`). Gives run history + governance for free. See `SPEC.md`.
- **Language: Go + kubebuilder.** Greenfield operator under `cmd/`, `internal/`, `config/`. Maintained runtime and support images live under `runtimes/` (echo, reference, reporter); the old Python Anthropic source remains only as an unmaintained migration example.
- **Scope: MVP.** Prove the thesis and support real external workloads. Out of scope for now: A2A service mesh, `AgentTeam`, vcluster hypothesis-testing, KEDA autoscaling, web dashboard, OperatorHub, multi-provider sprawl.
- **Delivery:** Milestones are incremental product slices with explicit completion criteria.

## Consumer boundary

Kontext provides generic `Agent` and `AgentRun` primitives. Consumer-specific concepts and workflows stay inside runtime images and external orchestration (see the `SPEC.md` anti-overfit firewall).

## Milestones

### M0 — Contract + scaffold ✅
- `SPEC.md` drafted.
- Go operator scaffolded (`api/v1alpha1`, controllers, kustomize install).
- **Done when:** both CRDs install and controllers run on kind.

### M1 — `AgentRun` → Pod engine ✅
- `AgentRun` reconciler creates Pods, tracks phases, parses termination messages, enforces wallclock budget.
- **Done when:** standalone `AgentRun` lands `.status.result` (`scripts/e2e-kind.sh`).

### M2 — `Agent` (Task) → `AgentRun` templating
- Deferred for now; callers can dispatch standalone `AgentRun`s directly.

### M3 — `Agent` (Service) → continuous run + auto-recast ✅
- Service reconciler keeps one live child `AgentRun` and re-casts with backoff.
- **Done when:** deleting the live Pod mints a replacement run (`scripts/e2e-kind.sh`).

### M4 — Bring-your-own-runtime hardening (implementation complete)
- The echo conformance oracle remains keyless and uses the accepted legacy
  payload during the v1alpha1 transition. The Python Anthropic source is an
  unmaintained, non-conformant migration example and is not published.
- Versioned results, the reusable reporter, and optional stdout capture support existing Linux images with explicit commands.
- The maintained Go reference runtime has a provider-neutral core, deterministic fake-provider path, and direct Anthropic and OpenAI-compatible HTTP transports.
- The maintained runtime has a bounded provider-neutral loop with allowlisted knowledge, Kubernetes-read, and shell tools.
- Issue #20's implementation adds allowlisted stdio/HTTP MCP tools and the
  isolated Playwright acceptance path without adding MCP vocabulary to the
  CRDs.
- Issue #21 adds external deterministic evals, all four bring-your-own result
  paths, keyless failure acceptance, operations guidance, and bounded provider
  acceptance records. A protected authenticated provider dispatch is still a
  required pre-alpha release action, not evidence produced by keyless CI.

### M5 — Governance (remaining)
- Expand CEL/admission validation beyond the shipped immutable-run and
  restricted-security checks.
- Add richer Kubernetes Events and metrics for budget decisions, failures, and
  Service recasts.
- Decide whether authoritative dollar-limit enforcement and finalizer-backed
  cleanup belong in the alpha contract, then implement only the retained
  behavior.

### M6 — Packaging + observability
- Kustomize install exists under `config/default`. Helm/metrics still open.
- Tag-driven releases publish version-matched operator, echo, reporter, and
  reference images for `linux/amd64` and `linux/arm64`, with immutable digests
  attached to the GitHub release.
- Each release includes a single install manifest with digest-pinned operator
  and reporter images for standard Kubernetes clusters.

### M7 — External integration spike ✅
- Validated a `Service` `Agent`, knowledge `ConfigMap`, and task `AgentRun` from an external client.

## Immediate next actions

1. M2 Task templating when a concrete consumer needs parameterized triggers.
2. M5 governance (remaining CEL/admission coverage, Events, and metrics).
3. Dispatch and retain the protected provider acceptance record before alpha.
4. Publish immutable maintained runtime images as separately tracked release work.
