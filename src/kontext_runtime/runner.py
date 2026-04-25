from __future__ import annotations

import json
import os
import signal
import sys
import time
from contextlib import contextmanager
from dataclasses import dataclass
from typing import Iterator

from anthropic import Anthropic


DEFAULT_MODEL = "claude-sonnet-4-20250514"
DEFAULT_WALLCLOCK_SECONDS = 300
DEFAULT_MAX_TOKENS = 1024
MODEL_ALIASES = {
    "claude-sonnet-4": DEFAULT_MODEL,
}


@dataclass(frozen=True)
class RuntimeConfig:
    agent_name: str
    goal: str
    provider: str
    model: str
    tools: list[str]
    max_tokens: int
    wallclock_seconds: int
    termination_message_path: str


class WallclockTimeout(RuntimeError):
    pass


def main() -> int:
    config = load_config()

    try:
        if config.provider != "anthropic":
            raise ValueError(f"unsupported provider: {config.provider}")

        result, input_tokens, output_tokens = run_anthropic(config)
        write_termination_message(
            config.termination_message_path,
            result=result,
            tokens_used=input_tokens + output_tokens,
            dollars_used=0.0,
        )
        print(f"RESULT: {result}", flush=True)
        return 0
    except Exception as exc:
        message = f"{type(exc).__name__}: {exc}"
        print(f"ERROR: {message}", file=sys.stderr, flush=True)
        write_termination_message(
            config.termination_message_path,
            result="",
            tokens_used=0,
            dollars_used=0.0,
            error=message,
        )
        return 1


def load_config() -> RuntimeConfig:
    tools = [tool for tool in env("KONTEXT_TOOLS", "").split(",") if tool]
    return RuntimeConfig(
        agent_name=env("KONTEXT_AGENT_NAME", "agent"),
        goal=env("KONTEXT_GOAL", "Think about the requested task."),
        provider=env("KONTEXT_PROVIDER", "anthropic").lower(),
        model=normalize_model(env("KONTEXT_MODEL", DEFAULT_MODEL)),
        tools=tools,
        max_tokens=parse_positive_int(env("KONTEXT_BUDGET_TOKENS", ""), DEFAULT_MAX_TOKENS),
        wallclock_seconds=parse_duration_seconds(
            env("KONTEXT_BUDGET_WALLCLOCK", ""),
            DEFAULT_WALLCLOCK_SECONDS,
        ),
        termination_message_path=env("KONTEXT_TERMINATION_MESSAGE", "/dev/termination-log"),
    )


def run_anthropic(config: RuntimeConfig) -> tuple[str, int, int]:
    print(f"> Agent {config.agent_name} accepted goal: {config.goal}", flush=True)
    print(f"> Loading Anthropic model: {config.model}", flush=True)
    print(f"> Available tools: {', '.join(config.tools) or 'none'}", flush=True)
    print(f"> Wallclock budget: {config.wallclock_seconds}s", flush=True)
    print("> Sending prompt to the model.", flush=True)

    client = Anthropic()
    started = time.monotonic()

    with wallclock_limit(config.wallclock_seconds):
        response = client.messages.create(
            model=config.model,
            max_tokens=min(config.max_tokens, DEFAULT_MAX_TOKENS),
            system=(
                "You are running inside a Kubernetes Pod as a Kontext Agent. "
                "Answer the user's goal directly and concisely. Mention key assumptions "
                "when the task asks for current or externally verifiable facts."
            ),
            messages=[{"role": "user", "content": config.goal}],
        )

    elapsed = time.monotonic() - started
    print(f"> Model call completed in {elapsed:.1f}s.", flush=True)
    print("> Synthesizing final answer for Agent status.", flush=True)

    result = extract_text(response)
    input_tokens = int(getattr(response.usage, "input_tokens", 0) or 0)
    output_tokens = int(getattr(response.usage, "output_tokens", 0) or 0)
    return result, input_tokens, output_tokens


def normalize_model(model: str) -> str:
    return MODEL_ALIASES.get(model, model)


def extract_text(response: object) -> str:
    chunks: list[str] = []
    for block in getattr(response, "content", []) or []:
        if getattr(block, "type", "") == "text":
            chunks.append(str(getattr(block, "text", "")))
    return "\n".join(chunk for chunk in chunks if chunk).strip()


def write_termination_message(
    path: str,
    *,
    result: str,
    tokens_used: int,
    dollars_used: float,
    error: str | None = None,
) -> None:
    payload = {
        "result": result,
        "tokensUsed": tokens_used,
        "dollarsUsed": dollars_used,
    }
    if error:
        payload["error"] = error

    try:
        with open(path, "w", encoding="utf-8") as handle:
            json.dump(payload, handle)
    except OSError as exc:
        print(f"warning: could not write termination message: {exc}", file=sys.stderr, flush=True)


@contextmanager
def wallclock_limit(seconds: int) -> Iterator[None]:
    def raise_timeout(_signum: int, _frame: object) -> None:
        raise WallclockTimeout(f"wallclock budget exceeded after {seconds}s")

    previous_handler = signal.signal(signal.SIGALRM, raise_timeout)
    signal.alarm(seconds)
    try:
        yield
    finally:
        signal.alarm(0)
        signal.signal(signal.SIGALRM, previous_handler)


def parse_positive_int(value: str, default: int) -> int:
    try:
        parsed = int(value)
    except ValueError:
        return default
    return parsed if parsed > 0 else default


def parse_duration_seconds(value: str, default: int) -> int:
    if not value:
        return default

    units = {"s": 1, "m": 60, "h": 3600}
    suffix = value[-1].lower()
    multiplier = units.get(suffix, 1)
    number = value[:-1] if suffix in units else value

    try:
        parsed = int(number)
    except ValueError:
        return default
    return parsed * multiplier if parsed > 0 else default


def env(name: str, default: str) -> str:
    return os.environ.get(name, default).strip() or default


if __name__ == "__main__":
    raise SystemExit(main())
