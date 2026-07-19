# Support

Kontext is a public alpha. It has no uptime, compatibility, or response-time
SLA.

## Before asking for help

Read:

- [alpha operations and support](docs/operations.md);
- [release and image versioning](docs/releases.md);
- [the API and runtime contract](SPEC.md).

Search existing GitHub issues before opening a new one.

## What the project supports

Bug reports are in scope when they reproduce against:

- the latest published alpha release;
- release-provided operator or reporter images;
- the maintained reference and echo runtimes;
- standalone `AgentRun` or `Agent` mode `Service`;
- the documented install and upgrade paths.

The project can help identify whether a bring-your-own runtime follows the
Kontext contract. Debugging application logic inside an arbitrary runtime is
the runtime author's responsibility.

Kubernetes distribution behavior, custom CNI configuration, admission
policies, cloud IAM, provider availability, quotas, and user-defined RBAC may
require help from the corresponding platform owner.

## Opening a useful bug report

Use the bug report form and include:

- Kontext release tag;
- Kubernetes distribution and version;
- node architecture;
- installation method;
- resource kind and mode;
- sanitized manifest;
- relevant conditions, events, and logs;
- exact expected and observed behavior.

Redact Secret values, authorization headers, provider responses with private
content, internal hostnames, and account identifiers.

Feature requests should describe the user problem and explain why the behavior
belongs in a general Kubernetes control plane rather than one runtime or
consumer.

## Security reports

Do not open a public issue for a suspected vulnerability. Follow
[SECURITY.md](SECURITY.md) and use GitHub private vulnerability reporting.
