# Alpha operations and support

This document is the operational contract for the Kontext public alpha. It
defines what the project supports, where Kubernetes remains responsible, and
which failures users must diagnose in their own cluster.

## Supported behavior

Kontext maintains and tests the following paths:

- A standalone `AgentRun` creates one Pod, tracks its phase, and stores a
  bounded terminal result and measured usage in status.
- An `Agent` in `Service` mode keeps one active child `AgentRun`. When that run
  ends or its Pod disappears, the controller creates a new run with backoff.
- Runtime images may write a versioned termination envelope directly or opt
  into `Stdout` result capture with an explicit command.
- `budget.wallclock` is validated at admission and enforced by the controller.
- Release images support `linux/amd64` and `linux/arm64`.
- The maintained Go reference runtime supports its documented fake, Anthropic,
  OpenAI, and OpenAI-compatible paths and bounded tool loop.

The following fields exist in the alpha API but do not have controller
behavior:

- `Agent` mode `Task`
- `Agent` mode `Scheduled`
- `spec.schedule`

The controller accepts these modes and reports an `UnsupportedMode` condition.
It does not create an `AgentRun` for them. Callers that need one-shot work must
create an `AgentRun` directly.

Token and dollar budgets are not enforcement controls. Runtimes may report
those measurements, and Kontext records them in status. Only wallclock is
authoritatively enforced by the controller.

Kontext does not yet publish a formal Kubernetes version compatibility range.
CI uses Kubernetes 1.32 envtest binaries and disposable kind clusters. Other
distributions and versions may work, but the alpha does not certify them.

## Installation assumptions

Installing the release manifest requires permission to create:

- CustomResourceDefinitions
- a Namespace and namespaced controller resources
- a ClusterRole and ClusterRoleBinding

Every cluster node that runs the controller or a workload must be able to pull
the referenced image for its architecture. Release manifests pin the operator
and reporter by digest. Runtime image selection remains part of each `Agent` or
`AgentRun` specification.

Local `:dev` images and `kind load` belong only to the development workflow.
They are not part of the supported installation path.

## Security and responsibility boundaries

### Operator identity

The controller runs as the `controller-manager` ServiceAccount in the
`kontext-system` Namespace. The installed `manager-role` ClusterRole permits
it to:

- create and patch Events;
- create, read, watch, update, patch, and delete Pods;
- create, read, watch, update, patch, and delete `Agent` and `AgentRun`
  resources;
- read and update `Agent` and `AgentRun` status.

These permissions are cluster-wide because Kontext reconciles namespaced
resources in every namespace. Runtime Pods do not inherit the controller's
ServiceAccount or RBAC permissions.

### Workload identity

`spec.serviceAccountName` selects an existing ServiceAccount in the workload's
namespace. Kontext neither creates that ServiceAccount nor grants it RBAC
permissions.

If the field is omitted, Kubernetes assigns the namespace's `default`
ServiceAccount. Kubernetes normally mounts that ServiceAccount token unless
the Pod or ServiceAccount disables automounting. Omission therefore does not
mean "no Kubernetes identity."

Use a dedicated ServiceAccount and verify its effective permissions:

```bash
kubectl auth can-i list pods \
  --as=system:serviceaccount:<namespace>:<service-account> \
  -n <namespace>
```

Runtime tool allowlists do not override RBAC. A listed Kubernetes tool still
fails if the workload ServiceAccount lacks permission.

### Provider credentials and configuration

Provider credentials come from a Kubernetes Secret in the same namespace as
the `AgentRun`. `spec.secretRef.name` selects that Secret. If it is omitted,
the controller uses the provider's documented default name, such as
`kontext-anthropic` or `kontext-openai`.

The operator writes `secretKeyRef` entries into the Pod specification. It does
not read or copy Secret values. Kubernetes resolves the references when it
starts the Pod. A missing Secret or key normally produces
`CreateContainerConfigError`.

`knowledgeConfigMapRef` follows the same namespace rule. Kubernetes mounts it
read-only at `/kontext/knowledge`. A missing ConfigMap produces a volume mount
failure before runtime code starts.

Do not put credentials in `spec.env`, manifests, logs, status, issue reports,
or evaluation artifacts.

### Network access

Kontext does not create a default NetworkPolicy for workloads. Runtime Pods
have whatever ingress and egress the cluster, namespace, and CNI allow. On many
clusters that means unrestricted egress.

Tool declarations do not restrict network traffic. Apply an enforced
NetworkPolicy when a runtime must reach only DNS, a provider endpoint, the
Kubernetes API, or a named in-cluster service. kindnet does not enforce
NetworkPolicy, so Kontext's policy acceptance uses Calico.

### Runtime containers

Kontext exposes only security-context fields that restrict a container. It
does not add a secure runtime profile by default. A runtime without an
explicit security context receives normal Kubernetes and image defaults.

Stdout result capture uses a trusted reporter init container. That init
container runs as UID 0 only long enough to copy the reporter binary into an
empty volume. It drops capabilities, disables privilege escalation, and uses a
read-only root filesystem. The workload container keeps its configured
identity and security context.

Any runtime can perform actions allowed by its ServiceAccount, network access,
mounted data, and container permissions. Kontext does not inspect or sandbox
application logic.

## Budgets, results, and interruption

