# Operations and failure boundaries

An `AgentRun` depends on more than its prompt. Treat these dependencies as
part of the workload specification and evaluation environment.

## Images and startup

- Every node must be able to pull the runtime image, and reporter injection
  additionally requires the operator-configured reporter image.
- Pin production images by immutable digest and publish versioned images as a
  separate release step. Local `:dev` tags are only for kind.
- A missing image, incompatible architecture, invalid command, failed init
  container, eviction, or unschedulable Pod can leave a run Pending or Failed
  before runtime code starts.
- Plain images keep logs but have no structured output unless they write a
  termination payload or opt into reporter capture.

## Identity, secrets, and network

- Provider credentials are namespace-local Kubernetes Secrets. An absent
  Secret/key prevents Pod startup; an existing empty key lets the Pod start
  and the reference runtime reports `missing_provider_credentials`.
- Use a dedicated ServiceAccount. Runtime tool allowlists do not bypass RBAC;
  `kubernetes_read` can still return `kubernetes_rbac_denied`.
- NetworkPolicy behavior depends on an enforcing CNI. kindnet does not enforce
  policy, so the policy acceptance installs Calico. DNS, provider endpoints,
  proxies, certificates, and cloud-metadata routes are separate dependencies.
- Provider outages, authentication changes, quotas, and rate limits normalize
  to stable codes where possible. Provider messages and HTTP behavior can
  still change independently of Kontext.

## Budgets, retries, and interruption

The controller is authoritative for `budget.wallclock`: after expiry it marks
the run `BudgetExceeded` and deletes the Pod. Omission means no controller
deadline. Token and dollar fields depend on runtime/provider measurements and
may be absent.

The reference runtime does not retry provider requests or failed tool calls.
That avoids duplicating side effects. MCP transport reconnection can prepare a
later call but never repeats the failed call. If an application adds retries,
bound them, use idempotency controls, and include repeated usage in budgets.

Node loss, eviction, controller restart, and Pod deletion can interrupt work.
Service mode recasts a fresh run; it does not restore in-memory conversation.
Task callers must decide whether a new run is safe.

## Logs, status, and retention

`kubectl logs` is the detailed operational stream. `AgentRun.status` is a
bounded terminal summary. Kubernetes termination messages have a 4096-byte
limit, so envelopes are compacted and may carry truncation metadata. Do not
put transcripts or large artifacts in status/etcd; store them externally and
place bounded references in the envelope.

Logs may disappear with Pod cleanup or retention. Collect them before deleting
a run when an event/envelope/exit grader requires them. Status-only evaluation
can remain valid after the wallclock controller removes the Pod. Redact and
bound diagnostics before sharing because provider/runtime errors can contain
workload-specific values.

Useful first checks:

```bash
kubectl get agentrun,pod -n <namespace> -o wide
kubectl describe agentrun <name> -n <namespace>
kubectl logs <pod> -n <namespace> -c runtime
kubectl get events -n <namespace> --sort-by=.lastTimestamp
```
