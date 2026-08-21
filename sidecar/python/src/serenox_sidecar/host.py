"""Bounded lifecycle and dispatch host for one isolated Python module."""

from __future__ import annotations

import asyncio
import contextlib
import contextvars
import dataclasses
import enum
import functools
import hmac
import importlib
import importlib.util
import inspect
import json
import re
import sys
import time
from collections.abc import Callable, Mapping, Sequence
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path
from types import ModuleType
from typing import Any

from serenox_sdk.actions import (
    ActionSink,
    CallbackQuery,
    CallbackRegistry,
    InlineQuery,
    Message,
)
from serenox_sdk.errors import (
    ConfigurationError,
    ConnectionClosedError,
    OverloadedError,
    ProtocolError,
    SerenoxError,
    UnsupportedAPIError,
)
from serenox_sdk.module import Module, ModuleRegistry, RegisteredHandler, RuntimeContext
from serenox_sdk.protocol import (
    MAX_ACTIONS,
    MAX_COLLECTION_ITEMS,
    MAX_STRING_BYTES,
    PROTOCOL_NAME,
    PROTOCOL_VERSION,
    HandlerKind,
    JSONValue,
    Method,
    OperationKind,
    OperationV1,
    RequestV1,
    ensure_json_value,
)
from serenox_sdk.storage import DatabaseProxy

from .framing import DEFAULT_MAX_FRAME_BYTES, FramedJSONStream
from .transport import TransportConnector, connector_for


class HostState(str, enum.Enum):
    LOADED = "loaded"
    NEGOTIATED = "negotiated"
    CONFIGURED = "configured"
    RUNNING = "running"
    STOPPING = "stopping"
    STOPPED = "stopped"


@dataclasses.dataclass(frozen=True, slots=True)
class HostSettings:
    queue_capacity: int = 64
    worker_count: int = 2
    response_capacity: int = 64
    callback_capacity: int = 256
    max_actions: int = 128
    max_action_bytes: int = 256 * 1_024
    max_frame_bytes: int = DEFAULT_MAX_FRAME_BYTES
    max_handler_timeout_seconds: float = 30.0
    shutdown_timeout_seconds: float = 5.0
    storage_entries: int = 1_024
    storage_value_bytes: int = 256 * 1_024

    def __post_init__(self) -> None:
        bounds = {
            "queue_capacity": (self.queue_capacity, 1, 65_536),
            "worker_count": (self.worker_count, 1, 32),
            "response_capacity": (self.response_capacity, 1, 65_536),
            "callback_capacity": (self.callback_capacity, 1, 65_536),
            "max_actions": (self.max_actions, 1, MAX_ACTIONS),
            "max_action_bytes": (self.max_action_bytes, 256, 1 << 20),
            "max_frame_bytes": (self.max_frame_bytes, 256, 64 << 20),
            "storage_entries": (self.storage_entries, 1, 1_000_000),
            "storage_value_bytes": (self.storage_value_bytes, 1, 16 << 20),
        }
        for name, (value, minimum, maximum) in bounds.items():
            if value < minimum or value > maximum:
                raise ValueError(f"{name} must be between {minimum} and {maximum}")
        if not 0.01 <= self.max_handler_timeout_seconds <= 300:
            raise ValueError("max_handler_timeout_seconds must be between 0.01 and 300")
        if not 0.01 <= self.shutdown_timeout_seconds <= 60:
            raise ValueError("shutdown_timeout_seconds must be between 0.01 and 60")
        if self.max_action_bytes > self.max_frame_bytes:
            raise ValueError("max_action_bytes cannot exceed max_frame_bytes")


@dataclasses.dataclass(slots=True)
class _WorkItem:
    request: dict[str, JSONValue]


@dataclasses.dataclass(slots=True)
class _ResponseItem:
    response: dict[str, JSONValue]
    close_after: bool = False


class _ActionCollector:
    def __init__(self, capacity: int, max_action_bytes: int, max_total_bytes: int) -> None:
        self.capacity = capacity
        self.max_action_bytes = max_action_bytes
        self.max_total_bytes = max_total_bytes
        self.actions: list[dict[str, JSONValue]] = []
        self.sequence = 0
        self.total_bytes = 0

    def append(self, actions: Sequence[dict[str, JSONValue]]) -> None:
        if len(self.actions) + len(actions) > self.capacity:
            raise OverloadedError(
                f"handler produced more than the configured {self.capacity} actions"
            )
        encoded_sizes = [
            len(
                json.dumps(
                    action, ensure_ascii=False, allow_nan=False, separators=(",", ":")
                ).encode("utf-8")
            )
            for action in actions
        ]
        too_large = next((size for size in encoded_sizes if size > self.max_action_bytes), None)
        if too_large is not None:
            raise OverloadedError(
                f"action is {too_large} bytes; configured limit is {self.max_action_bytes}"
            )
        if self.total_bytes + sum(encoded_sizes) > self.max_total_bytes:
            raise OverloadedError("action batch exceeds the negotiated bounded response allowance")
        self.actions.extend(actions)
        self.total_bytes += sum(encoded_sizes)

    def action_id(self) -> str:
        self.sequence += 1
        return f"py-{self.sequence}"


_active_collector: contextvars.ContextVar[_ActionCollector | None] = contextvars.ContextVar(
    "serenox_active_action_collector", default=None
)


