from __future__ import annotations

import json
import logging
import os
import re
import subprocess
from typing import Any, cast

import kopf
from kubernetes import client, config
from kubernetes.client import ApiException


GROUP = "kontext.dev"
VERSION = "v1"
PLURAL = "agents"
AGENT_IMAGE = os.environ.get("KONTEXT_AGENT_IMAGE", "kontext:dev")
NAMESPACE = os.environ.get("KONTEXT_NAMESPACE", "default")


def main() -> int:
    command = ["kopf", "run", "--standalone", "--all-namespaces", __file__]
    return subprocess.call(command)


@kopf.on.startup()
def configure(settings: kopf.OperatorSettings, **_: Any) -> None:
    settings.posting.level = logging.INFO
    try:
        config.load_incluster_config()
    except config.ConfigException:
        config.load_kube_config()


@kopf.on.create(GROUP, VERSION, PLURAL)
def create_agent(
    spec: Any,
    name: str,
    namespace: str | None,
    uid: str,
    patch: kopf.Patch,
    **_: Any,
) -> None:
    namespace = namespace or NAMESPACE
    pod_name = pod_name_for(name)
    pod = build_pod(name=name, pod_name=pod_name, namespace=namespace, uid=uid, spec=dict(spec))
    core = client.CoreV1Api()

    try:
        core.create_namespaced_pod(namespace=namespace, body=pod)
    except ApiException as exc:
        if exc.status != 409:
            raise

    patch.status["phase"] = "Pending"
    patch.status["podName"] = pod_name
    patch.status["message"] = "Agent pod requested."
    patch.status["tokensUsed"] = 0
    patch.status["dollarsUsed"] = 0


@kopf.timer(GROUP, VERSION, PLURAL, interval=2.0)
def reconcile_agent(
    spec: Any,
    name: str,
    namespace: str | None,
    status: Any,
    patch: kopf.Patch,
    **_: Any,
) -> None:
    namespace = namespace or NAMESPACE
    pod_name = status.get("podName") or pod_name_for(name)
    core = client.CoreV1Api()

    try:
        pod = cast(client.V1Pod, core.read_namespaced_pod(name=pod_name, namespace=namespace))
    except ApiException as exc:
        if exc.status == 404:
            patch.status["phase"] = "Pending"
            patch.status["message"] = "Waiting for agent pod."
            return
        raise

    phase, message, result, usage = phase_from_pod(pod)
    patch.status["phase"] = phase
    patch.status["podName"] = pod_name
    patch.status["message"] = message
    patch.status["tokensUsed"] = usage.get("tokensUsed", 0)
    patch.status["dollarsUsed"] = usage.get("dollarsUsed", 0.0)
    if result:
        patch.status["result"] = result


def build_pod(
    *,
    name: str,
    pod_name: str,
    namespace: str,
    uid: str,
    spec: dict[str, Any],
) -> client.V1Pod:
    labels = {"app.kubernetes.io/name": "kontext-agent", "kontext.dev/agent": name}
    tools = spec.get("tools") or []
    budget = spec.get("budget") or {}
    provider = str(spec.get("provider", "anthropic"))
    secret_name = ((spec.get("secretRef") or {}).get("name")) or "kontext-anthropic"
    command = ["kontext-fake-runner"] if provider in {"fake", "replay"} else ["kontext-runner"]
    env = [
        client.V1EnvVar(name="KONTEXT_AGENT_NAME", value=name),
        client.V1EnvVar(name="KONTEXT_GOAL", value=str(spec.get("goal", ""))),
        client.V1EnvVar(name="KONTEXT_PROVIDER", value=provider),
        client.V1EnvVar(name="KONTEXT_MODEL", value=str(spec.get("model", ""))),
        client.V1EnvVar(name="KONTEXT_TOOLS", value=",".join(map(str, tools))),
        client.V1EnvVar(name="KONTEXT_BUDGET_TOKENS", value=str(budget.get("tokens", ""))),
        client.V1EnvVar(name="KONTEXT_BUDGET_WALLCLOCK", value=str(budget.get("wallclock", ""))),
    ]
    if provider not in {"fake", "replay"}:
        env.append(
            client.V1EnvVar(
                name="ANTHROPIC_API_KEY",
                value_from=client.V1EnvVarSource(
                    secret_key_ref=client.V1SecretKeySelector(
                        name=secret_name,
                        key="ANTHROPIC_API_KEY",
                    )
                ),
            )
        )

    return client.V1Pod(
        metadata=client.V1ObjectMeta(
            name=pod_name,
            namespace=namespace,
            labels=labels,
            owner_references=[
                client.V1OwnerReference(
                    api_version=f"{GROUP}/{VERSION}",
                    kind="Agent",
                    name=name,
                    uid=uid,
                    controller=True,
                    block_owner_deletion=True,
                )
            ],
        ),
        spec=client.V1PodSpec(
            restart_policy="Never",
            containers=[
                client.V1Container(
                    name="runner",
                    image=AGENT_IMAGE,
                    image_pull_policy="IfNotPresent",
                    command=command,
                    env=env,
                    resources=client.V1ResourceRequirements(
                        requests={"cpu": "50m", "memory": "64Mi"},
                        limits={"cpu": "250m", "memory": "128Mi"},
                    ),
                )
            ],
        ),
    )


def phase_from_pod(pod: client.V1Pod) -> tuple[str, str, str | None, dict[str, Any]]:
    pod_status = pod.status
    if pod_status is None:
        return "Pending", "Agent pod status is not available yet.", None, {"tokensUsed": 0, "dollarsUsed": 0.0}

    pod_phase = pod_status.phase or "Pending"
    statuses = pod_status.container_statuses or []
    terminated = statuses[0].state.terminated if statuses and statuses[0].state else None

    if terminated:
        payload = parse_termination_message(terminated.message)
        usage = {
            "tokensUsed": int(payload.get("tokensUsed", 0) or 0),
            "dollarsUsed": float(payload.get("dollarsUsed", 0.0) or 0.0),
        }
        result = str(payload.get("result") or "")
        if terminated.exit_code == 0:
            return "Succeeded", "Agent pod completed.", result or terminated.message or None, usage
        error = payload.get("error")
        message = f"Agent pod exited with code {terminated.exit_code}."
        if error:
            message = f"{message} {error}"
        return "Failed", message, result or None, usage

    if pod_phase == "Running":
        return "Running", "Agent pod is streaming thoughts.", None, {"tokensUsed": 0, "dollarsUsed": 0.0}
    if pod_phase in {"Failed", "Succeeded"}:
        return pod_phase, f"Pod phase is {pod_phase}.", None, {"tokensUsed": 0, "dollarsUsed": 0.0}
    return "Pending", "Agent pod is waiting to start.", None, {"tokensUsed": 0, "dollarsUsed": 0.0}


def parse_termination_message(message: str | None) -> dict[str, Any]:
    if not message:
        return {}
    try:
        payload = json.loads(message)
    except json.JSONDecodeError:
        return {"result": message}
    return payload if isinstance(payload, dict) else {"result": message}


def pod_name_for(agent_name: str) -> str:
    safe = re.sub(r"[^a-z0-9-]+", "-", agent_name.lower()).strip("-")
    return f"agent-{safe[:56]}"


if __name__ == "__main__":
    raise SystemExit(main())
