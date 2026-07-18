# v1alpha1 examples

These manifests assume the Kontext CRDs and controller are installed. The kind
quickstart builds and loads the local `kontext-*:dev` images; provider examples
additionally require the named
namespace-local Secret. Apply namespaced manifests with `kubectl apply -f`.

## Bring your own image

| Path | Manifest | Prerequisite | Expected observation |
|---|---|---|---|
| Plain image, logs only | `plain-logs-run.yaml` | `kontext-stdout-fixture:dev` | `Succeeded`; ordinary `kubectl logs`; empty `status.result` and no `status.output`. No reporter is injected. |
| Injected final-line capture | `stdout-last-line-run.yaml` | Fixture and configured `KONTEXT_REPORTER_IMAGE` | `Succeeded`; logs remain available; `status.result` is `final answer from busybox`. |
| Injected structured capture | `stdout-envelope-run.yaml` | Fixture and configured reporter image | `Succeeded`; the prefixed envelope supplies JSON output and measured usage. |
| Native versioned envelope | `native-envelope-run.yaml` | Fixture image | `Succeeded`; the process writes `kontext.dev/result/v1alpha1` to `/dev/termination-log` itself. `runtime.result` is omitted, so the controller does not inject a reporter. |

`stdout-failure-run.yaml` and `stdout-signal-run.yaml` demonstrate that injected
capture preserves a child exit code and forwards termination. A plain image is
still a valid workload when it only needs lifecycle and logs; structured
results are opt-in.

## Runtime and tool examples

- `reference-fake-run.yaml` and `reference-fake-tool-run.yaml` are deterministic
  keyless reference-runtime paths.
- `reference-tools-run.yaml` demonstrates built-in tools. A listed tool is
  available to the model, not guaranteed to be called.
- `reference-kind-policy-runs.yaml` exercises runtime allowlists, namespace
  RBAC, restricted Pod security, and wallclock cleanup.
- `reference-playwright-*.yaml` exercises a separately deployed MCP server;
  the Calico-backed script is required before treating NetworkPolicy behavior
  as evidence.
- `reference-anthropic-run.yaml` and
  `reference-openai-compatible-run.yaml` require provider credentials created
  from `provider-secrets.example.yaml` or an equivalent Secret.

`knowledgeConfigMapRef` mounts static, versioned context at
`/kontext/knowledge`. It is useful for small fixtures and operating
instructions; it is not production retrieval or RAG.

## Service mode

`echo-service-agent.yaml` is intentionally long-running. It omits
`spec.budget.wallclock`, so the controller does not set a wallclock deadline.
The Pod remains `Running`; deleting it causes the Service controller to mint a
fresh `AgentRun` with backoff. `scripts/eval-kind.sh` checks both the omitted
deadline and recast behavior.
