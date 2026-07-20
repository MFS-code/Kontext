# External evaluations

`kontext-eval` runs evaluation cases from outside the workload. It creates
ordinary typed `AgentRun` resources, waits for terminal status, collects the
runtime Pod's logs and exit code while the Pod exists, grades the observation,
and then deletes only the runs it created.

Build and inspect the CLI:

```sh
make build-eval
./bin/kontext-eval -help
```

Run a suite:

```sh
./bin/kontext-eval \
  --suite evals/suites/keyless.yaml \
  --records tmp/keyless.jsonl \
  --summary tmp/keyless-summary.json
```

`--kubeconfig` selects an explicit file; otherwise the standard client-go
loading rules apply. `KONTEXT_EVAL_RUNTIME_IMAGE` is the environment equivalent
of `--runtime-image`. The flag wins when both are set. Runs are removed after
collection unless `--keep-runs` is set.

## Suite schema

Suites are strict YAML. Unknown fields and invalid grader values are rejected.
The runtime image may come from the case, suite defaults, or the CLI override.
Model comparisons are represented as explicit cases with the same goal and
different models; there is no implicit matrix expansion.

```yaml
apiVersion: kontext.dev/eval/v1alpha1
kind: EvalSuite
metadata:
  name: keyless
spec:
  defaults:
    namespace: kontext-system
    timeout: 2m
    runtimeImage: kontext-reference:dev
  assertions:
    - type: fieldsEqual
      records: [fake-success-model-a, fake-success-model-b]
      fields: [statusResult]
    - type: forbiddenMarkers
      fields: [statusResult, statusOutput, collectionErrors]
      markers: [credential-placeholder]
  cases:
    - id: fake-success-model-a
      description: Deterministic reference-runtime success
      agentRun:
        goal: Return the expected answer
        provider: fake
        model: model-a
        runtime: {}
      graders:
        - type: terminalPhase
          phase: Succeeded
        - type: statusResult
          statusResult:
            contains: expected
        - type: structuredOutput
          structuredOutput:
            present: true
            valid: true
            mediaType: text/plain
        - type: usageFields
          usageFields: [tokens, inputTokens, outputTokens]
        - type: envelopeOutcome
          outcome: Succeeded
        - type: executionModel
          model: model-a
        - type: eventCount
          event: {type: tool, count: 0}
        - type: duration
          maxDuration: 30s
        - type: podExitCode
          exitCode: 0
```

Other deterministic graders are `envelopeErrorCode`, `envelopeTurns`,
`envelopeToolCalls`, and `toolEvents`. A `toolEvents` expectation accepts
`name`, `count`, and optional `isError`, `errorCode`, and `truncated` filters.
Status-result matching supports exactly one of `exact`, `contains`, or
`notContains`.

Suite assertions run after all case records exist and are separate from
per-record graders. `fieldsEqual` compares each declared record field across at
least two named cases. `forbiddenMarkers` scans the declared fields on every
record, or only its optional `records` list. Supported fields are
`terminalPhase`, `statusResult`, `statusOutput`, `statusUsage`, `podExitCode`,
`envelope`, `events`, `grades`, `judge`, `collectionErrors`, and
`durationMillis`. Unknown assertion types, case IDs, and fields fail suite
validation.

The JSONL record contains only explicitly requested status/model output,
projected envelope fields, collection errors, and bounded event metadata rather
than raw logs or `status.message`. The runner never automatically reads Pod
environments or Secret values. Optional usage fields retain the distinction
between missing and measured zero. A summary JSON file reports expected and
actual case totals, collection-error counts and affected cases, each suite
assertion result, an overall pass flag, and the record path.

Artifact collection remains least-privilege. Suite assertions can request
`statusResult`, `statusOutput`, `statusUsage`, or `podExitCode` for their
targeted records. Their `envelope` and `events` fields scan only projections
already requested by per-record graders; they never trigger full envelope or
raw-log capture. Phase and duration graders do not require a Pod that the
wallclock controller has already deleted. Envelope graders parse the runtime
container's authoritative termination message, event graders read only a
bounded log tail, and exit-code graders require a terminated Pod. Missing or
incomplete required artifacts fail the case.

`scripts/eval-kind.sh` runs the complete keyless suite against the existing
kind cluster, validates the records, checks controller timeout cleanup, and
separately proves the no-wallclock Service example remains Running and
recasts. See [`docs/evals.md`](docs/evals.md) for comparison guidance,
privacy boundaries, and protected provider acceptance for releases.

## Optional judge

`--judge-command` runs only after deterministic grading. The command receives a
bounded observation on stdin and must return:

```json
{"pass":true,"score":0.9,"rationale":"brief explanation"}
```

The judge process receives a minimal environment and no kubeconfig, Pod
environment, Secret values, raw logs, or private reasoning. A command failure,
invalid response, or failing judge result is recorded separately and makes the
CLI exit non-zero. Without the option, no model judge runs.
