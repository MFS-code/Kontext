#!/usr/bin/env python3
"""Validate bounded JSONL tool events emitted by the reference runtime."""

import json
import sys


def main() -> None:
    (
        path,
        count,
        max_each,
        max_total,
        names,
        errors,
        minimum_truncations,
    ) = sys.argv[1:]
    events = []
    with open(path, encoding="utf-8") as stream:
        for line in stream:
            try:
                event = json.loads(line)
            except json.JSONDecodeError:
                continue
            if event.get("type") == "tool":
                events.append(event["data"])

    expected_count = int(count)
    if len(events) != expected_count:
        raise SystemExit(
            f"expected {expected_count} tool events, got {len(events)}: {events!r}"
        )
    expected_names = names.split(",") if names else []
    actual_names = [event.get("name") for event in events]
    if actual_names != expected_names:
        raise SystemExit(
            f"expected tool order {expected_names!r}, got {actual_names!r}"
        )
    if any("output" in event for event in events):
        raise SystemExit("tool event content was emitted without KONTEXT_EMIT_TOOL_OUTPUT")
    byte_counts = [int(event.get("outputBytes", -1)) for event in events]
    if any(value < 0 or value > int(max_each) for value in byte_counts):
        raise SystemExit(
            f"tool output byte counts exceed per-result bound: {byte_counts!r}"
        )
    if sum(byte_counts) > int(max_total):
        raise SystemExit(
            f"tool output byte counts exceed cumulative bound: {byte_counts!r}"
        )
    error_count = sum(bool(event.get("isError")) for event in events)
    if error_count != int(errors):
        raise SystemExit(
            f"expected {errors} tool errors, got {error_count}: {events!r}"
        )
    truncation_count = sum(bool(event.get("truncated")) for event in events)
    if truncation_count < int(minimum_truncations):
        raise SystemExit(
            f"expected at least {minimum_truncations} truncated tool results, "
            f"got {truncation_count}: {events!r}"
        )


if __name__ == "__main__":
    main()
