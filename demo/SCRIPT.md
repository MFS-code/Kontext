# Kontext 5-Minute Demo Script

## Before The Room

Run the hardening path once:

```bash
export ANTHROPIC_API_KEY=...
./scripts/install-kind.sh
python3 -m pip install -e .
```

Keep a replay fallback ready if the provider or network fails:

```bash
kubectl apply -f deploy/examples/replay-agent.yaml
kontext demo --fallback
```

## 0:00-1:00 Configure

Open the guided launcher:

```bash
kontext demo
```

Show the generated `Agent` YAML while saying: "This is an agent as a Kubernetes resource. Same control plane, new workload type."

## 1:00-2:15 Launch

Launch from the TUI or run:

```bash
kubectl apply -f deploy/examples/research-agent.yaml
kubectl get agents -w
```

Point out the CRD state changing through Kubernetes, not through a local process.

## 2:15-3:15 Watch Logs

Stream the pod:

```bash
kubectl logs -f agent-research-tariffs
```

Let the stdout stream breathe. The point is that `kubectl logs` works because the agent is a normal Pod.

## 3:15-3:45 Show Result

Read the reconciled answer:

```bash
kubectl get agent research-tariffs -o jsonpath='{.status.result}'
```

Say: "The result lives in the Kubernetes object. I can query it, back it up, or hand it to another controller."

## 3:45-5:00 Fanout

Launch the parallel demo:

```bash
kubectl apply -f deploy/examples/research-fanout.yaml
kubectl get agents -w
```

Close with: "This is the same scaling primitive Kubernetes uses for web servers. Agents are workloads."

## If The API Fails

Switch without apologizing:

```bash
kubectl apply -f deploy/examples/replay-agent.yaml
kubectl get agents -w
kubectl logs -f agent-replay-tariffs
kubectl get agent replay-tariffs -o yaml
```

The replay path still proves the Kubernetes contract: CRD, controller, Pod, logs, status, and cleanup.
