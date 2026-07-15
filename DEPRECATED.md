# Deprecated hackathon stack

The original Phase 1 hackathon build (Python `kopf` operator, fake runtime, TUI,
`kontext.dev/v1` single-`Agent` CRD) is preserved on:

```text
branch: deprecated/hackathon-python
```

That branch contains the full historical tree including:

- `src/kontext_operator/` — kopf controller
- `src/kontext_tui/` — terminal UI
- `src/kontext_runtime/` — fake + Anthropic runners
- `deploy/crds/agents.kontext.dev.yaml` — v1 CRD
- `deploy/install.yaml` — Python controller Deployment
- `deploy/examples/research-agent.yaml` and related v1 examples
- `demo/fixtures/` — canned demo data
- `PLAN.md`, `PHASES.md`, `TUI_PLAN.md`, `CONCEPT.md`

**Do not install the hackathon stack on a cluster that runs the Go v1alpha1
operator.** Both used the CRD name `agents.kontext.dev` with incompatible schemas.

To explore the old stack locally:

```bash
git checkout deprecated/hackathon-python
python3 -m pip install -e .
./scripts/install-kind.sh   # uses the old Dockerfile + Python controller path
```

Mainline development uses the Go operator only. See `README.md`.
