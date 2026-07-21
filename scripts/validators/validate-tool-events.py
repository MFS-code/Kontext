#!/usr/bin/env python3
"""Validate bounded JSONL tool events emitted by the reference runtime."""

import json
import sys

EVENT_API_VERSION = "kontext.dev/event/v1alpha1"
EVENT_FIELDS = {"apiVersion", "timestamp", "type", "data"}
EVENT_TYPES = {"lifecycle", "output", "usage", "tool", "error"}


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
        for line_number, line in enumerate(stream, start=1):
            stripped = line.strip()
            if not stripped or not stripped.startswith("{"):
                continue
            try:
                event = json.loads(stripped)
            except json.JSONDecodeError as error:
                raise SystemExit(
                    f"malformed JSON event on line {line_number}: {error.msg}"
                ) from error
            if not isinstance(event, dict) or set(event) != EVENT_FIELDS:
                raise SystemExit(
                    f"invalid event envelope on line {line_number}: "
                    f"expected fields {sorted(EVENT_FIELDS)!r}"
                )
            if event["apiVersion"] != EVENT_API_VERSION:
                raise SystemExit(
                    f"unsupported event apiVersion on line {line_number}: "
                    f"{event['apiVersion']!r}"
                )
            if event["type"] not in EVENT_TYPES:
                raise SystemExit(
                    f"unsupported event type on line {line_number}: {event['type']!r}"
                )
            if not isinstance(event["timestamp"], str) or not event["timestamp"]:
                raise SystemExit(
                    f"event timestamp is required on line {line_number}"
                )
            if event.get("type") == "tool":
                data = event["data"]
                if not isinstance(data, dict):
                    raise SystemExit(
                        f"tool event data must be an object on line {line_number}"
                    )
                required = {"name", "count", "isError", "truncated"}
                if not required.issubset(data):
                    raise SystemExit(
                        f"tool event data is missing required fields on line {line_number}"
                    )
                if not isinstance(data["name"], str) or not data["name"].strip():
                    raise SystemExit(
                        f"tool event name is required on line {line_number}"
                    )
                if type(data["count"]) is not int or data["count"] < 1:
                    raise SystemExit(
                        f"tool event count must be at least 1 on line {line_number}"
                    )
                if type(data["isError"]) is not bool:
                    raise SystemExit(
                        f"tool event isError must be boolean on line {line_number}"
                    )
                if type(data["truncated"]) is not bool:
                    raise SystemExit(
                        f"tool event truncated must be boolean on line {line_number}"
                    )
                events.append(data)

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