class HostActionSink(ActionSink):
    """Collect actions into the current bounded lifecycle/dispatch response."""

    def __init__(self, max_actions: int) -> None:
        self.max_actions = max_actions

    async def request(self, action: OperationV1) -> JSONValue:
        self.notify(action)
        return self._provisional_result(action)

    def notify(self, action: OperationV1) -> None:
        collector = _active_collector.get()
        if collector is None:
            raise UnsupportedAPIError(
                "background Python plugin actions",
                "an action awaited inside a lifecycle hook or dispatched handler",
            )
        actions = self._wire_actions(action, collector)
        actions = [_compact_wire_action(wire_action) for wire_action in actions]
        for wire_action in actions:
            _validate_wire_action(wire_action)
        collector.append(actions)

    def _wire_actions(
        self, action: OperationV1, collector: _ActionCollector
    ) -> list[dict[str, JSONValue]]:
        target = action.target
        parameters = action.parameters
        action_id = collector.action_id()
        chat_id = _optional_int(target.get("chat_id"), 0)
        message_id = _optional_int(target.get("message_id"), 0)
        text = _optional_string(parameters.get("text"), "")
        parse_mode = _optional_string(parameters.get("parse_mode"), "")
        base: dict[str, JSONValue] = {"id": action_id}

        if action.kind is OperationKind.REPLY:
            return [
                {
                    **base,
                    "kind": "reply",
                    "chat_id": chat_id,
                    "reply_to_message_id": message_id,
                    "text": text,
                    "parse_mode": parse_mode,
                    "buttons": _wire_buttons(parameters.get("reply_markup")),
                }
            ]
        if action.kind is OperationKind.EDIT:
            return [
                {
                    **base,
                    "kind": "edit",
                    "chat_id": chat_id,
                    "message_id": message_id,
                    "text": text,
                    "parse_mode": parse_mode,
                    "buttons": _wire_buttons(parameters.get("reply_markup")),
                }
            ]
        if action.kind is OperationKind.DELETE:
            ids_value = parameters.get("message_ids")
            ids = (
                [item for item in ids_value if isinstance(item, int)]
                if isinstance(ids_value, list)
                else [message_id]
            )
            return [
                {
                    "id": action_id if index == 0 else collector.action_id(),
                    "kind": "delete",
                    "chat_id": chat_id,
                    "message_id": current_id,
                }
                for index, current_id in enumerate(ids)
            ]
        if action.kind is OperationKind.SEND:
            return [
                {
                    **base,
                    "kind": "send",
                    "chat_id": chat_id,
                    "reply_to_message_id": _optional_int(parameters.get("reply_to_message_id"), 0),
                    "text": text,
                    "parse_mode": parse_mode,
                    "buttons": _wire_buttons(parameters.get("reply_markup")),
                }
            ]
        if action.kind is OperationKind.INLINE_ANSWER:
            inline_query_id = _optional_string(target.get("query_id"), "")
            callback_id = _optional_string(target.get("callback_id"), "")
            return [
                {
                    **base,
                    "kind": "inline_answer",
                    "inline_query_id": inline_query_id,
                    "callback_id": callback_id,
                    "text": text,
                    "inline_results": _wire_inline_results(parameters.get("results")),
                }
            ]
        if action.kind is OperationKind.INLINE_FORM and chat_id:
            return [
                {
                    **base,
                    "kind": "send",
                    "chat_id": chat_id,
                    "reply_to_message_id": message_id,
                    "text": text,
                    "parse_mode": "html",
                    "buttons": _wire_buttons(parameters.get("reply_markup")),
                }
            ]
        if action.kind in {
            OperationKind.GET_MESSAGE,
            OperationKind.GET_ME,
            OperationKind.RESOLVE_PEER,
        }:
            raise UnsupportedAPIError(
                action.kind.value,
                "a future duplex host.client RPC; protocol v1 response batches cannot return reads",
            )
        return [self._wire_client_action(action, action_id)]

    def _wire_client_action(self, action: OperationV1, action_id: str) -> dict[str, JSONValue]:
        target = action.target
        parameters = action.parameters
        operation_name = action.kind.value.removeprefix("client.")
        if action.kind in {
            OperationKind.STORAGE_GET,
            OperationKind.STORAGE_SET,
            OperationKind.STORAGE_DELETE,
        }:
            if action.kind is OperationKind.STORAGE_GET:
                raise UnsupportedAPIError(
                    "storage.get",
                    "Module.get from the configured startup snapshot or a future duplex "
                    "host.storage RPC",
                )
            operation_name = action.kind.value
        payload = {
            "target": ensure_json_value(target, field="action.target"),
            "parameters": ensure_json_value(parameters, field="action.parameters"),
        }
        peer = target.get("chat", target.get("peer", target.get("chat_id", 0)))
        return {
            "id": action_id,
            "kind": "client_operation",
            "client_operation": {
                "name": operation_name,
                "peer_id": peer if isinstance(peer, int) else 0,
                "message_id": _optional_int(target.get("message_id"), 0),
                "value": json.dumps(payload, ensure_ascii=False, separators=(",", ":")),
            },
        }

    def _provisional_result(self, action: OperationV1) -> JSONValue:
        target = action.target
        parameters = action.parameters
        if action.kind in {OperationKind.DELETE, OperationKind.LEAVE_CHAT, OperationKind.REACT}:
            return True
        if action.kind in {
            OperationKind.INLINE_ANSWER,
            OperationKind.INLINE_EDIT,
            OperationKind.INLINE_DELETE,
            OperationKind.STORAGE_SET,
            OperationKind.STORAGE_DELETE,
        }:
            return True
        if action.kind in {OperationKind.REPLY, OperationKind.EDIT, OperationKind.SEND}:
            return {
                "message": {
                    "id": _optional_int(target.get("message_id"), 0),
                    "chat_id": _optional_int(target.get("chat_id"), 0),
                    "text": _optional_string(parameters.get("text"), ""),
                    "outgoing": True,
                }
            }
        return {}


