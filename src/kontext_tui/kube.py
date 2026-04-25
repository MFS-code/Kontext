from __future__ import annotations

import asyncio
import json
import os
from collections.abc import AsyncIterator

import yaml

from .models import AgentConfig, AgentStatus


class KubectlError(RuntimeError):
    pass


async def run_kubectl(
    *args: str,
    stdin: str | None = None,
    timeout: float = 20,
) -> str:
    env = os.environ.copy()
    env.setdefault("KUBECTL_EXTERNAL_DIFF", "true")
    process = await asyncio.create_subprocess_exec(
        "kubectl",
        *args,
        stdin=asyncio.subprocess.PIPE if stdin is not None else None,
        stdout=asyncio.subprocess.PIPE,
        stderr=asyncio.subprocess.PIPE,
        env=env,
    )
    try:
        stdout, stderr = await asyncio.wait_for(
            process.communicate(stdin.encode() if stdin is not None else None),
            timeout=timeout,
        )
    except asyncio.TimeoutError as exc:
        process.kill()
        raise KubectlError(f"kubectl {' '.join(args)} timed out") from exc

    if process.returncode != 0:
        detail = stderr.decode().strip() or stdout.decode().strip()
        raise KubectlError(detail or f"kubectl {' '.join(args)} failed")
    return stdout.decode()


async def secret_exists(name: str) -> tuple[bool, str]:
    try:
        await run_kubectl("get", "secret", name, timeout=5)
    except KubectlError as exc:
        return False, str(exc)
    return True, name


async def kubectl_available() -> tuple[bool, str]:
    try:
        version = await run_kubectl("version", "--client", "--output=json")
    except (FileNotFoundError, KubectlError) as exc:
        return False, str(exc)
    data = json.loads(version)
    git_version = data.get("clientVersion", {}).get("gitVersion", "unknown")
    return True, git_version


async def cluster_available() -> tuple[bool, str]:
    try:
        context = await run_kubectl("config", "current-context")
        await run_kubectl("get", "--raw=/readyz", timeout=5)
    except KubectlError as exc:
        return False, str(exc)
    return True, context.strip()


async def crd_installed() -> tuple[bool, str]:
    try:
        await run_kubectl("get", "crd", "agents.kontext.dev")
    except KubectlError as exc:
        return False, str(exc)
    return True, "agents.kontext.dev"


async def upsert_provider_secret(provider: str, api_key: str, secret_name: str | None = None) -> str:
    key_name = "ANTHROPIC_API_KEY"
    secret = {
        "apiVersion": "v1",
        "kind": "Secret",
        "metadata": {"name": secret_name or f"kontext-{provider}"},
        "type": "Opaque",
        "stringData": {key_name: api_key},
    }
    await run_kubectl("apply", "-f", "-", stdin=yaml.safe_dump(secret), timeout=15)
    return secret["metadata"]["name"]


async def apply_agent(config: AgentConfig) -> str:
    await run_kubectl("apply", "-f", "-", stdin=config.to_yaml(), timeout=15)
    return config.name


async def list_agents() -> list[AgentStatus]:
    raw = await run_kubectl("get", "agents", "-o", "json", timeout=10)
    payload = json.loads(raw)
    return [AgentStatus.from_resource(item) for item in payload.get("items", [])]


async def get_agent(name: str) -> AgentStatus:
    raw = await run_kubectl("get", "agent", name, "-o", "json", timeout=10)
    return AgentStatus.from_resource(json.loads(raw))


async def stream_logs(pod_name: str, lines: int = 80) -> AsyncIterator[str]:
    if not pod_name or pod_name == "-":
        return
    process = await asyncio.create_subprocess_exec(
        "kubectl",
        "logs",
        "-f",
        f"--tail={lines}",
        pod_name,
        stdout=asyncio.subprocess.PIPE,
        stderr=asyncio.subprocess.STDOUT,
    )
    assert process.stdout is not None
    try:
        async for line in process.stdout:
            yield line.decode(errors="replace").rstrip()
    finally:
        if process.returncode is None:
            process.terminate()
