# Kontext reference runtime

The maintained reference runtime is a small provider-neutral execution loop.
It demonstrates how a model-backed agent behaves as a Kubernetes workload
without adopting an agent framework.

The runtime includes a deterministic `fake` provider plus direct Anthropic
Messages and OpenAI-compatible Chat Completions HTTP transports.

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
| `KONTEXT_PROVIDER` | yes | `fake`, `anthropic`, `openai`, or `openai-compatible` |
| `KONTEXT_MODEL` | yes | Opaque provider model identifier |
| `KONTEXT_RUN_NAME` | no | Run metadata; defaults to `unknown-run` |
| `KONTEXT_AGENT_NAME` | no | Agent metadata; defaults to run name |
| `KONTEXT_TOOLS` | no | Declared allowlist; recorded but not executed yet |
| `KONTEXT_BUDGET_TOKENS` | no | Positive requested output/token limit |
| `KONTEXT_BUDGET_WALLCLOCK` | no | Optional Go duration; omitted means disabled |
| `KONTEXT_BUDGET_DOLLARS` | no | Optional non-negative recorded budget |
| `KONTEXT_PROVIDER_ENDPOINT` | no | Exact absolute HTTP(S) request endpoint |
| `KONTEXT_PROVIDER_BASE_URL` | no | Absolute HTTP(S) base URL; provider path is appended |
| `ANTHROPIC_API_KEY` | Anthropic only | Anthropic API key, normally injected from a Secret |
| `OPENAI_API_KEY` | OpenAI-compatible only | Bearer token, normally injected from a Secret |
| `KONTEXT_FAKE_SCENARIO` | no | `success`, `failure`, `malformed`, or `delay` |
| `KONTEXT_FAKE_DELAY` | delay only | Positive Go duration such as `250ms` |

There is deliberately no hidden five-minute deadline. The Kubernetes
controller remains authoritative for `KONTEXT_BUDGET_WALLCLOCK`; the runtime
parses the value but does not start a competing timer. It reacts promptly when
the reporter forwards controller cancellation signals.

Model identifiers pass through unchanged.

`KONTEXT_PROVIDER_ENDPOINT` and `KONTEXT_PROVIDER_BASE_URL` are mutually
exclusive. A base URL may contain a path prefix. The runtime appends
`/v1/messages` for Anthropic or `/chat/completions` for OpenAI-compatible
providers. An exact endpoint is used without modification.

## Maintained HTTP compatibility

### Anthropic

- Sends one non-streaming `POST` to the Messages API with
  `anthropic-version: 2023-06-01`.
- Defaults to `https://api.anthropic.com/v1/messages`.
- Sends `KONTEXT_BUDGET_TOKENS` as `max_tokens`. Because Anthropic requires
  that field, the transport uses `4096` when the budget is omitted.
- Normalizes text and client `tool_use` blocks; current documented stop
  reasons; input/output token usage; and the `request-id` header.

### OpenAI-compatible

- `openai` and `openai-compatible` use the same transport.
- Sends one non-streaming `POST` to the Chat Completions API and defaults to
  `https://api.openai.com/v1/chat/completions`.
- Treats the base URL as the OpenAI API root (normally ending in `/v1`) and
  appends `/chat/completions`.
- Sends `model`, text `messages`, and optional `max_completion_tokens`.
- Normalizes assistant text, function `tool_calls`, documented finish reasons,
  optional prompt/completion/total usage, optional
  `completion_tokens_details.reasoning_tokens`, and the `x-request-id` header.

OpenAI completion totals can exceed the tokenized visible text because they
may include hidden reasoning tokens. When the API reports that breakdown, the
runtime preserves it as optional `usage.reasoningTokens` in the usage event
and final versioned result envelope. It never estimates the value when the
detail is absent, and a reported zero remains distinct from absence.

`OpenAI-compatible` means compatibility with that request, response, auth, and
tool-call shape. It does not include the Responses API, streaming, embeddings,
realtime transports, Azure deployment URL construction, or vendor-specific
extensions. Endpoint operators are responsible for accepting a bearer token
in `Authorization`; a non-secret placeholder may be used only when an
in-cluster endpoint ignores auth.

The runtime currently does not send declared tools or execute returned tool
calls. Tool calls are normalized so the transport boundary remains explicit,
but this one-turn runtime is intended for text tasks until the bounded tool
loop is implemented.

HTTP failures use stable result error codes including
`authentication_failed`, `rate_limited`, `provider_timeout`,
`provider_network_error`, `provider_endpoint_error`,
`provider_request_rejected`, and `invalid_provider_response`. Retryability,
HTTP status, and provider request IDs are retained where available. Response
bodies are bounded to 4 MiB.

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

## Kubernetes credentials and examples

The controller reads `spec.secretRef` and injects only the maintained
transport's expected environment variable. Create Secrets without putting
credentials in source control:

```bash
kubectl create secret generic kontext-anthropic \
  --from-literal=ANTHROPIC_API_KEY="$ANTHROPIC_API_KEY"

kubectl create secret generic kontext-openai \
  --from-literal=OPENAI_API_KEY="$OPENAI_API_KEY"
```

Example templates live in `deploy/examples/v1alpha1/`:

- `provider-secrets.example.yaml`
- `reference-anthropic-run.yaml`
- `reference-openai-compatible-run.yaml`

The `Provider acceptance` GitHub Actions workflow is dispatch-only and uses
the `provider-acceptance` environment. Repository maintainers must protect
that environment with required reviewers and store `ANTHROPIC_API_KEY` and
`OPENAI_API_KEY` as environment secrets. Pull-request CI never receives or
uses these credentials.

## Dependency inventory

The reference binary uses only the Go standard library and in-repository
Kontext packages. The bundled reporter additionally uses `golang.org/x/sys`
for Linux process supervision. The production image contains:

- `gcr.io/distroless/static:nonroot`
- `/kontext-reporter`
- `/kontext-reference`

It contains no shell, package manager, agent framework, retrieval system, or
persistent storage.

## Explicit limits

- One provider completion per run.
- Conversation state exists only in memory for that run.
- Tools are declared but not executed.
- No retries, planning, memory, retrieval, subagents, background work, or
  provider-specific model aliases.