class SidecarHost:
    """One-module host with fixed queues, workers, deadlines, and clean ownership."""

    def __init__(
        self,
        module: Module,
        *,
        auth_token: str,
        instance_id: str,
        settings: HostSettings | None = None,
        clock: Callable[[], float] = time.monotonic,
        wall_clock: Callable[[], float] = time.time,
    ) -> None:
        if not auth_token:
            raise ValueError("auth_token must not be empty")
        if not instance_id:
            raise ValueError("instance_id must not be empty")
        self.settings = settings or HostSettings()
        self.auth_token = auth_token
        self.instance_id = instance_id
        self.clock = clock
        self.wall_clock = wall_clock
        self.state = HostState.LOADED
        self.started_at = clock()
        self._sink = HostActionSink(self.settings.max_actions)
        self._callbacks = CallbackRegistry(self.settings.callback_capacity)
        self._database = DatabaseProxy(
            self._sink,
            max_entries=self.settings.storage_entries,
            max_value_bytes=self.settings.storage_value_bytes,
        )
        module._bind(
            RuntimeContext(
                sink=self._sink,
                database=self._database,
                callbacks=self._callbacks,
                clock=clock,
            )
        )
        self.module = module
        self.registry = ModuleRegistry(module, instance_id=instance_id)
        self._requests: asyncio.Queue[_WorkItem] = asyncio.Queue(
            maxsize=self.settings.queue_capacity
        )
        self._responses: asyncio.Queue[_ResponseItem | None] = asyncio.Queue(
            maxsize=self.settings.response_capacity
        )
        self._state_lock = asyncio.Lock()
        self._shutdown = asyncio.Event()
        self._executor = ThreadPoolExecutor(
            max_workers=self.settings.worker_count,
            thread_name_prefix="serenox-python-plugin",
        )
        self._metrics: dict[str, int] = {
            "accepted": 0,
            "completed": 0,
            "overloaded": 0,
            "failed": 0,
            "timed_out": 0,
            "malformed": 0,
        }
        self._tasks: set[asyncio.Task[Any]] = set()
        self._negotiated_frame_bytes = self.settings.max_frame_bytes
        self._unloaded = False

    async def connect_and_serve(
        self, address: str, connector: TransportConnector | None = None
    ) -> None:
        selected_connector = connector or connector_for(address)
        connection = await selected_connector.connect(address)
        await self.serve(connection.reader, connection.writer)

    async def serve(self, reader: asyncio.StreamReader, writer: asyncio.StreamWriter) -> None:
        stream = FramedJSONStream(reader, writer, max_frame_bytes=self.settings.max_frame_bytes)
        writer_task: asyncio.Task[None] | None = None
        worker_tasks: list[asyncio.Task[None]] = []
        try:
            await self._handshake_exchange(stream)
            stream.max_frame_bytes = self._negotiated_frame_bytes
            writer_task = asyncio.create_task(self._writer_loop(stream), name="sidecar-writer")
            worker_tasks = [
                asyncio.create_task(self._worker_loop(), name=f"sidecar-worker-{index}")
                for index in range(self.settings.worker_count)
            ]
            self._tasks = {writer_task, *worker_tasks}
            await self._reader_loop(stream)
        except ConnectionClosedError:
            pass
        finally:
            self._shutdown.set()
            with contextlib.suppress(TimeoutError):
                await asyncio.wait_for(
                    self._requests.join(), timeout=self.settings.shutdown_timeout_seconds
                )
            for task in worker_tasks:
                task.cancel()
            await asyncio.gather(*worker_tasks, return_exceptions=True)
            if writer_task is not None and not writer_task.done():
                try:
                    self._responses.put_nowait(None)
                except asyncio.QueueFull:
                    writer_task.cancel()
            if writer_task is not None:
                try:
                    await asyncio.wait_for(
                        asyncio.shield(writer_task), timeout=self.settings.shutdown_timeout_seconds
                    )
                except (TimeoutError, asyncio.CancelledError):
                    writer_task.cancel()
                    await asyncio.gather(writer_task, return_exceptions=True)
            await self._unload_after_disconnect()
            await stream.close()
            self._executor.shutdown(wait=False, cancel_futures=True)
            self._tasks.clear()
            self.state = HostState.STOPPED

    async def _handshake_exchange(self, stream: FramedJSONStream) -> None:
        request = await stream.read()
        try:
            self._validate_request(request)
            if request.get("method") != Method.HANDSHAKE.value:
                raise _InvalidStateError("first request must be handshake")
            response, _ = await self._process_request(request)
        except Exception as exc:
            if _has_response_identity(request):
                await stream.write(self._exception_response(request, exc))
            raise
        stream.max_frame_bytes = self._negotiated_frame_bytes
        await stream.write(response)

    async def _reader_loop(self, stream: FramedJSONStream) -> None:
        while not self._shutdown.is_set():
            request: dict[str, JSONValue] | None = None
            try:
                request = await stream.read()
                self._validate_request(request)
            except ConnectionClosedError:
                raise
            except ProtocolError as exc:
                self._metrics["malformed"] += 1
                if request is not None and _has_response_identity(request):
                    await self._responses.put(
                        _ResponseItem(self._error_response(request, "invalid_request", str(exc)))
                    )
                    continue
                raise
            method = _required_string(request.get("method"), "method")
            if method == Method.HANDSHAKE.value:
                await self._responses.put(
                    _ResponseItem(
                        self._error_response(
                            request,
                            "invalid_state",
                            "handshake is already complete",
                        )
                    )
                )
                continue
            if method == Method.DISPATCH.value:
                if self.state is not HostState.RUNNING:
                    await self._responses.put(
                        _ResponseItem(
                            self._error_response(
                                request, "invalid_state", "dispatch requires started state"
                            )
                        )
                    )
                    continue
                try:
                    self._requests.put_nowait(_WorkItem(request))
                    self._metrics["accepted"] += 1
                except asyncio.QueueFull:
                    self._metrics["overloaded"] += 1
                    await self._responses.put(
                        _ResponseItem(
                            self._error_response(
                                request,
                                "overloaded",
                                "Python plugin dispatch queue is full",
                                retryable=True,
                            )
                        )
                    )
                continue
            if method == Method.STOP.value:
                with contextlib.suppress(TimeoutError):
                    await asyncio.wait_for(
                        self._requests.join(), timeout=self.settings.shutdown_timeout_seconds
                    )
            try:
                response, close_after = await self._process_request(request)
            except Exception as exc:
                self._metrics["failed"] += 1
                response = self._exception_response(request, exc)
                close_after = False
            await self._responses.put(_ResponseItem(response, close_after=close_after))
            if close_after:
                return

    async def _writer_loop(self, stream: FramedJSONStream) -> None:
        while True:
            item = await self._responses.get()
            try:
                if item is None:
                    return
                try:
                    await stream.write(item.response)
                except (ProtocolError, ConnectionError, OSError):
                    self._shutdown.set()
                    await stream.close()
                    raise
                if item.close_after:
                    self._shutdown.set()
                    await stream.close()
                    return
            finally:
                self._responses.task_done()

    async def _worker_loop(self) -> None:
        while True:
            item = await self._requests.get()
            try:
                response, close_after = await self._process_request(item.request)
                await self._responses.put(_ResponseItem(response, close_after=close_after))
                self._metrics["completed"] += 1
            except Exception as exc:
                self._metrics["failed"] += 1
                await self._responses.put(
                    _ResponseItem(self._exception_response(item.request, exc))
                )
            finally:
                self._requests.task_done()

    async def _process_request(
        self, request: Mapping[str, JSONValue]
    ) -> tuple[dict[str, JSONValue], bool]:
        method = str(request["method"])
        payload = _object(request.get("payload"), "payload")
        deadline_ms = _required_int(request.get("deadline_unix_ms"), "deadline_unix_ms")
        remaining = deadline_ms / 1000 - self.wall_clock()
        if remaining <= 0:
            self._metrics["timed_out"] += 1
            return (
                self._error_response(
                    request,
                    "deadline_exceeded",
                    "request deadline elapsed before Python execution began",
                    retryable=True,
                ),
                False,
            )
        timeout = min(remaining, self.settings.max_handler_timeout_seconds)
        collector = _ActionCollector(
            self.settings.max_actions,
            self.settings.max_action_bytes,
            max(256, self._negotiated_frame_bytes - 4_096),
        )
        token = _active_collector.set(collector)
        try:
            result = await asyncio.wait_for(self._dispatch_method(method, payload), timeout=timeout)
        except TimeoutError:
            self._metrics["timed_out"] += 1
            return (
                self._error_response(
                    request,
                    "deadline_exceeded",
                    f"Python handler exceeded its {timeout:.3f}-second deadline",
                    retryable=False,
                ),
                False,
            )
        finally:
            _active_collector.reset(token)
        if collector.actions and (
            method.startswith("lifecycle.") or method == Method.DISPATCH.value
        ):
            result["actions"] = ensure_json_value(collector.actions, field="actions")
        close_after = method == Method.STOP.value
        return self._success_response(request, result), close_after

    async def _dispatch_method(
        self, method: str, payload: Mapping[str, JSONValue]
    ) -> dict[str, JSONValue]:
        if method == Method.HANDSHAKE.value:
            return await self._handshake(payload)
        if method == Method.CONFIGURE.value:
            return await self._configure(payload)
        if method == Method.START.value:
            return await self._start(payload)
        if method == Method.STOP.value:
            return await self._stop(payload)
        if method == Method.DISPATCH.value:
            return await self._dispatch_update(payload)
        if method == Method.HEALTH.value:
            return self._health(payload)
        if method.startswith("host."):
            raise UnsupportedAPIError(
                method,
                "a negotiated duplex core transport; this connection currently accepts "
                "host requests",
            )
        raise UnsupportedAPIError(method, "one of the protocol v1 lifecycle methods")

    async def _handshake(self, payload: Mapping[str, JSONValue]) -> dict[str, JSONValue]:
        async with self._state_lock:
            if self.state is not HostState.LOADED:
                raise _InvalidStateError(
                    f"handshake is invalid while host state is {self.state.value}"
                )
            versions = payload.get("supported_versions")
            if (
                not isinstance(versions, list)
                or not 1 <= len(versions) <= 16
                or any(not isinstance(version, str) for version in versions)
            ):
                raise ProtocolError("supported_versions must contain 1..16 strings")
            if PROTOCOL_VERSION not in versions:
                raise _IncompatibleProtocolError(
                    f"core does not offer required protocol version {PROTOCOL_VERSION}"
                )
            token = _required_string(payload.get("auth_token"), "auth_token")
            if not hmac.compare_digest(token, self.auth_token):
                raise _AuthenticationError("sidecar authentication token did not match")
            instance_id = _required_string(payload.get("instance_id"), "instance_id")
            if instance_id != self.instance_id:
                raise _AuthenticationError("sidecar instance ID did not match")
            nonce = _required_string(payload.get("nonce"), "nonce")
            requested_frame = _required_int(payload.get("max_frame_bytes"), "max_frame_bytes")
            if requested_frame <= 0:
                raise ProtocolError("max_frame_bytes must be positive")
            host_version = payload.get("host_version")
            if host_version is not None and not isinstance(host_version, str):
                raise ProtocolError("host_version must be a string")
            self._negotiated_frame_bytes = min(requested_frame, self.settings.max_frame_bytes)
            self.state = HostState.NEGOTIATED
            return {
                "selected_version": PROTOCOL_VERSION,
                "nonce": nonce,
                "max_frame_bytes": self._negotiated_frame_bytes,
                "metadata": self._wire_metadata(),
            }

    async def _configure(self, payload: Mapping[str, JSONValue]) -> dict[str, JSONValue]:
        async with self._state_lock:
            if self.state is not HostState.NEGOTIATED:
                raise _InvalidStateError(
                    f"configure is invalid while host state is {self.state.value}"
                )
            phase = payload.get("phase", "configure")
            if phase != "configure":
                raise ProtocolError("configure payload phase must be 'configure'")
            raw_config = payload.get("config", [])
            if not isinstance(raw_config, list):
                raise ProtocolError("lifecycle config must be a list")
            if len(raw_config) > MAX_COLLECTION_ITEMS:
                raise ProtocolError(f"lifecycle config exceeds {MAX_COLLECTION_ITEMS} entries")
            schema = {item.key: item for item in self.module.config.schema()}
            values: dict[str, Any] = {}
            for index, entry in enumerate(raw_config):
                item = _object(entry, f"config[{index}]")
                key = _required_string(item.get("key"), f"config[{index}].key")
                configured_type = _required_string(item.get("type"), f"config[{index}].type")
                expected = schema.get(key)
                if expected is None:
                    raise ConfigurationError(f"unknown configuration option {key!r}")
                expected_type = (
                    expected.type.value if hasattr(expected.type, "value") else str(expected.type)
                )
                if configured_type != expected_type:
                    raise ConfigurationError(
                        f"configuration option {key!r} has type {configured_type!r}; "
                        f"expected {expected_type!r}"
                    )
                if "value" not in item:
                    raise ProtocolError(f"config[{index}].value is required")
                values[key] = item.get("value")
            self.module.config.apply(values)
            await self._call(self.module.config_complete)
            self.state = HostState.CONFIGURED
            return {"state": self.state.value}

    async def _start(self, payload: Mapping[str, JSONValue]) -> dict[str, JSONValue]:
        async with self._state_lock:
            if self.state is not HostState.CONFIGURED:
                raise _InvalidStateError(f"start is invalid while host state is {self.state.value}")
            if payload.get("phase", "start") != "start":
                raise ProtocolError("start payload phase must be 'start'")
            await self._call(self.module.client_ready)
            self.state = HostState.RUNNING
            return {"state": "ready"}

    async def _stop(self, payload: Mapping[str, JSONValue]) -> dict[str, JSONValue]:
        async with self._state_lock:
            if self.state not in {HostState.CONFIGURED, HostState.RUNNING}:
                raise _InvalidStateError(f"stop is invalid while host state is {self.state.value}")
            if payload.get("phase", "stop") != "stop":
                raise ProtocolError("stop payload phase must be 'stop'")
            await self._call(self.module.on_unload)
            self._unloaded = True
            self.state = HostState.STOPPED
            return {"state": HostState.STOPPED.value}

    async def _dispatch_update(self, payload: Mapping[str, JSONValue]) -> dict[str, JSONValue]:
        if self.state is not HostState.RUNNING:
            raise _InvalidStateError(f"dispatch is invalid while host state is {self.state.value}")
        update = _object(payload.get("update"), "update")
        _required_string(update.get("id"), "update.id")
        _required_int(update.get("occurred_unix_ms"), "update.occurred_unix_ms")
        command_value = payload.get("command")
        if command_value is not None:
            command = _object(command_value, "command")
            name = _required_string(command.get("name"), "command.name")
            for field_name in ("prefix", "raw_args"):
                field_value = command.get(field_name, "")
                if not isinstance(field_value, str):
                    raise ProtocolError(f"command.{field_name} must be a string")
            args = command.get("args", [])
            if (
                not isinstance(args, list)
                or len(args) > MAX_COLLECTION_ITEMS
                or any(not isinstance(argument, str) for argument in args)
            ):
                raise ProtocolError(
                    f"command.args must contain at most {MAX_COLLECTION_ITEMS} strings"
                )
            handler = self.registry.find(HandlerKind.COMMAND, name)
            if handler is None:
                raise ProtocolError(f"core dispatched unknown command {name!r}")
            message = Message.from_dict(
                _object(command.get("message"), "command.message"), self._sink
            )
            await self._invoke_handler(handler, message)
            for watcher_handler in self.registry.by_kind(HandlerKind.WATCHER):
                if _matches_filters(watcher_handler, message, is_command=True):
                    await self._invoke_handler(watcher_handler, message)
            return {}
        kind = _required_string(update.get("kind"), "update.kind")
        if kind in {"message", "edited_message"}:
            raw_message = _object(update.get("message"), "update.message")
            message = Message.from_dict(raw_message, self._sink)
            for handler in self.registry.by_kind(HandlerKind.WATCHER):
                if _matches_filters(handler, message, is_command=False):
                    await self._invoke_handler(handler, message)
            return {}
        if kind == "inline_query":
            raw_query = _object(update.get("inline_query"), "update.inline_query")
            query = InlineQuery(
                id=_required_string(raw_query.get("id"), "inline_query.id"),
                sender_id=_required_int(raw_query.get("sender_id"), "inline_query.sender_id"),
                query=_optional_string(raw_query.get("query"), ""),
                offset=_optional_string(raw_query.get("offset"), ""),
                _inline=self.module.inline,
            )
            for handler in self.registry.by_kind(HandlerKind.INLINE):
                await self._invoke_handler(handler, query)
            return {}
        if kind == "callback":
            raw_callback = _object(update.get("callback"), "update.callback")
            callback = CallbackQuery(
                id=_required_string(raw_callback.get("id"), "callback.id"),
                sender_id=_required_int(raw_callback.get("sender_id"), "callback.sender_id"),
                data=_optional_string(raw_callback.get("data"), ""),
                chat_id=_nullable_int(raw_callback.get("chat_id"), "callback.chat_id"),
                message_id=_nullable_int(raw_callback.get("message_id"), "callback.message_id"),
                _sink=self._sink,
            )
            registered = self._callbacks.resolve(callback.data, now=self.clock())
            if registered is not None:
                callback_function, args = registered
                await self._call(callback_function, callback, *args)
            else:
                for handler in self.registry.by_kind(HandlerKind.CALLBACK):
                    await self._invoke_handler(handler, callback)
            return {}
        if kind == "deleted_message":
            deleted_ids = update.get("deleted_ids")
            if (
                not isinstance(deleted_ids, list)
                or not 1 <= len(deleted_ids) <= MAX_COLLECTION_ITEMS
                or any(
                    isinstance(message_id, bool) or not isinstance(message_id, int)
                    for message_id in deleted_ids
                )
            ):
                raise ProtocolError(
                    f"update.deleted_ids must contain 1..{MAX_COLLECTION_ITEMS} integers"
                )
            return {}
        raise ProtocolError(f"unsupported update kind {kind!r}")

    async def _invoke_handler(self, handler: RegisteredHandler, argument: Any) -> Any:
        timeout = min(handler.spec.timeout_ms / 1000, self.settings.max_handler_timeout_seconds)
        try:
            return await asyncio.wait_for(self._call(handler.callback, argument), timeout=timeout)
        except TimeoutError:
            raise TimeoutError(
                f"handler {handler.spec.kind.value}:{handler.spec.name} exceeded {timeout}s"
            ) from None

    async def _call(self, callback: Callable[..., Any], *args: Any) -> Any:
        if inspect.iscoroutinefunction(callback):
            return await callback(*args)
        loop = asyncio.get_running_loop()
        context = contextvars.copy_context()
        result = await loop.run_in_executor(
            self._executor, functools.partial(context.run, callback, *args)
        )
        if inspect.isawaitable(result):
            return await result
        return result

    def _health(self, payload: Mapping[str, JSONValue]) -> dict[str, JSONValue]:
        detailed_value = payload.get("detailed", False)
        if not isinstance(detailed_value, bool):
            raise ProtocolError("health.detailed must be a boolean")
        detailed = detailed_value
        response: dict[str, JSONValue] = {
            "ready": self.state is HostState.RUNNING,
            "status": self.state.value,
            "uptime_ms": int((self.clock() - self.started_at) * 1000),
        }
        if detailed:
            response["metrics"] = ensure_json_value(self._metrics, field="metrics")
        return response

    def _wire_metadata(self) -> dict[str, JSONValue]:
        commands: list[JSONValue] = []
        watchers: list[JSONValue] = []
        for handler in self.registry.metadata.handlers:
            callback = getattr(self.module, handler.method)
            if handler.kind is HandlerKind.COMMAND:
                command: dict[str, JSONValue] = {"name": handler.name}
                if handler.aliases:
                    command["aliases"] = ensure_json_value(
                        list(handler.aliases), field="command.aliases"
                    )
                description = inspect.getdoc(callback) or ""
                if description:
                    command["description"] = description
                if getattr(callback, "__serenox_owner_only__", False):
                    command["owner_only"] = True
                commands.append(command)
            elif handler.kind is HandlerKind.WATCHER:
                watcher: dict[str, JSONValue] = {
                    "id": handler.name,
                    "filter": _wire_watcher_filter(handler.filters),
                }
                description = inspect.getdoc(callback) or ""
                if description:
                    watcher["description"] = description
                watchers.append(watcher)
        config: list[JSONValue] = []
        for item in self.module.config.schema():
            config_item: dict[str, JSONValue] = {
                "key": item.key,
                "type": item.type.value if hasattr(item.type, "value") else str(item.type),
                "default": item.default,
            }
            if item.description:
                config_item["description"] = item.description
            if item.required:
                config_item["required"] = True
            if item.secret:
                config_item["secret"] = True
            config.append(config_item)
        metadata: dict[str, JSONValue] = {
            "id": self.registry.metadata.id,
            "name": self.registry.metadata.name,
            "version": self.registry.metadata.version,
            "description": self.registry.metadata.description,
            "author": self.registry.metadata.author,
            "protocol_version": PROTOCOL_VERSION,
            "capabilities": ensure_json_value(
                list(self.registry.metadata.capabilities), field="metadata.capabilities"
            ),
            "commands": commands,
            "watchers": watchers,
            "config": config,
        }
        for field in (
            "description",
            "author",
            "capabilities",
            "commands",
            "watchers",
            "config",
        ):
            if metadata.get(field) in (None, "", [], {}):
                metadata.pop(field, None)
        checked = ensure_json_value(metadata, field="metadata")
        if not isinstance(checked, dict):
            raise ProtocolError("metadata must be a JSON object")
        return checked

    def _validate_request(self, request: Mapping[str, JSONValue]) -> None:
        if "payload" not in request:
            raise ProtocolError("payload is required by every protocol v1 method")
        RequestV1.from_dict(request)

    def _success_response(
        self, request: Mapping[str, JSONValue], result: Mapping[str, Any]
    ) -> dict[str, JSONValue]:
        response: dict[str, JSONValue] = {
            "protocol": PROTOCOL_NAME,
            "request_id": _required_int(request.get("request_id"), "request_id"),
            "kind": "response",
            "deadline_unix_ms": _required_int(request.get("deadline_unix_ms"), "deadline_unix_ms"),
            "result": ensure_json_value(result, field="result"),
        }
        self._ensure_response_size(response)
        return response

    def _error_response(
        self,
        request: Mapping[str, JSONValue],
        code: str,
        message: str,
        *,
        retryable: bool = False,
        field: str = "",
    ) -> dict[str, JSONValue]:
        error: dict[str, JSONValue] = {
            "code": code,
            "message": _truncate_utf8(message, MAX_STRING_BYTES),
        }
        if retryable:
            error["retryable"] = True
        if field:
            error["field"] = field
        return {
            "protocol": PROTOCOL_NAME,
            "request_id": _required_int(request.get("request_id"), "request_id"),
            "kind": "response",
            "deadline_unix_ms": _required_int(request.get("deadline_unix_ms"), "deadline_unix_ms"),
            "error": error,
        }

    def _exception_response(
        self, request: Mapping[str, JSONValue], exc: Exception
    ) -> dict[str, JSONValue]:
        if isinstance(exc, _AuthenticationError):
            code = "authentication_failed"
        elif isinstance(exc, _IncompatibleProtocolError):
            code = "incompatible_protocol"
        elif isinstance(exc, _InvalidStateError):
            code = "invalid_state"
        elif isinstance(exc, _InvalidResponseError):
            code = "invalid_response"
        elif isinstance(exc, OverloadedError):
            code = "overloaded"
        elif isinstance(exc, UnsupportedAPIError):
            code = "unsupported"
        elif isinstance(exc, (ProtocolError, ConfigurationError, TypeError, ValueError)):
            code = "invalid_request"
        elif isinstance(exc, TimeoutError):
            code = "deadline_exceeded"
        elif isinstance(exc, SerenoxError):
            code = "plugin_error"
        else:
            code = "plugin_error"
        response = self._error_response(request, code, str(exc), retryable=False)
        if isinstance(exc, UnsupportedAPIError):
            error = response.get("error")
            if isinstance(error, dict):
                details = {
                    "api": exc.api,
                    "replacement": exc.replacement
                    or "consult the protocol v1 compatibility matrix",
                }
                error["details"] = ensure_json_value(details, field="error.details")
        return response

    def _ensure_response_size(self, response: Mapping[str, JSONValue]) -> None:
        size = len(
            json.dumps(response, ensure_ascii=False, allow_nan=False, separators=(",", ":")).encode(
                "utf-8"
            )
        )
        if size > self._negotiated_frame_bytes:
            raise _InvalidResponseError(
                f"plugin response is {size} bytes; negotiated limit is "
                f"{self._negotiated_frame_bytes}"
            )

    async def _unload_after_disconnect(self) -> None:
        if self._unloaded or self.state not in {
            HostState.CONFIGURED,
            HostState.RUNNING,
            HostState.STOPPING,
        }:
            return
        self.state = HostState.STOPPING
        try:
            await asyncio.wait_for(
                self._call(self.module.on_unload),
                timeout=self.settings.shutdown_timeout_seconds,
            )
        except (Exception, asyncio.CancelledError):
            pass
        finally:
            self._unloaded = True

    @property
    def owned_task_count(self) -> int:
        return len(self._tasks)


