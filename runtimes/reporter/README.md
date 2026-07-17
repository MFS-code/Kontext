# Kontext result reporter

The reporter is a small process supervisor for agent containers. It runs as
PID 1, launches an explicit child command, preserves the child's logs and
process exit code, and writes the versioned Kontext result envelope to the
container termination log.

It contains no model, provider, tool, or Kubernetes control-plane behavior.

## Local usage

Use a temporary termination path outside Kubernetes:

```bash
go run ./runtimes/reporter \
  --format last-line \
  --termination-log /tmp/kontext-result.json \
  -- sh -c 'echo "working"; echo "final answer"'
```

The child output is still printed normally. The reporter writes a compact
`kontext.dev/result/v1alpha1` envelope to `/tmp/kontext-result.json`.

The command after `--` is required. The reporter does not discover an image
entrypoint.

## Result formats

### `last-line`

The last non-empty stdout line becomes `text/plain` output. Empty stdout
produces a successful envelope with no output. This mode is intentionally
heuristic and cannot infer usage metrics. A line larger than the capture limit
produces a successful envelope with an explicit truncated-output marker instead
of silently returning partial text.

### `kontext-envelope`

The child emits a complete versioned envelope on one stdout line:

```text
KONTEXT_RESULT: {"apiVersion":"kontext.dev/result/v1alpha1","outcome":"Succeeded","output":{"mediaType":"application/json","value":{"ok":true}}}
```

The line remains visible in ordinary logs. If multiple prefixed lines are
present, the last one wins. Missing, malformed, legacy, or capture-truncated
candidates produce a failed result envelope.

`RESULT:` is not a reporter prefix. The exact prefix is `KONTEXT_RESULT:`.

## Configuration

| Flag | Environment | Default |
|---|---|---|
| `--format` | `KONTEXT_RESULT_FORMAT` | `last-line` |
| `--termination-log` | `KONTEXT_TERMINATION_MESSAGE` | `/dev/termination-log` |
| `--max-capture-bytes` | — | 65536 |

The capture limit must be at least 4096 bytes. Logs themselves are not
buffered or limited by the reporter.

## Process behavior

- Child stdout and stderr are forwarded unchanged.
- SIGTERM and SIGINT are forwarded to the child process group.
- Remaining processes in the child process group are killed after the main
  child exits.
- Normal completion preserves the child exit code.
- Reporter infrastructure failures use exit code `125`.
- Failure to start the child uses exit code `126`.
- Reporter-generated failure envelopes use stable error codes so they remain
  distinguishable from agent process failures.

The production image is Linux-only and builds a static binary for the selected
`TARGETOS` and `TARGETARCH`.

## Development

```bash
go test ./runtimes/reporter -count=1
make docker-build-reporter
```

Reporter injection into arbitrary workload images is intentionally deferred to
the control plane. The internal `--install-to PATH` mode atomically copies the
running static executable into the shared injection volume; it is used by the
trusted init container rather than by workload authors.
