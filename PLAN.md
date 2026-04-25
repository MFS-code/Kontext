# Kontext

> Kubernetes-native runtime for AI agents. Agents are workloads. `kubectl apply` an agent, `kubectl scale` an agent, `kubectl logs` its thoughts.

**Status:** Pre-hackathon plan
**Created:** 2026-04-20
**Mode:** Hackathon seed → summer OSS project

> 📖 **New here?** Read [`CONCEPT.md`](./CONCEPT.md) first — it's the ELI5 of what we're building, what the demo actually looks like, and why this is *real* Kubernetes (not a wrapper).

---

## The Thesis

The agent ecosystem is re-inventing scheduling, isolation, autoscaling, secrets, identity, and durable retries — badly. Meanwhile k8s spent a decade getting these right for a workload type (long-running, bursty, multi-tenant, expensive, failure-prone) that looks *exactly like an agent*.

Kontext is the bet that in 18 months "agent platforms" and "Kubernetes" converge, and the people who saw it first will own the primitives.

## Landscape (why this is whitespace)

Most existing projects build agents that **operate on** Kubernetes (kubectl-ai, KubeChat, Kubently, k8sgpt, k8s-mechanic, KubeHealer, etc. — dozens, mostly variants of "NL kubectl" or "self-healing operator"). The graveyard is full.

Almost nobody is treating **Kubernetes as the substrate for the agent itself** — its memory, scheduler, identity, and isolation primitives. Kagenti is the closest thing and is alpha (8 stars after 13 months). The first-principles flip is: "k8s is the best agent runtime we have and almost no one has noticed."

---

## The 4-Hour Hackathon Demo

Single CRD + a controller + a polished terminal UI, on a kind cluster, with one drop-the-mic moment.

### The CRD

```yaml
apiVersion: kontext.dev/v1
kind: Agent
metadata:
  name: research-trump-tariffs
spec:
  goal: "Find the 5 most-cited articles on US tariffs this month and summarize the consensus."
  model: claude-sonnet-4
  tools: [web_fetch, python]
  budget:
    tokens: 200000
    wallclock: 5m
    dollars: 2.00
```

### The Controller

Python with `kopf`, not Go — Go would eat 3 of the 4 hours.

- Watches `Agent` resources
- For each one, creates a Pod running an agent loop (Anthropic SDK + a ~100-line ReAct loop + the listed tools as Python functions)
- Streams the agent's reasoning to the pod's stdout (so `kubectl logs -f` shows it thinking in real time — the magic moment)
- Updates `status.phase`: `Pending → Thinking → Running → Succeeded/Failed/BudgetExceeded`
- Writes the final result into `status.result` so `kubectl get agent -o yaml` shows the answer
- Enforces `budget` by killing the pod if exceeded

### The TUI

Python with `textual`, built as a local terminal UI for the hackathon.

- Configures provider/API key, model, prompt, budget, tools, and replica count
- Creates or updates a Kubernetes `Secret` for the provider API key
- Generates an `Agent` YAML preview before launch
- Applies the generated resource to the cluster
- Watches `Agent` status from Kubernetes, not local state
- Streams pod logs into a live "thoughts" panel
- Shows `.status.result` when the agent finishes
- Supports fanout mode if time allows

The TUI is the visual layer for judging, but not the execution layer. The generated resource must still work with plain `kubectl`.

### The CLI (optional)

`kubectl-kontext run "summarize my emails"` — sugar that generates a CRD and tails logs. ~50 lines. Skip if running short.

### The Demo Script (5 minutes)

1. `kontext demo` opens the TUI. Configure provider/API key, model, prompt, budget, and replicas while the generated YAML previews live. *(60s)*
2. Launch from the TUI, then show `kubectl get agents -w` to prove it created a real Kubernetes resource. *(45s)*
3. Watch the agent reason in the TUI log panel, then mirror it with `kubectl logs -f agent/research-trump-tariffs`. ChatGPT-style streaming, but coming out of Kubernetes logs. *(90s)*
4. `kubectl get agent research-trump-tariffs -o jsonpath='{.status.result}'` — clean answer pops out. *(30s)*
5. **The mic drop:** TUI fanout mode or `kubectl apply -f research-fanout.yaml` — a single Agent with `replicas: 10` and 10 different goals templated in. Cluster spins up 10 pods. The TUI shows cards; `kubectl get agents -w` shows 10 agents thinking in parallel. *(90s)*
6. `kubectl delete agent research-trump-tariffs` mid-thought → pod gracefully terminates, refunds budget. "It's a real k8s primitive." *(30s)*

### What to Fake / Skip for the 4-Hour Version

- No webhooks, no validation — `kubectl apply` and trust the user
- No Helm chart — raw YAML install
- One model provider (Anthropic), hardcoded
- Three tools max (`web_fetch`, `python`, `bash`) — implemented as plain Python functions in the agent image
- No persistence beyond what k8s already gives you (the CR is the source of truth)
- "Budget enforcement" is just a token counter + a wallclock check; no cost-per-provider math
- TUI is local-only; no hosted SSH, no multi-user auth, no web dashboard
- TUI can have a demo replay fallback, but the primary path must use real Kubernetes objects
- Tests: zero. Push the demo.

### Parallel Build Split

This is now two demos in one: a real Kubernetes primitive and a visual terminal experience. Build them in parallel against a small shared contract.