class _AuthenticationError(ProtocolError):
    pass


class _IncompatibleProtocolError(ProtocolError):
    pass


class _InvalidStateError(ProtocolError):
    pass


class _InvalidResponseError(ProtocolError):
    pass


def load_plugin_module(target: str, class_name: str | None = None) -> Module:
    """Load exactly one Module subclass from an import name or a local .py file."""

    imported: ModuleType
    path = Path(target)
    if path.suffix == ".py" or path.exists():
        resolved = path.resolve(strict=True)
        module_name = f"serenox_plugin_{abs(hash(str(resolved))):x}"
        spec = importlib.util.spec_from_file_location(module_name, resolved)
        if spec is None or spec.loader is None:
            raise ImportError(f"cannot create an import spec for {resolved}")
        imported = importlib.util.module_from_spec(spec)
        sys.modules[module_name] = imported
        try:
            spec.loader.exec_module(imported)
        except Exception:
            sys.modules.pop(module_name, None)
            raise
    else:
        imported = importlib.import_module(target)
    candidates = [
        value
        for _, value in inspect.getmembers(imported, inspect.isclass)
        if issubclass(value, Module)
        and value is not Module
        and value.__module__ == imported.__name__
    ]
    if class_name is not None:
        candidates = [candidate for candidate in candidates if candidate.__name__ == class_name]
    if len(candidates) != 1:
        names = ", ".join(candidate.__name__ for candidate in candidates) or "none"
        raise ImportError(
            f"expected exactly one Serenox Module subclass in {target!r}; found {names}"
        )
    instance = candidates[0]()
    if not isinstance(instance, Module):
        raise TypeError("plugin class constructor returned a non-Module object")
    return instance


