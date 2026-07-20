---
title: Introduction
description: Kontext runs AI agents as Kubernetes workloads with Agent and AgentRun resources.
sidebarTitle: Introduction
---

# Introduction

Kontext is a Kubernetes-native control plane for running, governing, and
observing AI agents as production workloads.

The thesis: **agents are workloads.** An agent should not live in a screen
session or behind a bespoke orchestration service. It should be a resource your
cluster understands — created with `kubectl apply`, observed with
`kubectl logs -f`, governed by RBAC and budgets, and restarted by a controller
when it dies.

## Two resources

| Kontext | Kubernetes analogue | Behavior |
|---|---|---|
| `Agent` (mode `Service`) | `Deployment` | Always-on. The controller keeps one live `AgentRun` and re-casts it when it exits. |
| `Agent` (mode `Task`) | reusable template | Schema available; the controller reports `UnsupportedMode`. Use a standalone `AgentRun` for one-shot work. |
| `Agent` (mode `Scheduled`) | `CronJob` | Mints one-shot `AgentRun`s from standard five-field cron slots with safe overlap and deadline policy. |
| `AgentRun` | `Job` / `Pod` | One bounded execution. Owns exactly one Pod and holds `.status.result`. |

You can also create a standalone `AgentRun` without an owning `Agent` — useful
for ad-hoc dispatch and demos.

## Next steps

1. [Install without cloning](/docs/quickstart)
2. Learn the [resource model](/docs/resources)
3. Run a [Service-mode agent](/docs/service-workload)
4. Read the [API spec](/SPEC)
