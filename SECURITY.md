# Security policy

Kontext is alpha software. It has not received an independent security audit
and should not be treated as a hardened multi-tenant sandbox.

## Supported versions

Security fixes target the latest published alpha release, currently
`v0.1.0-alpha.1`. The `main` branch is development code, not a supported
distribution. Older alpha releases may not receive patches.

## Reporting a vulnerability

Use GitHub's
[private vulnerability reporting form](https://github.com/MFS-code/Kontext/security/advisories/new).

Do not open a public issue for:

- privilege escalation or RBAC bypass;
- Secret or credential exposure;
- container escape or cross-namespace access;
- result, log, or status data leaks;
- release workflow or image provenance problems;
- denial-of-service paths with practical impact.

Include the affected version or commit, prerequisites, reproduction steps,
impact, and any suggested mitigation. Remove live credentials and unrelated
private data.

There is no guaranteed response time during alpha. The project reviews reports
privately before publishing disclosure or remediation details.

## Security boundaries

Read [the alpha operations contract](docs/operations.md) before deploying
Kontext. In particular:

- the operator has cluster-wide Pod and custom-resource permissions;
- runtime Pods use their own Kubernetes ServiceAccount;
- the namespace's default ServiceAccount is used when none is specified;
- Kontext does not install a default NetworkPolicy;
- runtime images execute application-controlled code;
- token and dollar budgets are reported, not enforced;
- deleting the CRDs deletes all `Agent` and `AgentRun` resources.

Kontext does not make an untrusted runtime safe. Cluster operators remain
responsible for admission policy, workload identity, Pod security, network
policy, node isolation, and Secret management.
