from __future__ import annotations

import asyncio
from pathlib import Path

from textual import on
from textual.app import App, ComposeResult
from textual.binding import Binding
from textual.containers import Container, Horizontal, Vertical
from textual.reactive import reactive
from textual.widgets import DataTable, Footer, Input, Label, Log, Static, TabbedContent, TabPane, TextArea

from . import kube
from .models import AgentConfig, AgentStatus


LOGO = """ _                  _            _
| | _____  _ __    | |_ _____  _| |_
| |/ / _ \\| '_ \\   | __/ _ \\ \\/ / __|
|   < (_) | | | |  | ||  __/>  <| |_
|_|\\_\\___/|_| |_|   \\__\\___/_/\\_\\\\__|

agents are workloads"""

PHASE_STYLES = {
    "Pending": "dim",
    "Running": "dodger_blue1",
    "Succeeded": "green",
    "Failed": "red",
}


class KontextApp(App[None]):
    CSS_PATH = "styles.tcss"
    TITLE = "Kontext"
    SUB_TITLE = "agents are workloads"
    BINDINGS = [
        ("1", "show_create", "Create"),
        ("2", "show_monitor", "Monitor"),
        ("g", "focus_goal", "Goal"),
        ("n", "focus_name", "Name"),
        ("y", "focus_yaml", "YAML"),
        ("w", "focus_watch", "Watch"),
        ("c", "check_cluster", "Check"),
        ("s", "save_secret", "Secret"),
        ("l", "launch_agent", "Launch"),
        ("r", "refresh_agents", "Refresh"),
        ("x", "stop_logs", "Stop logs"),
        Binding("escape", "clear_focus", "Commands", show=False, priority=True),
        ("?", "toggle_help", "Help"),
        ("q", "quit", "Quit"),
    ]

    show_help = reactive(False)

    def __init__(self, fallback: bool = False) -> None:
        super().__init__()
        self.config = AgentConfig()
        self.fallback = fallback
        self._log_task: asyncio.Task[None] | None = None
        self._log_pod_name: str | None = None
        self.selected_agent: str | None = None
        self._agents_by_name: dict[str, AgentStatus] = {}

    def compose(self) -> ComposeResult:
        yield Container(
            Static(LOGO, id="logo"),
            Static(
                "esc commands  1 create  2 monitor  c check  s secret  g goal  n name  y yaml  w watch  l launch  r refresh  x stop  ? help  q quit",
                id="command-strip",
            ),
            Static("kubectl checking | cluster checking | crd checking | secret checking", id="readiness-strip"),
            Static("REPLAY MODE: canned fixture data, not Kubernetes", id="fallback-banner"),
            id="hero",
        )
        yield Static(
            "Tab moves fields. Esc clears focus so single-key commands work. Generated YAML can be applied manually.",
            id="help",
        )
        with TabbedContent(initial="create", id="tabs"):
            with TabPane("Create Agent", id="create"):
                with Horizontal(classes="create-grid"):
                    yield from self._agent_panel()
                    with Vertical(classes="panel yaml-panel"):
                        yield Label("Generated Agent YAML")
                        yield TextArea(self.config.to_yaml(), id="yaml-preview", read_only=True)
            with TabPane("Monitor", id="monitor"):
                with Vertical(id="monitor-cockpit"):
                    with Horizontal(classes="monitor-grid"):
                        with Vertical(classes="panel agents-panel"):
                            yield Label("Agents")
                            table = DataTable(id="agents-table", cursor_type="row")
                            table.add_columns("Name", "Phase", "Pod", "Tokens")
                            yield table
                        with Vertical(classes="panel logs-panel"):
                            yield Label("Logs")
                            yield Log(id="logs", highlight=True)
                    with Vertical(classes="panel result-panel"):
                        yield Label("Result")
                        yield TextArea("Waiting for `.status.result`...", id="result", read_only=True)
        yield Footer()

    def _agent_panel(self) -> ComposeResult:
        with Vertical(classes="panel agent-panel"):
            yield Label("Agent")
            yield Static(self.provider_line(), id="provider-line", classes="terminal-line")
            yield Label("name")
            yield Input(value=self.config.name, id="agent-name")
            yield Label("goal")
            yield TextArea(self.config.goal, id="goal")
            with Horizontal(classes="fanout-row"):
                with Vertical(classes="field-column"):
                    yield Label("replicas")
                    yield Input(value=str(self.config.replicas), placeholder="1", id="replicas")
                with Vertical(classes="field-column"):
                    yield Label("goal template")
                    yield Input(value=self.config.goal_template, id="goal-template")
            yield Label("fanout topics")
            yield TextArea(self.config.topics_text, id="topics")
            with Horizontal(classes="budget-row"):
                with Vertical(classes="field-column"):
                    yield Label("wallclock limit")
                    yield Input(value=self.config.wallclock, placeholder="5m", id="wallclock")
                with Vertical(classes="field-column"):
                    yield Label("token budget")
                    yield Input(value=str(self.config.tokens), placeholder="200000", id="tokens")
                with Vertical(classes="field-column"):
                    yield Label("dollar budget")
                    yield Input(value=str(self.config.dollars), placeholder="2.0", id="dollars")
            yield Label("secret")
            yield Input(value=self.config.secret_name, id="secret-name")
            yield Label("api key")
            yield Input(password=True, placeholder="ANTHROPIC_API_KEY", id="api-key")
            yield Static(self.tools_line(), id="tools-line", classes="terminal-line")
            yield Static("[c] check cluster   [s] save Secret   [l] launch Agent", classes="action-line")
            yield Static("status: ready", id="action-status", classes="status")

    async def on_mount(self) -> None:
        self.query_one("#fallback-banner", Static).display = self.fallback
        self.query_one("#help", Static).display = False
        self.set_interval(2, lambda: asyncio.create_task(self.refresh_agents()))
        asyncio.create_task(self.check_cluster())
        await self.refresh_agents()

    @on(Input.Changed)
    def input_changed(self, event: Input.Changed) -> None:
        if event.input.id == "api-key":
            return
        self.update_config_from_form()

    @on(TextArea.Changed)
    def textarea_changed(self, event: TextArea.Changed) -> None:
        if event.text_area.id in {"goal", "topics"}:
            self.update_config_from_form()

    async def check_cluster(self) -> None:
        self.set_status("checking Kubernetes readiness...")
        self.query_one("#readiness-strip", Static).update(
            "kubectl checking | cluster checking | crd checking | secret checking"
        )
        kubectl, cluster, crd, secret = await asyncio.gather(
            kube.kubectl_available(),
            kube.cluster_available(),
            kube.crd_installed(),
            kube.secret_exists(self.config.secret_name),
        )
        parts = [
            self._readiness_part("kubectl", kubectl),
            self._readiness_part("cluster", cluster),
            self._readiness_part("crd agents.kontext.dev", crd),
            self._readiness_part("secret", secret),
        ]
        self.query_one("#readiness-strip", Static).update(" | ".join(parts))
        self.set_status("readiness check complete")

    async def save_secret(self) -> None:
        self.update_config_from_form()
        api_key = self.query_one("#api-key", Input).value.strip()
        if not api_key:
            self.set_status("enter an API key before creating the Secret")
            return
        self.set_status(f"applying Secret/{self.config.secret_name}...")
        try:
            secret_name = await kube.upsert_provider_secret(
                self.config.provider,
                api_key,
                self.config.secret_name,
            )
        except Exception as exc:  # noqa: BLE001 - TUI should surface subprocess failures.
            self.set_status(f"Secret apply failed: {exc}")
            return
        self.set_status(f"Secret ready: {secret_name}")
        await self.check_cluster()

    async def launch_agent(self) -> None:
        self.update_config_from_form()
        errors = self.config.validate()
        if errors:
            self.set_status("Cannot launch: " + "; ".join(errors))
            return
        if self.fallback:
            self.set_status("replay mode active: showing canned watch data")
            await self.show_fallback()
            return
        count = len(self.config.to_resources())
        noun = "Agent" if count == 1 else "Agents"
        self.set_status(f"applying {count} {noun} with: kubectl apply -f -")
        try:
            await kube.apply_agent(self.config)
        except Exception as exc:  # noqa: BLE001
            self.set_status(f"Apply failed: {exc}")
            return
        self.set_status(f"Applied {count} {noun} with: kubectl apply -f -")
        self.selected_agent = self.config.generated_names()[0]
        await self.refresh_agents()
        self.query_one(TabbedContent).active = "monitor"

    def update_config_from_form(self) -> None:
        self.config.name = self.query_one("#agent-name", Input).value.strip() or "research-tariffs"
        self.config.goal = self.query_one("#goal", TextArea).text.strip() or AgentConfig().goal
        self.config.replicas = self._int_value("#replicas", 1)
        self.config.goal_template = self.query_one("#goal-template", Input).value.strip() or AgentConfig().goal_template
        self.config.topics_text = self.query_one("#topics", TextArea).text
        self.config.wallclock = self.query_one("#wallclock", Input).value.strip() or "5m"
        self.config.tokens = self._int_value("#tokens", 200_000)
        self.config.dollars = self._float_value("#dollars", 2.0)
        self.config.secret_name_value = self.query_one("#secret-name", Input).value.strip()
        self.update_yaml_preview()

    def update_yaml_preview(self) -> None:
        if self.is_mounted:
            self.query_one("#yaml-preview", TextArea).text = self.config.to_yaml()
            self.query_one("#provider-line", Static).update(self.provider_line())
            self.query_one("#tools-line", Static).update(self.tools_line())

    async def refresh_agents(self) -> None:
        if self.fallback:
            await self.show_fallback()
            return
        try:
            agents = await kube.list_agents()
        except Exception as exc:  # noqa: BLE001
            self.query_one("#logs", Log).write_line(f"kubectl get agents failed: {exc}")
            return
        await self.render_agents(agents)

    async def render_agents(self, agents: list[AgentStatus]) -> None:
        table = self.query_one("#agents-table", DataTable)
        table.clear()
        self._agents_by_name = {agent.name: agent for agent in agents}
        selected = self.selected_agent or (agents[0].name if agents else None)
        for agent in agents:
            phase_style = PHASE_STYLES.get(agent.phase, "white")
            table.add_row(
                agent.name,
                f"[{phase_style}]{agent.phase}[/]",
                agent.pod_name,
                agent.tokens,
                key=agent.name,
            )
        if not agents:
            self.query_one("#result", TextArea).text = "No `Agent` resources found yet."
            return
        current = next((agent for agent in agents if agent.name == selected), agents[0])
        self.selected_agent = current.name
        self.render_agent_detail(current)

    @on(DataTable.RowSelected, "#agents-table")
    async def row_selected(self, event: DataTable.RowSelected) -> None:
        self.selected_agent = str(event.row_key.value)
        agent = self._agents_by_name.get(self.selected_agent)
        if agent:
            self.render_agent_detail(agent)

    def render_agent_detail(self, agent: AgentStatus) -> None:
        result = agent.result or agent.message or "Waiting for `.status.result`..."
        if agent.tokens != "-":
            result = f"{result}\n\n`tokens: {agent.tokens}`"
        self.query_one("#result", TextArea).text = result
        if not self.fallback:
            self.follow_logs(agent)

    def follow_logs(self, agent: AgentStatus) -> None:
        if self._log_pod_name == agent.pod_name and self._log_task and not self._log_task.done():
            return
        if self._log_task and not self._log_task.done():
            self._log_task.cancel()
        self._log_pod_name = None
        log = self.query_one("#logs", Log)
        log.clear()
        if not agent.pod_name or agent.pod_name == "-":
            log.write_line("Waiting for controller to report status.podName...")
            return
        self._log_pod_name = agent.pod_name
        self._log_task = asyncio.create_task(self._stream_pod_logs(agent.pod_name))

    async def _stream_pod_logs(self, pod_name: str) -> None:
        log = self.query_one("#logs", Log)
        try:
            async for line in kube.stream_logs(pod_name):
                log.write_line(line)
        except asyncio.CancelledError:
            raise
        except Exception as exc:  # noqa: BLE001
            log.write_line(f"kubectl logs failed: {exc}")

    async def show_fallback(self) -> None:
        fixture_dir = Path(__file__).resolve().parents[3] / "demo" / "fixtures"
        statuses = [
            AgentStatus("research-tariffs", "Running", "3s", "$0.08/$2.0", "agent-research-tariffs", "1200/200000"),
            AgentStatus("ai-regulation", "Running", "7s", "$0.21/$2.0", "agent-ai-regulation", "3200/200000"),
            AgentStatus("ev-market", "Pending", "1s", "$0/$2.0", "-", "0/200000"),
        ]
        logs_path = fixture_dir / "research-logs.txt"
        result_path = fixture_dir / "result.txt"
        statuses[0].result = result_path.read_text() if result_path.exists() else ""
        statuses[0].yaml = self.config.to_yaml()
        await self.render_agents(statuses)
        log = self.query_one("#logs", Log)
        log.clear()
        if logs_path.exists():
            for line in logs_path.read_text().splitlines():
                log.write_line(line)
                await asyncio.sleep(0.12)

    def action_refresh_agents(self) -> None:
        asyncio.create_task(self.refresh_agents())

    def action_show_create(self) -> None:
        self.query_one(TabbedContent).active = "create"

    def action_show_monitor(self) -> None:
        self.query_one(TabbedContent).active = "monitor"

    def action_check_cluster(self) -> None:
        asyncio.create_task(self.check_cluster())

    def action_save_secret(self) -> None:
        asyncio.create_task(self.save_secret())

    def action_launch_agent(self) -> None:
        asyncio.create_task(self.launch_agent())

    def action_focus_goal(self) -> None:
        self.query_one("#goal", TextArea).focus()

    def action_focus_name(self) -> None:
        self.query_one("#agent-name", Input).focus()

    def action_focus_yaml(self) -> None:
        self.query_one("#yaml-preview", TextArea).focus()

    def action_focus_watch(self) -> None:
        self.query_one("#agents-table", DataTable).focus()

    def action_clear_focus(self) -> None:
        self.set_focus(None)
        self.set_status("command mode")

    def action_stop_logs(self) -> None:
        if self._log_task and not self._log_task.done():
            self._log_task.cancel()
        self._log_pod_name = None
        self.query_one("#logs", Log).write_line("Stopped following pod logs.")

    def action_toggle_help(self) -> None:
        self.show_help = not self.show_help
        self.query_one("#help", Static).display = self.show_help

    def provider_line(self) -> str:
        return f"> provider={self.config.provider}  model={self.config.model}  secret={self.config.secret_name}"

    def tools_line(self) -> str:
        return "> tools=" + ", ".join(self.config.tools)

    def set_status(self, message: str) -> None:
        self.query_one("#action-status", Static).update(f"status: {message}")

    def _readiness_part(self, label: str, check: tuple[bool, str]) -> str:
        ok, detail = check
        state = "OK" if ok else "missing"
        if label == "cluster" and ok:
            return f"cluster {detail} {state}"
        return f"{label} {state}"

    def _int_value(self, selector: str, default: int) -> int:
        try:
            return int(self.query_one(selector, Input).value)
        except ValueError:
            return default

    def _float_value(self, selector: str, default: float) -> float:
        try:
            return float(self.query_one(selector, Input).value)
        except ValueError:
            return default