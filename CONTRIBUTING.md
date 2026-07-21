# Contributing to Kontext

Kontext is an alpha Kubernetes operator. Small, focused changes are easier to
review and safer to test than broad rewrites.

## Before opening an issue

- Use the issue forms for bugs and feature requests.
- Read [the alpha operations contract](docs/operations.md).
- Search existing issues and pull requests.
- Use private vulnerability reporting for security problems. Do not post
  credentials, private logs, or exploit details in a public issue.

## Development setup

Required tools:

- Go 1.26.5
- Node.js 22 for the hosted docs site
- Docker with Buildx
- kubectl
- kind for end-to-end tests
- a C/C++ toolchain is not required for the Go operator

Clone the repository, then run:

```bash
make verify
make test
make build
```

The core test suite uses envtest and does not require an existing cluster.

To exercise the full local workflow:

```bash
make kind-install
./scripts/e2e-kind.sh
./scripts/eval-kind.sh
```

Run the focused mode and admission acceptance scripts against the same
installed cluster:

```bash
./scripts/e2e-kind-task.sh
./scripts/e2e-kind-scheduled.sh
./scripts/e2e-kind-webhook.sh
```

The Task script covers sparse mutation, rejection classes, immutable
snapshots, concurrent invocations, retained status, and ownership. The
Scheduled script covers a real cron tick, status, `Forbid`, and ownership. The
webhook script covers fresh TLS bootstrap, fail-closed matching, complete-run
bypass, trust repair, renewal, restart reuse, and two-replica convergence.

NetworkPolicy acceptance creates a separate disposable kind cluster with
Calico:

```bash
./scripts/e2e-kind-network-policy.sh
```

For documentation changes, use Node.js 22 and run the same docs-site checks as
CI:

```bash
cd docs-site
npm ci
npm test
npm run typecheck
npm run build
npm run verify:build
```

The build synchronizes repository Markdown into the hosted site and emits raw
mirrors, `llms.txt`, and `llms-full.txt`. The verification checks byte
identity between source Markdown and raw output plus required Task, Scheduled,
API, and webhook terms in the generated corpus.

## Generated files

Changes under `api/v1alpha1` may require regenerated deep-copy code, CRDs, and
RBAC:

```bash
make manifests generate
make verify
```

Commit the generated output with the API change. Do not edit generated CRDs or
`zz_generated.deepcopy.go` by hand.

## Design rules

- Keep Kontext general. Consumer concepts such as repositories, pull requests,
  or code ownership belong in external orchestration or runtime images.
- Treat `SPEC.md` as the API and runtime-image contract.
- Keep operator behavior in the controller or Pod builder that owns it.
- Reject invalid immutable input at admission when Kubernetes validation can
  express the rule.
- Do not add provider-specific behavior to the control plane unless it is
  limited to generic credential wiring.
- Preserve ordinary Kubernetes debugging through resources, conditions, Pods,
  events, and logs.

Update `SPEC.md`, operations documentation, examples, and generated CRDs when a
change affects their contract.

## Pull requests

Open one pull request per issue or coherent change. Include:

- the problem and why the change belongs in Kontext;
- the important implementation choices;
- commands used to test the change;
- API, security, upgrade, or release implications;
- documentation changes or a reason none are needed.

Run the narrowest relevant test while developing, then run `make verify` and
`make test` before requesting review. Changes to images, installation, or
runtime behavior should also exercise the appropriate kind workflow.

Do not include Secrets, provider responses containing private data, generated
credentials, or unredacted cluster diagnostics.