def _wire_watcher_filter(filters: Mapping[str, JSONValue]) -> dict[str, JSONValue]:
    result: dict[str, JSONValue] = {}
    if filters.get("in") is True:
        result["incoming"] = True
    if filters.get("out") is True:
        result["outgoing"] = True
    if "startswith" in filters:
        result["text_prefix"] = _optional_string(filters.get("startswith"), "")
    chat_ids = _int_list(filters.get("chat_id"))
    if chat_ids:
        result["chat_ids"] = chat_ids
    sender_ids = _int_list(filters.get("from_id"))
    if sender_ids:
        result["sender_ids"] = sender_ids
    result["kinds"] = ["message", "edited_message"]
    return result


def _matches_filters(handler: RegisteredHandler, message: Message, *, is_command: bool) -> bool:
    metadata = handler.metadata
    if metadata is None:
        return True
    filters = metadata.filters
    if filters.get("out") is True and not message.outgoing:
        return False
    if filters.get("in") is True and message.outgoing:
        return False
    if filters.get("only_commands") is True and not is_command:
        return False
    if filters.get("no_commands") is True and is_command:
        return False
    if filters.get("only_media") is True and message.dto.media is None:
        return False
    if filters.get("no_media") is True and message.dto.media is not None:
        return False
    mime_type = message.dto.media.mime_type if message.dto.media is not None else ""
    media_kind = message.dto.media.kind if message.dto.media is not None else ""
    if filters.get("only_photos") is True and not mime_type.startswith("image/"):
        return False
    if filters.get("only_videos") is True and not mime_type.startswith("video/"):
        return False
    if filters.get("only_audios") is True and not mime_type.startswith("audio/"):
        return False
    if filters.get("only_docs") is True and media_kind != "document":
        return False
    if filters.get("only_stickers") is True and media_kind != "sticker":
        return False
    if filters.get("no_photos") is True and mime_type.startswith("image/"):
        return False
    if filters.get("no_videos") is True and mime_type.startswith("video/"):
        return False
    if filters.get("no_audios") is True and mime_type.startswith("audio/"):
        return False
    if filters.get("no_docs") is True and media_kind == "document":
        return False
    if filters.get("no_stickers") is True and media_kind == "sticker":
        return False
    if filters.get("only_inline") is True and not message.dto.via_bot:
        return False
    if filters.get("no_inline") is True and message.dto.via_bot:
        return False
    if filters.get("only_forwards") is True and not message.dto.forwarded:
        return False
    if filters.get("no_forwards") is True and message.dto.forwarded:
        return False
    if filters.get("only_reply") is True and not message.is_reply:
        return False
    if filters.get("no_reply") is True and message.is_reply:
        return False
    if filters.get("mention") is True and not message.dto.mentioned:
        return False
    if filters.get("no_mention") is True and message.dto.mentioned:
        return False
    if filters.get("only_pm") is True and message.dto.chat_kind != "private":
        return False
    if filters.get("no_pm") is True and message.dto.chat_kind == "private":
        return False
    if filters.get("only_groups") is True and message.dto.chat_kind not in {
        "group",
        "supergroup",
    }:
        return False
    if filters.get("no_groups") is True and message.dto.chat_kind in {
        "group",
        "supergroup",
    }:
        return False
    if filters.get("only_channels") is True and message.dto.chat_kind != "channel":
        return False
    if filters.get("no_channels") is True and message.dto.chat_kind == "channel":
        return False
    chat_ids = _int_list(filters.get("chat_id"))
    if chat_ids and message.chat_id not in chat_ids:
        return False
    sender_ids = _int_list(filters.get("from_id"))
    if sender_ids and message.sender_id not in sender_ids:
        return False
    startswith = filters.get("startswith")
    if isinstance(startswith, str) and not message.text.startswith(startswith):
        return False
    endswith = filters.get("endswith")
    if isinstance(endswith, str) and not message.text.endswith(endswith):
        return False
    contains = filters.get("contains")
    if isinstance(contains, str) and contains not in message.text:
        return False
    regex = filters.get("regex")
    return not (isinstance(regex, str) and re.search(regex, message.text) is None)


