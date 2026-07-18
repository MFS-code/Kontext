# Legacy python-anthropic behavior oracle

This image is retained as a legacy behavior oracle. It is not the maintained
primary runtime; new provider work and release acceptance target the Go
`runtimes/reference` image.

It fulfills one `AgentRun` by sending the goal to the Anthropic Messages API
and reporting through the accepted transition contract (see `SPEC.md`).

Contract surface used:

- Reads `KONTEXT_GOAL`, `KONTEXT_MODEL`, `KONTEXT_PROVIDER`, `KONTEXT_TOOLS`,
  `KONTEXT_BUDGET_TOKENS`, `KONTEXT_BUDGET_WALLCLOCK`, `KONTEXT_AGENT_NAME`.
- Expects `ANTHROPIC_API_KEY` injected from the Agent's `secretRef`.
- Streams progress to stdout (`kubectl logs -f`).
- Writes the legacy `{result, tokensUsed, dollarsUsed}` JSON payload to
  `/dev/termination-log`, which the v1alpha1 controller still parses into
  `AgentRun.status`.

Build:

```bash
docker build -t kontext-runtime-anthropic:dev runtimes/python-anthropic
```
