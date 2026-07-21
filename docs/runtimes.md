---
title: Runtimes
description: Echo, reference, and bring-your-own runtime roles and result capture paths.
sidebarTitle: Runtimes
---

# Runtime choices

Kontext controls Pods; the container controls agent behavior. Choose the
smallest integration that supplies the result semantics you need.

## Three runtime roles

- The echo image is the deterministic control-plane conformance oracle. It
  proves Pod lifecycle, status projection, and Service recast without a
  provider. During the v1alpha1 transition it still writes the accepted legacy
  `{result,tokensUsed,dollarsUsed}` termination payload.
- `runtimes/reference` is the maintained model-backed Go runtime. Its fake
  provider is keyless; its Anthropic and OpenAI-compatible transports require
  Secrets. It emits versioned events and a
  `kontext.dev/result/v1alpha1` envelope.
- Bring-your-own images are first-class. Kontext does not require the
  maintained runtime.

## Result capture

An image can use one of four paths:

1. Run unchanged with ordinary logs and no structured result.
2. Request injected `Stdout`/`LastLine` capture for an explicit command.
3. Request injected `Stdout`/`KontextEnvelope` capture when the child emits a
   prefixed versioned envelope.
4. Write a native versioned envelope to `/dev/termination-log`; omit
   `runtime.result` so no control-plane reporter is injected.

Last-line capture is heuristic and cannot infer usage. Envelope capture
preserves typed output, usage, timing, and execution metadata. The termination
message is limited to 4096 bytes, so it is a summary rather than a transcript;
keep full events and diagnostics outside Kubernetes status. The Pod termination
message and its `AgentRun.status` projection are authoritative for terminal
output; raw logs are only the event/diagnostic stream.

See the
[v1alpha1 example manifests](https://github.com/MFS-code/Kontext/tree/main/deploy/examples/v1alpha1)
for one manifest for each path.

## Context and tools

`knowledgeConfigMapRef` mounts static ConfigMap data at
`/kontext/knowledge`. This is useful for small, versioned instructions and
deterministic fixtures. It is not production RAG: there is no ingestion,
embedding, ranking, freshness policy, authorization-aware retrieval, or
source-quality measurement.

Putting a tool in `spec.tools` makes it available to the runtime/model. It does
not guarantee a model will call it. Tool events are the execution evidence:
inspect the tool name, count, error flag, stable error code, timing, and
truncation metadata. Kubernetes RBAC, ServiceAccounts, mounts, container
security, and enforced NetworkPolicy remain authoritative even when the
runtime allows a tool.

## Task and Scheduled workloads

Task invocations and Scheduled slots both produce one-shot `AgentRun`s. Their
images should perform the goal, emit any terminal result, and exit. Kontext
sets the Pod restart policy to `Never`; it does not restart a failed process
inside the same run.

A Task `Agent` is a reusable template. Admission resolves each sparse,
user-named invocation into a complete immutable run before storage. A
Scheduled `Agent` builds the same complete one-shot snapshot in the
controller, using a slot-derived run name. Runtime images receive a concrete
`KONTEXT_GOAL` in both cases and do not need to know which path created the
run.

## Service workloads

A Service image must stay alive. Omit `budget.wallclock` when the service has
no intended lifetime; the controller then does not apply a wallclock deadline.
If its Pod exits or is deleted, the Service controller creates a fresh
`AgentRun` with backoff. `echo-service-agent.yaml` is the keyless example.

Versioned image publication is release work, separate from this runtime
contract and its source-level acceptance tests.
