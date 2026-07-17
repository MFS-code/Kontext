# Kontext Roadmap (current direction)

> Near-term execution guide. Historical hackathon planning lives on branch `deprecated/hackathon-python`.

## Thesis (unchanged)

Agents are workloads. Kontext is the Kubernetes-native control plane for running, governing, and observing AI agents as production workloads — usable by anyone, not tied to any one consumer.

## Decisions locked

- **API model: Option B — `Agent` (definition) + `AgentRun` (execution).** Idiomatic K8s (mirrors `Deployment`/`CronJob` → `Job` → `Pod`). Gives run history + governance for free. See `SPEC.md`.
- **Language: Go + kubebuilder.** Greenfield operator under `cmd/`, `internal/`, `config/`. Optional runtime images under `runtimes/` (echo, python-anthropic).
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

### M4 — Bring-your-own-runtime hardening
- Echo runtime shipped (`runtimes/echo/`). Anthropic Python runner remains under `runtimes/python-anthropic/`.
- Versioned results, the reusable reporter, and optional stdout capture support existing Linux images with explicit commands.

### M5 — Governance
- Per-agent ServiceAccount, finalizers, budget enforcement, CEL validation on the CRDs, events on transitions.
- **Done when:** demo: "capped at $2, ran under this SA, failed on budget, Kubernetes recorded the lifecycle."

### M6 — Packaging + observability
- Kustomize install exists under `config/default`. Helm/metrics still open.

### M7 — External integration spike ✅
- Validated a `Service` `Agent`, knowledge `ConfigMap`, and task `AgentRun` from an external client.

## Immediate next actions

1. M2 Task templating when a concrete consumer needs parameterized triggers.
2. M5 governance (CEL, per-agent SA, richer events).
3. Anthropic runtime parity on v1alpha1 `AgentRun`.