def _wire_buttons(value: JSONValue) -> list[JSONValue]:
    if not isinstance(value, list):
        return []
    rows: list[JSONValue] = []
    for raw_row in value:
        if not isinstance(raw_row, list):
            continue
        row: list[JSONValue] = []
        for raw_button in raw_row:
            if not isinstance(raw_button, dict):
                continue
            row.append(
                {
                    "text": _optional_string(raw_button.get("text"), ""),
                    "callback_data": _optional_string(raw_button.get("callback"), ""),
                    "url": _optional_string(raw_button.get("url"), ""),
                    "switch_query": _optional_string(raw_button.get("switch_inline_query"), ""),
                }
            )
        rows.append(row)
    return rows


def _wire_inline_results(value: JSONValue) -> list[JSONValue]:
    if not isinstance(value, list):
        return []
    results: list[JSONValue] = []
    for index, item in enumerate(value):
        if not isinstance(item, dict):
            continue
        results.append(
            {
                "id": _optional_string(item.get("id"), str(index)),
                "type": _optional_string(item.get("type"), "article"),
                "title": _optional_string(item.get("title"), ""),
                "description": _optional_string(item.get("description"), ""),
                "text": _optional_string(item.get("text"), ""),
                "buttons": _wire_buttons(item.get("buttons")),
            }
        )
    return results


