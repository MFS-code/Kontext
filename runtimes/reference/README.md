# Kontext reference runtime

The maintained reference runtime is a small provider-neutral execution loop.
It demonstrates how a model-backed agent behaves as a Kubernetes workload
without adopting an agent framework.

Issue #17 ships only the deterministic `fake` provider. Anthropic and
OpenAI-compatible HTTP adapters are added separately.

## Execution model

The image bundles two static binaries:

```text
kontext-reporter (PID 1)
└─ kontext-reference
```

The runtime emits versioned JSONL lifecycle, usage, output, and error events to
stdout. Its final line is a `KONTEXT_RESULT:`-prefixed result envelope. The
reporter preserves the logs and process exit status, compacts the envelope, and
writes `/dev/termination-log`.

No private chain-of-thought is emitted.

## Configuration

| Environment variable | Required | Meaning |
|---|---|---|
| `KONTEXT_GOAL` | yes | Concrete task for this run |
| `KONTEXT_PROVIDER` | yes | Provider adapter name; currently `fake` |
| `KONTEXT_MODEL` | yes | Opaque provider model identifier |
| `KONTEXT_RUN_NAME` | no | Run metadata; defaults to `unknown-run` |
| `KONTEXT_AGENT_NAME` | no | Agent metadata; defaults to run name |
| `KONTEXT_TOOLS` | no | Declared allowlist; recorded but not executed yet |
| `KONTEXT_BUDGET_TOKENS` | no | Positive requested output/token limit |
| `KONTEXT_BUDGET_WALLCLOCK` | no | Optional Go duration; omitted means disabled |
| `KONTEXT_BUDGET_DOLLARS` | no | Optional non-negative recorded budget |
| `KONTEXT_PROVIDER_ENDPOINT` | no | Absolute HTTP(S) endpoint for future adapters |
| `KONTEXT_FAKE_SCENARIO` | no | `success`, `failure`, `malformed`, or `delay` |
| `KONTEXT_FAKE_DELAY` | delay only | Positive Go duration such as `250ms` |

There is deliberately no hidden five-minute deadline. The runtime creates a
timeout only when `KONTEXT_BUDGET_WALLCLOCK` is present.

Model identifiers pass through unchanged.

## Local fake-provider run

```bash
KONTEXT_GOAL="Explain the runtime contract" \
KONTEXT_PROVIDER=fake \
KONTEXT_MODEL="any-opaque-model-id" \
go run ./runtimes/reference
```

Build the production image:

```bash
make docker-build-reference
```

## Dependency inventory

The reference binary uses only the Go standard library and in-repository
Kontext packages. The bundled reporter additionally uses `golang.org/x/sys`
for Linux process supervision. The production image contains:

- `gcr.io/distroless/static:nonroot`
- `/kontext-reporter`
- `/kontext-reference`

It contains no shell, package manager, CA bundle requirement for the fake
provider, agent framework, retrieval system, or persistent storage.

## Explicit limits

- One provider completion per run.
- Conversation state exists only in memory for that run.
- Tools are declared but not executed.
- No retries, planning, memory, retrieval, subagents, or background work.
- No real provider credentials or network calls in issue #17.