The controller treats `budget.wallclock` as authoritative. When time expires,
it marks the run `BudgetExceeded` and deletes the Pod. Omitting the field means
there is no controller wallclock deadline.

Token and dollar values depend on runtime and provider reporting. They may be
missing, incomplete, or provider-specific.

`kubectl logs` is the detailed operational stream. `AgentRun.status` is a
bounded terminal summary. Kubernetes limits termination messages to 4096
bytes, so Kontext may compact output and record truncation metadata. Store
transcripts and large artifacts outside Kubernetes status.

Node loss, eviction, controller restart, and Pod deletion can interrupt work.
Service mode creates a fresh run after failure. It does not restore in-memory
conversation or retry side effects. External callers must decide whether
repeating one-shot work is safe.

## Resource lifecycle

- An `AgentRun` owns its Pod. Deleting the run removes the Pod through
  Kubernetes garbage collection.
- A Service `Agent` owns its child runs. Deleting the Agent removes those runs
  and their Pods.
- Completed run history remains until a user or another controller deletes it.
- Removing only the controller retains the CRDs and custom resources in other
  namespaces.
- Deleting either CRD deletes every resource of that kind across the cluster.

To remove the controller while retaining CRDs and resources outside
`kontext-system`:

```bash
kubectl delete clusterrolebinding manager-rolebinding \
  --ignore-not-found=true
kubectl delete clusterrole manager-role \
  --ignore-not-found=true
kubectl delete namespace kontext-system \
  --ignore-not-found=true --wait=true
```

A complete uninstall deletes the release manifest:

```bash
kubectl delete -f <release-install-url> \
  --ignore-not-found=true --wait=true
```

The complete path deletes both CRDs and all `Agent` and `AgentRun` resources
stored under them. Back up those resources first. Read
[release and image versioning](releases.md) for the exact install URL and alpha
upgrade procedure.

## Troubleshooting

### Check the controller

```bash
kubectl get deployment,pods -n kontext-system
kubectl rollout status deployment/controller-manager \
  -n kontext-system --timeout=180s
kubectl logs deployment/controller-manager -n kontext-system
```

If the Deployment is unavailable, describe its Pod and inspect recent events:

```bash
kubectl describe pod -n kontext-system \
  -l control-plane=controller-manager
kubectl get events -n kontext-system --sort-by=.lastTimestamp
```

`ImagePullBackOff` usually means the image is private, missing, or unavailable
for the node architecture.

### Check the custom resource

```bash
kubectl get agent,agentrun -n <namespace> -o wide
kubectl get agentrun <name> -n <namespace> -o yaml
kubectl describe agentrun <name> -n <namespace>
```

Inspect `status.phase`, `status.message`, `status.conditions`, `status.podName`,
and the completion timestamps. For an `Agent`, inspect `Ready` and
`Progressing`. `UnsupportedMode` means the mode is reserved but not
implemented.

### Check the workload Pod

```bash
kubectl get pod -n <namespace> -o wide
kubectl describe pod <pod> -n <namespace>
kubectl get events -n <namespace> --sort-by=.lastTimestamp
kubectl logs <pod> -n <namespace> -c runtime
kubectl logs <pod> -n <namespace> -c runtime --previous
```

Common failures:

- `Pending` with scheduling events indicates resource, quota, affinity, or
  admission problems.
- `ImagePullBackOff` indicates image name, registry access, or architecture
  problems.
- `CreateContainerConfigError` commonly indicates a missing Secret or key.
- `FailedMount` commonly indicates a missing ConfigMap.
- A failed reporter init container indicates that the configured reporter
  image could not start or copy its binary.
- A terminated runtime with no structured output may be valid when result
  capture was not configured.

### Check dependencies and identity

```bash
kubectl get secret <name> -n <namespace>
kubectl get configmap <name> -n <namespace>
kubectl get serviceaccount <name> -n <namespace> -o yaml
kubectl auth can-i --list \
  --as=system:serviceaccount:<namespace>:<service-account> \
  -n <namespace>
```

Do not print Secret values while collecting diagnostics.

### Check network policy

```bash
kubectl get networkpolicy -n <namespace>
kubectl describe networkpolicy <name> -n <namespace>
```

Confirm that the cluster CNI enforces policy. Then check DNS, provider endpoint
allowlists, proxies, certificates, and Kubernetes API egress. A runtime-level
timeout does not prove that Kontext or the provider failed. The network may
have dropped the request.

### Check result capture

For native termination results, inspect the runtime container's terminated
state:

```bash
kubectl get pod <pod> -n <namespace> -o yaml
```

For stdout capture, confirm that `runtime.command` is present and that the
controller Deployment has a non-empty `KONTEXT_REPORTER_IMAGE`. Runtime logs
remain available even when no structured result is produced.

## Compatibility

`kontext.dev/v1alpha1` is unstable. A later alpha release may change CRD fields,
defaults, status shape, result contracts, or runtime behavior without changing
the Kubernetes API version. Read release notes and follow the documented
upgrade procedure. Downgrades are not supported.

Kontext has no availability or response-time SLA during alpha.

For non-sensitive help, read [SUPPORT.md](../SUPPORT.md) and open a GitHub
issue. Report security problems through the private process in
[SECURITY.md](../SECURITY.md).
