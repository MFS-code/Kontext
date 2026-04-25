# Kontext

Kubernetes-native AI agents with a polished terminal demo layer.

## Quickstart

Install Kontext into a fresh or existing kind cluster:

```bash
export ANTHROPIC_API_KEY=...
./scripts/install-kind.sh
```

The script builds `kontext:dev`, creates a `kontext` kind cluster if needed,
loads the image into kind, installs the CRD/controller, and creates
`Secret/kontext-anthropic` when `ANTHROPIC_API_KEY` is set.

Launch the single-agent demo:

```bash
kubectl apply -f deploy/examples/research-agent.yaml
kubectl get agents -w
kubectl logs -f agent-research-tariffs
kubectl get agent research-tariffs -o jsonpath='{.status.result}'
```

The runner streams progress to pod stdout, calls Anthropic with `spec.goal`,
writes the final answer to the pod termination message, and the controller
reconciles that into `.status.result` with basic token usage.

## Replay Fallback

If the API key, provider, or network is unavailable, use the Kubernetes replay
manifest. It still creates a real `Agent`, controller-managed Pod, pod logs, and
`.status.result`; it just runs the canned fake runner instead of Anthropic.

```bash
kubectl apply -f deploy/examples/replay-agent.yaml
kubectl get agents -w
kubectl logs -f agent-replay-tariffs
kubectl get agent replay-tariffs -o yaml
```

## Run the TUI

Install the local package and dependencies:

```bash
python3 -m pip install -e .
```

Open the guided launcher:

```bash
kontext demo
```

If the cluster or provider is not ready, use the canned fallback flow:

```bash
kontext demo --fallback
```

The TUI generates real `kontext.dev/v1` `Agent` YAML, applies it through
`kubectl apply -f -`, and watches `Agent` status and pod logs through Kubernetes.

## Fanout Demo

Launch ten agents in parallel with one manifest:

```bash
kubectl apply -f deploy/examples/research-fanout.yaml
kubectl get agents -w
```

In `kontext demo`, set `replicas` above `1` and edit the fanout topics. The
YAML preview becomes a multi-document manifest, and launch applies all generated
`Agent` resources through Kubernetes.

## Demo Script

Use `demo/SCRIPT.md` as the rehearsed 5-minute path for judges. It includes the
single-agent launch, `kubectl logs`, status result, fanout, and replay fallback.
