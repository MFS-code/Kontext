# Kontext Project Phases

## Phase 0: Contract And Skeleton

Lock the `Agent` API shape before building around it.

Deliverables:

- `Agent` CRD schema
- shared sample YAMLs
- repo layout
- `pyproject.toml` dependencies
- kind/install script skeleton
- fanout decision: one `Agent` with `replicas/topics` vs many generated `Agent`s

Exit criteria: `kubectl apply -f deploy/crds/agents.kontext.dev.yaml` works and `kubectl get agents` recognizes the resource.

## Phase 1: Fake Runtime, Real Kubernetes

Prove the Kubernetes primitive before doing real AI.

Deliverables:

- `kopf` controller watches `Agent` resources
- controller creates one Pod per `Agent`
- Pod runs a fake runner that prints "thinking" logs and exits
- controller updates `.status.phase`, `.status.podName`, `.status.result`, and budget counters
- deletion cleans up Pods through owner references

Exit criteria: `kubectl apply -f deploy/examples/research-agent.yaml`, `kubectl get agents -w`, `kubectl logs -f agent-research-tariffs`, and `kubectl get agent research-tariffs -o yaml` all tell the right story.

## Phase 2: Real Runtime

Replace the fake runner with the smallest useful AI loop.

Deliverables:

- Anthropic SDK integration
- prompt from `spec.goal`
- stdout streaming for reasoning/progress
- final answer written so the controller can put it into `.status.result`
- wallclock timeout and basic token counting
- provider API key read from Kubernetes `Secret`

Exit criteria: an actual model answers a real prompt from inside a Kubernetes Pod, and the answer appears in `Agent.status.result`.

## Phase 3: Demo Launcher/TUI

Build the judging surface around the working primitive.

Deliverables:

- `kontext demo`
- provider/API key setup
- model, goal, budget, replicas form
- live YAML preview
- launch action
- watch screen with agent cards, logs, status, and result

Exit criteria: the TUI creates the same resources that can be applied manually, then watches Kubernetes state rather than local state.

## Phase 4: Fanout Mic Drop

Add the moment that makes Kubernetes feel inevitable.

Deliverables:

- either `spec.replicas + topics/goalTemplate`, or TUI-generated many `Agent` resources
- controller creates parallel pods
- status/log viewing for many agents
- sample `research-fanout.yaml`

Exit criteria: one command launches about 10 agents, `kubectl get agents -w` shows parallel progress, and the TUI shows multiple live cards.

## Phase 5: Demo Hardening

Make it survive a live room.

Deliverables:

- install script
- Docker build/load into kind
- sample manifests
- README quickstart
- fallback/demo replay mode if API or network fails
- rehearsed 5-minute script

Exit criteria: a fresh kind cluster can run the full demo with minimal manual steps.
