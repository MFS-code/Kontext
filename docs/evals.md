---
title: Evaluations
description: How kontext-eval grades AgentRuns outside the agent Pod.
sidebarTitle: Evaluations
---

# Evaluations

Evaluations run outside agent Pods. `kontext-eval` creates ordinary
`AgentRun`s, waits for terminal status, collects only the artifacts required
by configured graders, writes bounded JSONL records, and removes the runs it
created.

## Deterministic first

Deterministic graders always run before an optional model judge. They can
check terminal phase, status output, measured usage presence, versioned
envelope fields, event/tool counts, duration, and process exit code. A judge
cannot turn a failed deterministic grade into a pass; judge errors also fail
the case.

Artifact requirements follow the grader:

- phase and duration graders can pass after a controller has deleted the Pod;
- result, output, and usage are read from `AgentRun` status only when requested;
- envelope graders parse the runtime container's termination message;
- event graders read a bounded runtime-log tail;
- exit-code graders require the terminated runtime Pod.

This is why the wallclock case can grade `BudgetExceeded` after Pod cleanup,
while a missing tool event or result envelope remains a collection failure.

## Comparing cases

Represent a comparison as explicit cases with the same goal and controlled
configuration. Change one variable, such as the opaque model ID, and grade the
same stable output plus each case's `execution.model`. Do not infer model
quality from one changing live response. First repeat the same prompt and model
to estimate ordinary variance; then compare another model with the same prompt,
limits, tools, context, and grader. Retain normalized usage and duration, and
compare against a deterministic Job/script baseline for the same task.

The keyless suite at `evals/suites/keyless.yaml` covers:

- one goal through two opaque fake model IDs;
- tool used once and tool available but unused;
- namespace-RBAC denial;
- empty provider credential after Pod startup;
- unreachable provider endpoint;
- controller wallclock cancellation and Pod cleanup;
- malformed provider response;
- reporter-preserved process crash.

Run it against the already-installed local kind cluster:

```bash
KONTEXT_EVAL_DIR="$PWD/eval-results/kind" ./scripts/eval-kind.sh
```

The script also proves the no-wallclock Service example remains Running and
recasts after Pod deletion. Pull-request CI uses no provider credentials or
external provider endpoint.

## Records and privacy

Eval records use `kontext.dev/eval/v1alpha1`. They may contain status/model
output only when a grader explicitly requests it. They do not retain
`status.message`; failure evidence is the phase, projected envelope error code,
grades, and collection errors. Envelope records are projections limited to the
requested outcome, error code, model, turn count, or tool-call count. The runner
never automatically reads Pod environments, Secret values, raw logs, artifacts,
extensions, request IDs, or private reasoning. Optional usage retains missing
versus measured-zero semantics.

The dispatch-only provider workflow writes a separate bounded
`ProviderAcceptanceRecord` with provider, opaque model, scenario, commit/run
identity, phase, duration, measured usage, turns, tool calls, and pass/fail.
It initializes a `NotStarted` failed record before image or cluster work and
atomically replaces it when the acceptance script runs, so infrastructure
failures still leave non-secret evidence. Its short-lived artifact is the
acceptance record; failure diagnostics remain redacted separately.

Keyless CI similarly creates a small `EvalRunMarker` before image builds. A
successful `scripts/eval-kind.sh` removes the marker after writing and
validating JSONL/summary output; an earlier failure leaves the marker visible
in the always-uploaded artifact.

## Protected pre-alpha acceptance

Before an alpha release, a maintainer must dispatch `.github/workflows/provider-acceptance.yml`
against the protected `provider-acceptance` environment for each maintained
transport being released. Select a small supported model, review endpoint and
pricing, approve the environment, and require the workflow to pass. Download
the `provider-acceptance-<run>-<attempt>` artifact and retain
`acceptance.json` with the release evidence.

A local render or keyless run is not an authenticated provider acceptance.
Without the protected credentials, record that the dispatch remains pending;
do not claim a live run happened.
