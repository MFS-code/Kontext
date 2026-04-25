from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any

import yaml


DEFAULT_TOOLS = ["web_fetch", "python", "bash"]
DEFAULT_SECRET_NAME = "kontext-anthropic"
DEFAULT_GOAL = "Find the 5 most-cited articles on US tariffs this month and summarize the consensus."
DEFAULT_TOPICS = [
    "US tariffs",
    "AI regulation",
    "EV market",
    "semiconductor supply chain",
    "renewable energy storage",
    "cybersecurity insurance",
    "space launch market",
    "AI data centers",
    "battery recycling",
    "robotics startups",
]


@dataclass(slots=True)
class AgentConfig:
    name: str = "research-tariffs"
    goal: str = DEFAULT_GOAL
    provider: str = "anthropic"
    model: str = "claude-sonnet-4"
    tools: list[str] = field(default_factory=lambda: DEFAULT_TOOLS[:2])
    wallclock: str = "5m"
    dollars: float = 2.0
    tokens: int = 200_000
    secret_name_value: str = DEFAULT_SECRET_NAME
    replicas: int = 1
    goal_template: str = "Research and summarize the current landscape for: {{topic}}."
    topics_text: str = "\n".join(DEFAULT_TOPICS)

    @property
    def secret_name(self) -> str:
        return self.secret_name_value.strip() or DEFAULT_SECRET_NAME

    def validate(self) -> list[str]:
        errors: list[str] = []
        if not is_valid_name(self.name):
            errors.append("name must be a DNS label: lowercase letters, numbers, and hyphens")
        if not self.goal.strip():
            errors.append("goal is required")
        if not is_valid_wallclock(self.wallclock):
            errors.append("wallclock must look like 5m, 30s, or 1h")
        if self.tokens <= 0:
            errors.append("token budget must be greater than zero")
        if self.dollars < 0:
            errors.append("dollar budget cannot be negative")
        if self.replicas <= 0:
            errors.append("replicas must be greater than zero")
        if self.replicas > 1:
            if not self.topics:
                errors.append("fanout needs at least one topic")
            for name in self.generated_names():
                if not is_valid_name(name):
                    errors.append("generated fanout names must be DNS labels")
                    break
        return errors

    @property
    def topics(self) -> list[str]:
        raw_topics = self.topics_text.replace(",", "\n").splitlines()
        return [topic.strip() for topic in raw_topics if topic.strip()]

    def generated_names(self) -> list[str]:
        if self.replicas <= 1:
            return [self.name]
        return [f"{self.name}-{index}" for index in range(self.replicas)]

    def to_resources(self) -> list[dict[str, Any]]:
        if self.replicas <= 1:
            return [self.to_resource(self.name, self.goal)]

        resources: list[dict[str, Any]] = []
        topics = self.topics
        for index in range(self.replicas):
            topic = topics[index % len(topics)]
            goal = render_goal_template(self.goal_template, topic, index)
            resources.append(self.to_resource(f"{self.name}-{index}", goal))
        return resources

    def to_resource(self, name: str | None = None, goal: str | None = None) -> dict[str, Any]:
        spec: dict[str, Any] = {
            "goal": goal or self.goal,
            "provider": self.provider,
            "model": self.model,
            "tools": self.tools,
            "budget": {
                "tokens": self.tokens,
                "wallclock": self.wallclock,
                "dollars": round(self.dollars, 2),
            },
            "secretRef": {"name": self.secret_name},
        }

        return {
            "apiVersion": "kontext.dev/v1",
            "kind": "Agent",
            "metadata": {"name": name or self.name},
            "spec": spec,
        }

    def to_yaml(self) -> str:
        return yaml.safe_dump_all(self.to_resources(), sort_keys=False)


def is_valid_name(value: str) -> bool:
    import re

    return bool(re.fullmatch(r"[a-z0-9]([-a-z0-9]*[a-z0-9])?", value.strip()))


def is_valid_wallclock(value: str) -> bool:
    import re

    return bool(re.fullmatch(r"[1-9][0-9]*[smh]", value.strip()))


def render_goal_template(template: str, topic: str, index: int) -> str:
    goal = template.strip() or DEFAULT_GOAL
    return (
        goal.replace("{{topic}}", topic)
        .replace("{{ topic }}", topic)
        .replace("{{index}}", str(index))
        .replace("{{ index }}", str(index))
    )


@dataclass(slots=True)
class AgentStatus:
    name: str
    phase: str = "Pending"
    age: str = "-"
    budget: str = "-"
    pod_name: str = "-"
    tokens: str = "-"
    result: str = ""
    message: str = ""
    yaml: str = ""

    @classmethod
    def from_resource(cls, resource: dict[str, Any]) -> "AgentStatus":
        metadata = resource.get("metadata", {})
        status = resource.get("status", {})
        spec = resource.get("spec", {})
        budget = spec.get("budget", {})
        dollars_used = status.get("dollarsUsed")
        dollars_limit = budget.get("dollars")
        budget_label = "-"
        if dollars_used is not None or dollars_limit is not None:
            budget_label = f"${dollars_used or 0}/${dollars_limit or '?'}"
        tokens_used = status.get("tokensUsed")
        tokens_limit = budget.get("tokens")
        token_label = "-"
        if tokens_used is not None or tokens_limit is not None:
            token_label = f"{tokens_used or 0}/{tokens_limit or '?'}"

        return cls(
            name=metadata.get("name", "-"),
            phase=status.get("phase", "Pending"),
            age=metadata.get("creationTimestamp", "-"),
            budget=budget_label,
            pod_name=status.get("podName", "-"),
            tokens=token_label,
            result=status.get("result", ""),
            message=status.get("message", ""),
            yaml=yaml.safe_dump(resource, sort_keys=False),
        )