def _object(value: Any, field: str) -> dict[str, JSONValue]:
    if not isinstance(value, Mapping):
        raise ProtocolError(f"{field} must be a JSON object")
    checked = ensure_json_value(value, field=field)
    if not isinstance(checked, dict):
        raise ProtocolError(f"{field} must be a JSON object")
    return checked


def _required_string(value: Any, field: str) -> str:
    if not isinstance(value, str) or not value:
        raise ProtocolError(f"{field} must be a non-empty string")
    return value


def _optional_string(value: Any, default: str) -> str:
    return value if isinstance(value, str) else default


def _required_int(value: Any, field: str) -> int:
    if isinstance(value, bool) or not isinstance(value, int):
        raise ProtocolError(f"{field} must be an integer")
    return value


def _optional_int(value: Any, default: int) -> int:
    return value if isinstance(value, int) and not isinstance(value, bool) else default


def _nullable_int(value: Any, field: str) -> int | None:
    if value is None:
        return None
    return _required_int(value, field)


def _int_list(value: JSONValue) -> list[JSONValue]:
    if isinstance(value, int) and not isinstance(value, bool):
        return [value]
    if isinstance(value, list):
        return [item for item in value if isinstance(item, int) and not isinstance(item, bool)]
    return []


def _validate_wire_action(action: Mapping[str, JSONValue]) -> None:
    action_id = _required_string(action.get("id"), "action.id")
    kind = _required_string(action.get("kind"), "action.kind")
    if len(action_id.encode("utf-8")) > MAX_STRING_BYTES:
        raise _InvalidResponseError("action.id exceeds the protocol string limit")
    chat_id = _optional_int(action.get("chat_id"), 0)
    message_id = _optional_int(action.get("message_id"), 0)
    if kind in {"reply", "send"} and chat_id == 0:
        raise _InvalidResponseError(f"{kind} action requires chat_id")
    if kind in {"edit", "delete"} and (chat_id == 0 or message_id == 0):
        raise _InvalidResponseError(f"{kind} action requires chat_id and message_id")
    if kind == "inline_answer" and not (
        _optional_string(action.get("inline_query_id"), "")
        or _optional_string(action.get("callback_id"), "")
    ):
        raise _InvalidResponseError("inline_answer action requires inline_query_id or callback_id")
    if kind == "client_operation":
        operation = action.get("client_operation")
        if not isinstance(operation, dict) or not _optional_string(operation.get("name"), ""):
            raise _InvalidResponseError("client_operation action requires client_operation.name")
    elif kind not in {"reply", "edit", "delete", "send", "inline_answer"}:
        raise _InvalidResponseError(f"unknown wire action kind {kind!r}")
    for field in ("text", "parse_mode", "callback_id", "inline_query_id"):
        value = _optional_string(action.get(field), "")
        if len(value.encode("utf-8")) > MAX_STRING_BYTES:
            raise _InvalidResponseError(f"action.{field} exceeds the protocol string limit")
    for field in ("buttons", "inline_results"):
        value = action.get(field)
        if isinstance(value, list) and len(value) > MAX_COLLECTION_ITEMS:
            raise _InvalidResponseError(f"action.{field} exceeds {MAX_COLLECTION_ITEMS} entries")


