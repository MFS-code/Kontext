# python-anthropic runtime

A Kontext runtime image that fulfills one `AgentRun` by sending the goal to the
Anthropic Messages API and reporting the result through the runtime contract
(see `SPEC.md` at the repo root).

Contract surface used:

- Reads `KONTEXT_GOAL`, `KONTEXT_MODEL`, `KONTEXT_PROVIDER`, `KONTEXT_TOOLS`,
  `KONTEXT_BUDGET_TOKENS`, `KONTEXT_BUDGET_WALLCLOCK`, `KONTEXT_AGENT_NAME`.
- Expects `ANTHROPIC_API_KEY` injected from the Agent's `secretRef`.
- Streams progress to stdout (`kubectl logs -f`).
- Writes `{result, tokensUsed, dollarsUsed}` JSON to `/dev/termination-log`,
  which the controller parses into `AgentRun.status`.

Build:

```bash
docker build -t kontext-runtime-anthropic:dev runtimes/python-anthropic
```
