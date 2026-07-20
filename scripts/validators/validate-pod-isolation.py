#!/usr/bin/env python3
"""Validate the static isolation contract for Playwright and runtime Pods."""

import json
import sys


def main() -> None:
    path, container_name, mode = sys.argv[1:]
    with open(path, encoding="utf-8") as stream:
        pod = json.load(stream)
    containers = pod["spec"]["containers"]
    selected = next(
        (item for item in containers if item["name"] == container_name), None
    )
    if selected is None:
        raise SystemExit(f"container {container_name!r} is missing")
    if selected.get("envFrom"):
        raise SystemExit(f"{mode} container must not use envFrom")
    environment = selected.get("env", [])
    if any(item.get("valueFrom") is not None for item in environment):
        raise SystemExit(
            f"{mode} container has a Secret or other valueFrom environment source"
        )

    if mode == "browser":
        expected_environment = {
            "HOME": "/tmp/home",
            "TMPDIR": "/tmp",
            "XDG_CACHE_HOME": "/tmp/xdg-cache",
            "XDG_CONFIG_HOME": "/tmp/xdg-config",
            "PLAYWRIGHT_MCP_PING_TIMEOUT_MS": "30000",
        }
        actual_environment = {
            item["name"]: item.get("value", "") for item in environment
        }
        if actual_environment != expected_environment:
            raise SystemExit(
                "browser environment names differ from the explicit allowlist: "
                f"{sorted(actual_environment)}"
            )
        expected_volumes = {"tmp", "dev-shm"}
        volumes = pod["spec"].get("volumes", [])
        if {item["name"] for item in volumes} != expected_volumes:
            raise SystemExit(f"browser volume names differ from allowlist: {volumes!r}")
        if any(set(item) != {"name", "emptyDir"} for item in volumes):
            raise SystemExit("browser volume uses a source other than emptyDir")
        expected_mounts = {"tmp": "/tmp", "dev-shm": "/dev/shm"}
        actual_mounts = {
            item["name"]: item["mountPath"]
            for item in selected.get("volumeMounts", [])
        }
        if actual_mounts != expected_mounts:
            raise SystemExit(
                f"browser volume mounts differ from allowlist: {actual_mounts!r}"
            )
        if pod["spec"].get("automountServiceAccountToken") is not False:
            raise SystemExit(
                "browser Pod must explicitly disable ServiceAccount token automount"
            )
    elif mode == "runtime":
        expected_names = {
            "KONTEXT_RUN_NAME",
            "KONTEXT_AGENT_NAME",
            "KONTEXT_GOAL",
            "KONTEXT_PROVIDER",
            "KONTEXT_MODEL",
            "KONTEXT_TOOLS",
            "KONTEXT_BUDGET_TOKENS",
            "KONTEXT_BUDGET_WALLCLOCK",
            "KONTEXT_BUDGET_DOLLARS",
            "KONTEXT_MCP_CONFIG",
            "KONTEXT_FAKE_SCENARIO",
            "KONTEXT_FAKE_TOOL_SEQUENCE",
            "KONTEXT_MAX_TURNS",
            "KONTEXT_MAX_TOOL_CALLS",
            "KONTEXT_MAX_TOOL_RESULT_BYTES",
            "KONTEXT_MAX_TOTAL_TOOL_OUTPUT_BYTES",
        }
        actual_names = {item["name"] for item in environment}
        if actual_names != expected_names:
            raise SystemExit(
                "fake runtime environment names differ from expected literals: "
                f"{sorted(actual_names)}"
            )
        if pod["spec"].get("volumes"):
            raise SystemExit(
                "fake runtime Pod must not contain Secret, projected, or other volumes"
            )
        if selected.get("volumeMounts"):
            raise SystemExit(
                "fake runtime container must not contain volume mounts"
            )
    else:
        raise SystemExit(f"unsupported isolation mode {mode!r}")


if __name__ == "__main__":
    main()