def _compact_wire_action(action: Mapping[str, JSONValue]) -> dict[str, JSONValue]:
    result = {
        key: value
        for key, value in action.items()
        if key in {"id", "kind"} or value not in (None, "", 0, False, [], {})
    }
    operation = result.get("client_operation")
    if isinstance(operation, dict):
        result["client_operation"] = {
            key: value
            for key, value in operation.items()
            if key == "name" or value not in (None, "", 0, False, [], {})
        }
    return result


def _has_response_identity(request: Mapping[str, JSONValue]) -> bool:
    return (
        request.get("protocol") == PROTOCOL_NAME
        and isinstance(request.get("request_id"), int)
        and not isinstance(request.get("request_id"), bool)
        and bool(request.get("request_id"))
        and isinstance(request.get("deadline_unix_ms"), int)
        and not isinstance(request.get("deadline_unix_ms"), bool)
        and bool(request.get("deadline_unix_ms"))
    )


def _truncate_utf8(value: str, limit: int) -> str:
    encoded = value.encode("utf-8")
    if len(encoded) <= limit:
        return value
    return encoded[:limit].decode("utf-8", errors="ignore")


__all__ = [
    "HostActionSink",
    "HostSettings",
    "HostState",
    "SidecarHost",
    "load_plugin_module",
]
