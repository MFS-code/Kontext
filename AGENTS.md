# AGENTS.md

## First Read

Before making code changes, read these docs in order:

1. `CONCEPT.md` - project vision, demo beats, and why this is real Kubernetes.
2. `PLAN.md` - hackathon plan, scope, build split, and summer expansion.
3. `PHASES.md` - current implementation phases and exit criteria.
4. `README.md` - current quickstart and test commands.

If the docs disagree, prefer the most recent implementation state in the codebase, then update docs when the direction changes.

## Project Thesis

Kontext is a Kubernetes-native runtime for AI agents. The key idea is:

> Agents are workloads.

An `Agent` should be a real Kubernetes custom resource, reconciled by a real controller into real Pods. The demo must prove that `kubectl apply`, `kubectl get agents`, `kubectl logs`, and `.status.result` all work through Kubernetes, not through local mock state.

## Current Phase

The repo is currently in Phase 1:

- Real CRD: `deploy/crds/agents.kontext.dev.yaml`
- Python `kopf` controller: `src/kontext_operator/controller.py`
- Fake runtime runner: `src/kontext_runtime/fake_runner.py`
- Controller/RBAC install manifest: `deploy/install.yaml`
- Example resource: `deploy/examples/research-agent.yaml`
- TUI prototype: `src/kontext_tui/`

Phase 1 goal: fake AI runtime, real Kubernetes path.

## Architecture Boundaries

- `src/kontext_operator/` owns Kubernetes reconciliation.
- `src/kontext_runtime/` owns code that runs inside agent Pods.
- `src/kontext_tui/` owns the local terminal UI and should treat Kubernetes as the source of truth.
- `deploy/` owns CRDs, RBAC, controller deployment, and example manifests.
- `demo/fixtures/` owns canned fallback data for presentation safety.

Do not make the TUI the execution layer. It may generate/apply YAML and watch resources, but the resource must still work with plain `kubectl`.

## Agent Resource Contract

The shared `Agent.spec` contract is:

- `goal`
- `provider`
- `model`
- `tools`
- `budget.tokens`
- `budget.wallclock`
- `budget.dollars`
- optional `secretRef`
- optional `replicas`
- optional `goalTemplate`
- optional `topics`

The shared `Agent.status` contract is:

- `phase`
- `podName`
- `result`
- `message`
- optional `tokensUsed`
- optional `dollarsUsed`

Keep the controller, TUI models, CRD schema, and examples aligned when changing this contract.

## Demo-Critical Invariants

- `kubectl apply -f deploy/examples/research-agent.yaml` should create an `Agent`.
- The controller should create a Pod named like `agent-<agent-name>`.
- `kubectl logs -f <pod>` should show agent thoughts streaming from stdout.
- `kubectl get agents -w` should show phase movement.
- `kubectl get agent <name> -o yaml` should show `.status.result` after completion.

Protect these before polishing internals.

## Local Test Path

For a kind cluster named `kontext`:

```bash
docker build -t kontext:dev .
kind create cluster --name kontext
kind load docker-image kontext:dev --name kontext
kubectl apply -f deploy/crds/agents.kontext.dev.yaml
kubectl apply -f deploy/install.yaml
kubectl apply -f deploy/examples/research-agent.yaml
kubectl get agents -w
kubectl logs -f agent-research-tariffs
kubectl get agent research-tariffs -o jsonpath='{.status.result}'
```

If the cluster already exists, skip `kind create cluster`.

## Engineering Guidance

- Optimize for a working demo vertical slice over production completeness.
- Keep hackathon code small and legible.
- Prefer real Kubernetes primitives over local wrappers or subprocess illusions.
- Use raw manifests for now; no Helm until later phases.
- Use Python for the hackathon controller/runtime; the planned production rewrite is Go/kubebuilder later.
- Avoid broad refactors unless they directly protect the demo path.
- When adding runtime features, preserve stdout streaming because `kubectl logs -f` is the magic moment.
- Always provide a way to test your work after completing a task as well as a detailed explanation of everything you did.