- Agent A: CRD/controller/operator path — owns `Agent` schema, pod creation, status updates, deletion, and fanout.
- Agent B: runtime path — owns Anthropic call, ReAct loop, tools, stdout streaming, and final result.
- Agent C: TUI path — owns `textual` app, provider setup, YAML generation, launch flow, watch screen, and log/result panels.
- Agent D: demo polish — owns sample YAMLs, install script, README, fallback fixtures, and rehearsal script.

Shared contract:

- Spec: `goal`, `model`, `tools`, `budget`, optional `provider`, `secretRef`, `replicas`, `goalTemplate`, `topics`
- Status: `phase`, `result`, `podName`, optional `tokensUsed`, `dollarsUsed`, `message`

### Why Heavy Agentic Coding Unlocks This

Writing a working `kopf` controller + ReAct loop + Dockerfile + sample CRDs + kind setup script + TUI in one session is too much for one human to do cleanly in 4 hours. With parallel agents, it becomes plausible: the CRD/status contract lets the TUI and controller move independently, and demo polish can happen while the runtime is still coming together.

---

## The Summer Expansion

The hackathon ships a **toy controller**. The summer project turns it into something that could plausibly be at KubeCon, on Hacker News, or acquired.

### Month 1 — Make it real

Rewrite the controller in Go using kubebuilder (the *correct* tool for this; Python kopf was right for hackathon speed, wrong for credibility). Add:
- Helm chart
- Multi-provider support (Anthropic, OpenAI, Bedrock, local Ollama)
- Proper RBAC
- Admission webhook that validates budgets and tool allowlists
- Keep the terminal UI as the primary onboarding path, but make it talk to the Go operator cleanly

**Resume signal:** *"Wrote a production-grade Kubernetes operator in Go using kubebuilder, with admission webhooks, finalizers, and a custom CRD."*

### Month 2 — The killer feature: agent-to-agent as a Kubernetes Service

When Agent A wants to call Agent B, it does it through a normal k8s Service with normal NetworkPolicies. Implement the A2A protocol on top. Now agents have *service discovery*, *load balancing*, and *network policy* for free — things every agent framework is currently hand-rolling.

Add an `AgentTeam` CRD that composes agents into a workflow (one agent's output becomes another's input) with k8s as the coordinator.

**Resume signal:** *"Designed a multi-agent orchestration system on top of Kubernetes primitives, eliminating ~2000 lines of bespoke coordination code that frameworks like CrewAI and LangGraph reimplement."*

### Month 3 — The hard one that makes it research-paper-worthy

KEDA-style autoscaling on agent workload **plus** parallel hypothesis testing via vcluster sandboxes. An agent that needs to "try a deploy" gets its own ephemeral vcluster, runs three approaches in parallel pods, the controller diffs the results and promotes the winner.

**Resume signal:** *"Implemented parallel hypothesis-testing for agents using vcluster, achieving Nx faster convergence on infra tasks vs. sequential exploration."*

### Month 4 — Distribution

- OperatorHub.io listing
- Real website
- 2-minute demo video
- Launch post (HN, Reddit r/kubernetes, X)
- CFP submitted to KubeCon NA / KubeCon EU
- Discord opened

**Goal:** not 10K stars — **200 stars from the right people** (a Tailscale engineer, a couple of Anthropic folks, the kubebuilder maintainers).

---

## Why This Lands on a Resume Better Than 90% of Side Projects

- It's **infra**, the rarest and highest-paid skill in the AI hiring market right now. Everyone has a chatbot demo. Almost nobody has shipped a real CRD + controller.
- It's **opinionated**. There's a thesis ("k8s is the agent runtime") you can defend for 20 minutes in an interview. That's worth more than working code.
- It's **timed correctly**. Six months ago this would have been too early; eighteen months from now it'll be obvious and crowded. The window is now.
- Rare combo: **deep systems** (operators, CRDs, networking) **+ frontier AI** (agents, A2A, multi-model). The intersection is where the next decade of jobs are.
- It has **a story arc**: "Started as a hackathon hack, became a real OSS project, got N stars, presented at meetup X, used by Y." That's the structure recruiters and founders pattern-match on.

---

## Open Questions

1. Go for the summer rewrite, or is the language flexible? (Shapes month 1.)
2. Solo or team for the hackathon? (Changes scope ~30%.)
3. Anthropic / OpenAI API key available for the demo, or fall back to Ollama?
4. What's the hackathon's judging vibe — infra/devops, AI/agents, or general/VC?
5. How heavily is UI judged, and does a polished local TUI satisfy the rubric?
6. Should fanout be one `Agent` with `replicas`, or should the TUI generate many `Agent` resources?

---

## Next Steps

- [ ] Answer the open questions above
- [ ] Lock the shared `Agent` spec/status contract before parallel work starts
- [ ] Run a formal design-doc pass (premise check + 2-3 alternative architectures) before hackathon day
- [ ] Pre-build kind cluster setup script + Dockerfile skeleton (counts as prep, not "starting early")
- [ ] Write the demo YAMLs in advance so the live demo is `kubectl apply` not "let me type this"
- [ ] Build the `textual` TUI skeleton with setup, launch, YAML preview, and watch screens
- [ ] Pick the final name (Kontext / Kagent / Pod Genie / something else)
