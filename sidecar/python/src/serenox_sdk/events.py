"""Typed Serenox Event Fabric declarations, publication, and outcomes."""

from __future__ import annotations

import dataclasses
from collections.abc import Callable, Mapping
from typing import Any

from .actions import ActionSink
from .protocol import JSONValue, OperationKind, OperationV1


@dataclasses.dataclass(frozen=True, slots=True)
class EventPublication:
    type: str
    version: int = 1
    description: str = ""
    max_payload_bytes: int = 65_536
    delivery_class: str = "durable"
    schema: str = ""

    def to_dict(self) -> dict[str, JSONValue]:
        return dataclasses.asdict(self)


@dataclasses.dataclass(frozen=True, slots=True)
class EventSubscription:
    type: str
    handler_id: str
    version: int = 1
    source_policy: str = "any_public_plugin"
    source_plugin: str = ""
    delivery_class: str = "durable"
    max_runtime_ms: int = 30_000
    max_attempts: int = 5
    initial_delay_ms: int = 250
    max_delay_ms: int = 30_000
    accept_replay: bool = True

    def to_dict(self) -> dict[str, JSONValue]:
        return {
            "type": self.type,
            "version": self.version,
            "handler_id": self.handler_id,
            "source_policy": self.source_policy,
            "source_plugin": self.source_plugin,
            "delivery_class": self.delivery_class,
            "max_runtime_ms": self.max_runtime_ms,
            "retry": {
                "max_attempts": self.max_attempts,
                "initial_delay_ms": self.initial_delay_ms,
                "max_delay_ms": self.max_delay_ms,
            },
            "accept_replay": self.accept_replay,
        }


def event_handler(handler_id: str) -> Callable[[Callable[..., Any]], Callable[..., Any]]:
    def decorate(callback: Callable[..., Any]) -> Callable[..., Any]:
        callback.__serenox_event_handler__ = handler_id  # type: ignore[missing-attribute]
        return callback

    return decorate


class EventsProxy:
    def __init__(self, sink: ActionSink) -> None:
        self._sink = sink

    async def publish(
        self,
        event_type: str,
        payload: JSONValue,
        *,
        version: int = 1,
        dedupe_key: str = "",
        correlation_id: str = "",
        causation_id: str = "",
        trace_id: str = "",
    ) -> Mapping[str, JSONValue]:
        result = await self._sink.request(
            OperationV1(
                kind=OperationKind.EVENT_PUBLISH,
                target={},
                parameters={
                    "type": event_type,
                    "version": version,
                    "payload": payload,
                    "dedupe_key": dedupe_key,
                    "correlation_id": correlation_id,
                    "causation_id": causation_id,
                    "trace_id": trace_id,
                    "content_type": "application/json",
                },
            )
        )
        if not isinstance(result, Mapping):
            raise TypeError("host.events result must be an object")
        return result


def event_success() -> dict[str, JSONValue]:
    return {"event_result": {"outcome": "acknowledged"}}


def event_retry(code: str, message: str = "") -> dict[str, JSONValue]:
    return {"event_result": {"outcome": "retryable_failure", "code": code, "message": message}}


def event_reject(code: str, message: str = "") -> dict[str, JSONValue]:
    return {"event_result": {"outcome": "terminal_failure", "code": code, "message": message}}


def event_ambiguous(code: str, message: str = "") -> dict[str, JSONValue]:
    return {"event_result": {"outcome": "ambiguous_outcome", "code": code, "message": message}}
