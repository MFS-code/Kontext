# AGENTS.md

## Workspace & Cross-Project Vision

> This repo is part of a two-project Cursor workspace: **AgentNet** and **Kontext**, worked on together on purpose. Cursor reads `AGENTS.md` per repo (there is no enclosing repo over both), so this section is mirrored in both repos to give every agent the shared picture. Keep the two copies in sync when the vision changes.
>
> **You are in: Kontext.** Its sibling repo in this workspace is **AgentNet**.

### The two projects

- **Kontext** — a Kubernetes-native control plane for running, governing, and observing AI agents as production workloads. Thesis: *agents are workloads*. An `Agent` is a real custom resource reconciled into real Pods; `kubectl apply`, `kubectl logs`, and `.status.result` work through Kubernetes, not mock state. Kontext is meant to be a **general, standalone product**, not tied to any one consumer.
- **AgentNet** — an infrastructure layer that makes a codebase agent-native: it indexes a repo into a code graph and partitions it into a web of persistent, specialized **agentic code-owners** ("ownership zones") that review incoming code, hand the right context to external working agents, and enforce local conventions — mirroring how human engineering orgs do local ownership plus structured handoff.

### How they relate (and the firewall)

AgentNet is meant to **run on Kontext**: each persistent code-owner is deployed as a long-running (`Service`-mode) Kontext `Agent`, so when one fails it is **instantly re-cast** as a Kubernetes workload. The work a code-owner performs (review this PR, assemble context for task X) is dispatched as one-shot `AgentRun`s.

Critical boundary — **Kontext must stay general**. It never learns AgentNet's vocabulary ("code owner", "zone", "repository", "pull request"). AgentNet encodes all of that inside its own **runtime image** and orchestrates by creating generic Kontext `Agent`/`AgentRun` objects. If a feature cannot be expressed without Kontext knowing a consumer's domain, it belongs in the consumer's image, not in Kontext. (See the anti-overfit firewall in `SPEC.md`.)

### Current direction (live)

- **Go + kubebuilder** operator is the product control plane (`api/v1alpha1`, `internal/controller/`).
- **API model Option B:** `Agent` (definition) + `AgentRun` (execution). Modes: `Service` (implemented), `Task`/`Scheduled` (schema only).
- Canonical docs: **`SPEC.md`**, **`ROADMAP.md`**, **`README.md`**.
- Hackathon Python stack lives on branch **`deprecated/hackathon-python`** — see `DEPRECATED.md`. Do not reintroduce it on main.

### Maintainer context

The author is a junior engineer treating these as learning projects (experienced with cloud APIs; newer to Go and Kubernetes). Prefer building real product over throwaway practice, explain non-obvious Go/Kubernetes steps while doing them, and never make large architectural decisions silently — surface the trade-offs first.

## First Read

Before making code changes, read in order:

1. `SPEC.md` — API + runtime-image contract
2. `ROADMAP.md` — milestones and locked decisions
3. `README.md` — install, test, and layout

If docs disagree, prefer the codebase, then update docs.

## Project Thesis

> Agents are workloads.

An `Agent`/`AgentRun` must reconcile into real Pods. `kubectl apply`, `kubectl get agentruns`, `kubectl logs`, and `.status.result` are the demo path — not mock state.

## Architecture (main)

| Path | Owns |
|------|------|
| `api/v1alpha1/` | CRD Go types |
| `internal/controller/` | Agent + AgentRun reconciliation |
| `internal/podbuilder/` | Pod env, volumes, provider secrets |
| `internal/runtimepolicy/` | Provider credential table |
| `internal/status/` | Pod observation, termination parsing |
| `internal/conditions/` | Shared condition merge helpers |
| `config/` | Kustomize: CRDs, RBAC, manager Deployment |
| `deploy/examples/v1alpha1/` | Sample manifests |
| `runtimes/*/` | Container runtime images (bring-your-own-runtime) |

## Demo invariants

- `kubectl apply -f deploy/examples/v1alpha1/echo-task-run.yaml` → `AgentRun` → Pod → `.status.result`
- `kubectl apply -f deploy/examples/v1alpha1/echo-service-agent.yaml` → Service `Agent` mints child runs; pod delete triggers recast
- `./scripts/e2e-kind.sh` passes on a fresh kind cluster

## Local test path

```bash
make test
./scripts/install-go-kind.sh
./scripts/e2e-kind.sh
```

## Engineering guidance

- Keep Kontext general — no consumer domain fields on CRDs.
- Preserve stdout streaming (`kubectl logs -f`) as the observability contract.
- Run `make test` after controller or API changes.
- Always provide a way to test your work and explain what you did.
