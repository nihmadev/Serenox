"""Interactive Jobs API lab plugin.

The commands intentionally expose the whole bounded jobs surface so this
example can be used as a manual smoke test against a recorded or live daemon.
"""

from __future__ import annotations

import asyncio
import time
from collections.abc import Mapping
from typing import ClassVar

from serenox_sdk import JobSpec, Module, SerenoxError, loader
from serenox_sdk.actions import Message

_DAY_MS = 86_400_000
_MAX_TODO_TEXT = 256


def _interval(kind: str, interval_ms: int) -> dict[str, object]:
    return {"kind": kind, "interval": {"interval_ms": interval_ms}}


def _one_shot(run_at_unix_ms: int) -> dict[str, object]:
    return {"kind": "one_shot", "one_shot": {"run_at_unix_ms": run_at_unix_ms}}


def _calendar() -> dict[str, object]:
    # Every minute at second zero; the core owns timezone/DST handling.
    return {"kind": "calendar", "calendar": {"seconds": [0]}}


def _now_ms() -> int:
    return int(time.time() * 1000)


def _payload(invocation: Mapping[str, object]) -> Mapping[str, object]:
    value = invocation.get("payload")
    return value if isinstance(value, Mapping) else {}


class JobsLab(Module):
    """A safe, owner-only playground for every first-class job operation."""

    id = "example.jobs"
    version = "1.0.0"
    author = "Serenox"
    description = "Interactive bounded Jobs API smoke-test plugin"
    strings: ClassVar[dict[str, str]] = {"name": "Jobs Lab"}

    def _spec(
        self,
        job_id: str,
        handler: str,
        schedule: Mapping[str, object],
        *,
        persistent: bool = True,
        payload: Mapping[str, object] | None = None,
        deadline_ms: int = _DAY_MS,
        overlap: str = "skip_if_running",
    ) -> JobSpec:
        return JobSpec(
            job_id=job_id,
            handler=handler,
            schedule=schedule,
            timezone="UTC",
            persistent=persistent,
            deadline_ms=deadline_ms,
            max_runtime_ms=10_000,
            misfire="skip",
            overlap=overlap,
            payload=dict(payload or {}),
        )

    async def _counter_job(self, invocation: Mapping[str, object], key: str) -> None:
        count = await self.async_get(key, 0)
        current = int(count) if isinstance(count, (int, float)) else 0
        await self.async_set(key, current + 1)
        payload = _payload(invocation)
        if payload.get("chat_id") and payload.get("notify"):
            await self.client.send_message(
                int(payload["chat_id"]),
                f"Jobs Lab: {key} tick #{current + 1}",
                idempotency_key=f"example.jobs:{key}:{current + 1}",
            )

    @loader.job(
        name="micro_timer",
        description="Fast fixed-rate counter used for queue and status checks",
        max_runtime_ms=10_000,
        overlap="skip_if_running",
    )
    async def micro_timer(self, invocation: Mapping[str, object]) -> dict[str, object]:
        await self._counter_job(invocation, "counter:micro_timer")
        return {}

    @loader.job(
        name="fixed_delay_probe",
        description="Fixed-delay probe; next run is calculated after completion",
        max_runtime_ms=10_000,
        overlap="queue_one_bounded",
    )
    async def fixed_delay_probe(
        self, invocation: Mapping[str, object]
    ) -> dict[str, object]:
        await self._bounded_sleep(invocation)
        await self._counter_job(invocation, "counter:fixed_delay")
        return {}

    @loader.job(
        name="fixed_rate_probe",
        description="Fixed-rate probe; missed timeline entries never backlog",
        max_runtime_ms=10_000,
        overlap="coalesce_one_pending",
    )
    async def fixed_rate_probe(
        self, invocation: Mapping[str, object]
    ) -> dict[str, object]:
        await self._bounded_sleep(invocation)
        await self._counter_job(invocation, "counter:fixed_rate")
        return {}

    @loader.job(
        name="calendar_probe",
        description="UTC calendar probe running at the start of a minute",
        max_runtime_ms=10_000,
        overlap="skip_if_running",
    )
    async def calendar_probe(
        self, invocation: Mapping[str, object]
    ) -> dict[str, object]:
        await self._counter_job(invocation, "counter:calendar")
        return {}

    @loader.job(
        name="todo_reminder",
        description="One-shot reminder that exercises client delivery and persistence",
        max_runtime_ms=10_000,
        overlap="skip_if_running",
    )
    async def todo_reminder(
        self, invocation: Mapping[str, object]
    ) -> dict[str, object]:
        payload = _payload(invocation)
        chat_id = int(payload.get("chat_id", 0))
        todo_id = str(payload.get("todo_id", "todo"))
        text = str(payload.get("text", "Todo reminder"))[:_MAX_TODO_TEXT]
        if chat_id:
            await self.client.send_message(
                chat_id,
                f"⏰ {text}",
                idempotency_key=f"example.jobs:todo:{todo_id}",
            )
        await self.async_set(
            f"todo:last:{todo_id}", {"text": text, "sent_at_ms": _now_ms()}
        )
        return {}

    @loader.job(
        name="session_probe",
        description="Non-persistent one-shot for session-only lifecycle checks",
        persistent=False,
        max_runtime_ms=10_000,
        overlap="skip_if_running",
    )
    async def session_probe(
        self, invocation: Mapping[str, object]
    ) -> dict[str, object]:
        await self._counter_job(invocation, "counter:session")
        return {}

    async def _bounded_sleep(self, invocation: Mapping[str, object]) -> None:
        milliseconds = _payload(invocation).get("sleep_ms", 0)
        try:
            delay = min(max(int(milliseconds), 0), 5_000) / 1000
        except (TypeError, ValueError):
            delay = 0
        if delay:
            await asyncio.sleep(delay)

    async def _find(self, job_id: str) -> Mapping[str, object] | None:
        result = await self.jobs.list(limit=100)
        jobs = result.get("jobs") if isinstance(result, Mapping) else None
        if not isinstance(jobs, list):
            return None
        for status in jobs:
            if isinstance(status, Mapping):
                identity = status.get("identity")
                if isinstance(identity, Mapping) and identity.get("id") == job_id:
                    return status
        return None

    async def _operate(self, operation: str, job_id: str) -> str:
        status = await self._find(job_id)
        if status is None:
            return f"job not found: {job_id}"
        identity = status["identity"]
        revision = int(status.get("revision", 0))
        if operation == "cancel":
            result = await self.jobs.cancel(identity, revision)
        elif operation == "pause":
            result = await self.jobs.pause(identity, revision)
        elif operation == "resume":
            result = await self.jobs.resume(identity, revision)
        elif operation == "trigger":
            result = await self.jobs.trigger(identity, revision)
        elif operation == "get":
            result = await self.jobs.get(identity)
        elif operation == "inspect":
            result = await self.jobs.inspect(identity, include_history=True)
        elif operation == "clear":
            result = await self.jobs.clear(identity, revision)
        elif operation == "delete":
            await self.jobs.delete(identity, revision)
            return f"deleted {job_id}"
        else:
            return f"unsupported operation: {operation}"
        return self._format_status(result.get("status", result))

    async def _start_bundle(self, chat_id: int) -> list[str]:
        specs = (
            self._spec(
                "demo-micro",
                "micro_timer",
                _interval("fixed_rate", 2_000),
                payload={"chat_id": chat_id},
            ),
            self._spec(
                "demo-delay",
                "fixed_delay_probe",
                _interval("fixed_delay", 5_000),
                overlap="queue_one_bounded",
                payload={"sleep_ms": 250},
            ),
            self._spec(
                "demo-rate",
                "fixed_rate_probe",
                _interval("fixed_rate", 3_000),
                overlap="coalesce_one_pending",
                payload={"sleep_ms": 250},
            ),
            self._spec("demo-calendar", "calendar_probe", _calendar()),
            self._spec(
                "demo-session",
                "session_probe",
                _interval("fixed_delay", 60_000),
                persistent=False,
            ),
        )
        lines: list[str] = []
        for spec in specs:
            try:
                status = await self.jobs.create(spec)
                lines.append(
                    f"created {spec.job_id}: {self._status_value(status).get('state', '?')}"
                )
            except SerenoxError as error:
                lines.append(f"{spec.job_id}: {type(error).__name__}: {error}")
        return lines

    @loader.owner
    @loader.command(name="jobsdemo", aliases=("jobs",))
    async def jobsdemo(self, message: Message) -> None:
        """Run `.jobsdemo help` for the complete manual smoke-test flow."""

        parts = message.text.strip().split()
        action = parts[1].lower() if len(parts) > 1 else "help"
        try:
            if action == "start":
                lines = await self._start_bundle(message.chat_id)
            elif action == "status":
                result = await self.jobs.list(limit=100)
                lines = [self._format_status(item) for item in result.get("jobs", [])]
            elif (
                action
                in {
                    "cancel",
                    "pause",
                    "resume",
                    "trigger",
                    "get",
                    "inspect",
                    "clear",
                    "delete",
                }
                and len(parts) == 3
            ):
                lines = [await self._operate(action, parts[2])]
            elif action == "update" and len(parts) == 3:
                status = await self._find(parts[2])
                if status is None:
                    lines = [f"job not found: {parts[2]}"]
                else:
                    identity = status["identity"]
                    request = {
                        "identity": identity,
                        "revision": int(status.get("revision", 0)),
                        "schedule": _interval("fixed_rate", 1_000),
                    }
                    result = await self.jobs.update(request)
                    lines = [self._format_status(result)]
            elif action == "todo" and len(parts) >= 4 and parts[2] == "add":
                seconds = min(max(int(parts[3]), 1), 366 * 24 * 60 * 60)
                text = " ".join(parts[4:]).strip()[:_MAX_TODO_TEXT]
                if not text:
                    raise ValueError("todo text is required")
                todo_id = f"todo-{message.chat_id}-{_now_ms()}"
                status = await self.jobs.create(
                    self._spec(
                        todo_id,
                        "todo_reminder",
                        _one_shot(_now_ms() + seconds * 1000),
                        payload={
                            "chat_id": message.chat_id,
                            "todo_id": todo_id,
                            "text": text,
                        },
                    )
                )
                lines = [
                    f"todo scheduled: {self._format_status(self._status_value(status))}"
                ]
            elif action == "todo" and len(parts) == 3 and parts[2] == "list":
                result = await self.jobs.list(limit=100)
                lines = [
                    self._format_status(item)
                    for item in result.get("jobs", [])
                    if str(item.get("identity", {}).get("id", "")).startswith("todo-")
                ]
            elif action == "cleanup":
                result = await self.jobs.list(limit=100)
                lines = []
                for item in result.get("jobs", []):
                    identity = item.get("identity", {})
                    if isinstance(identity, Mapping):
                        await self.jobs.delete(identity, int(item.get("revision", 0)))
                        lines.append(f"deleted {identity.get('id', '?')}")
            else:
                lines = [
                    "usage: .jobsdemo start|status|cancel ID|pause ID|resume ID|",
                    "trigger ID|get ID|inspect ID|update ID|clear ID|delete ID|cleanup",
                    ".jobsdemo todo add SECONDS TEXT|todo list",
                ]
        except (TypeError, ValueError, KeyError) as error:
            lines = [f"invalid jobsdemo request: {error}"]
        # Surface structured host.jobs failures in the lab.
        except SerenoxError as error:
            lines = [f"host.jobs error: {type(error).__name__}: {error}"]
        await message.reply("\n".join(lines)[:4096], parse_mode="plain")

    @staticmethod
    def _status_value(result: Mapping[str, object]) -> Mapping[str, object]:
        status = result.get("status", result)
        return status if isinstance(status, Mapping) else {}

    @staticmethod
    def _format_status(status: Mapping[str, object]) -> str:
        identity = status.get("identity", {})
        job_id = identity.get("id", "?") if isinstance(identity, Mapping) else "?"
        return (
            f"{job_id}: state={status.get('state', '?')} rev={status.get('revision', '?')} "
            f"runs={status.get('runs', 0)} missed={status.get('missed', 0)} "
            f"rejected={status.get('rejected', 0)} active={status.get('active', 0)}"
        )


__all__ = ["JobsLab"]
