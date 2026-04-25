from __future__ import annotations

import os
import sys
import time


def _env(name: str, default: str) -> str:
    return os.environ.get(name, default).strip() or default


def main() -> int:
    agent_name = _env("KONTEXT_AGENT_NAME", "agent")
    goal = _env("KONTEXT_GOAL", "Think about the requested task.")
    model = _env("KONTEXT_MODEL", "fake-model")
    tools = [tool for tool in _env("KONTEXT_TOOLS", "web_fetch,python").split(",") if tool]

    steps = [
        f"> Agent {agent_name} accepted goal: {goal}",
        f"> Loading model profile: {model}",
        f"> Available tools: {', '.join(tools) or 'none'}",
        "> I need to break the request into a small research plan.",
        "> Calling tool: web_fetch(\"https://example.com/search?q=kontext\")",
        "> Got 5 candidate sources. Filtering for signal.",
        "> Calling tool: python(\"rank_sources(candidates)\")",
        "> Synthesizing the strongest evidence into a concise answer.",
    ]

    for line in steps:
        print(line, flush=True)
        time.sleep(1)

    result = (
        f"Fake Phase 1 result for {agent_name}: Kubernetes created a real Pod, "
        "streamed its logs, observed completion, and reconciled this Agent status."
    )
    print(f"RESULT: {result}", flush=True)

    termination_message = os.environ.get("KONTEXT_TERMINATION_MESSAGE", "/dev/termination-log")
    try:
        with open(termination_message, "w", encoding="utf-8") as handle:
            handle.write(result)
    except OSError as exc:
        print(f"warning: could not write termination message: {exc}", file=sys.stderr, flush=True)

    return 0


if __name__ == "__main__":
    raise SystemExit(main())
