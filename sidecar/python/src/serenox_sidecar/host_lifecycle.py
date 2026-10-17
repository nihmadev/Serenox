"""Lifecycle hooks, update dispatch, and plugin handler execution."""

from __future__ import annotations

import asyncio
import contextvars
import functools
import hmac
import inspect
from collections.abc import Callable, Mapping
from types import MappingProxyType
from typing import Any, cast

from serenox_sdk.actions import (
    CallbackQuery,
    DeletedMessages,
    InlineQuery,
    Message,
    OutgoingMessage,
)
from serenox_sdk.errors import ConfigurationError, ProtocolError
from serenox_sdk.module import RegisteredHandler
from serenox_sdk.protocol import (
    MAX_COLLECTION_ITEMS,
    PROTOCOL_VERSION,
    HandlerKind,
    JSONValue,
    ensure_json_value,
)

from .host_metadata import build_wire_metadata
from .host_models import (
    AuthenticationError,
    HostState,
    IncompatibleProtocolError,
    InvalidStateError,
)
from .host_utils import (
    compact_wire_action,
    matches_filters,
    nullable_int,
    object_value,
    optional_string,
    required_int,
    required_string,
    validate_wire_action,
)


class HostLifecycleMixin:
    async def _handshake(self: Any, payload: Mapping[str, JSONValue]) -> dict[str, JSONValue]:
        async with self._state_lock:
            if self.state is not HostState.LOADED:
                raise InvalidStateError(
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
                raise IncompatibleProtocolError(
                    f"core does not offer required protocol version {PROTOCOL_VERSION}"
                )
            token = required_string(payload.get("auth_token"), "auth_token")
            if not hmac.compare_digest(token, self.auth_token):
                raise AuthenticationError("sidecar authentication token did not match")
            instance_id = required_string(payload.get("instance_id"), "instance_id")
            if instance_id != self.instance_id:
                raise AuthenticationError("sidecar instance ID did not match")
            nonce = required_string(payload.get("nonce"), "nonce")
            requested_frame = required_int(payload.get("max_frame_bytes"), "max_frame_bytes")
            if requested_frame <= 0:
                raise ProtocolError("max_frame_bytes must be positive")
            host_version = payload.get("host_version")
            if host_version is not None and not isinstance(host_version, str):
                raise ProtocolError("host_version must be a string")
            features = payload.get("features", [])
            if not isinstance(features, list) or any(
                not isinstance(item, str) for item in features
            ):
                raise ProtocolError("handshake.features must contain strings")
            self._streaming_actions_enabled = "streaming_actions" in features
            self._duplex_enabled = "duplex_host_rpc" in features
            self._negotiated_frame_bytes = min(requested_frame, self.settings.max_frame_bytes)
            self.state = HostState.NEGOTIATED
            return {
                "selected_version": PROTOCOL_VERSION,
                "nonce": nonce,
                "max_frame_bytes": self._negotiated_frame_bytes,
                "metadata": self._wire_metadata(),
            }

    async def _configure(self: Any, payload: Mapping[str, JSONValue]) -> dict[str, JSONValue]:
        async with self._state_lock:
            if self.state is not HostState.NEGOTIATED:
                raise InvalidStateError(
                    f"configure is invalid while host state is {self.state.value}"
                )
            if payload.get("phase", "configure") != "configure":
                raise ProtocolError("configure payload phase must be 'configure'")
            raw_config = payload.get("config", [])
            if not isinstance(raw_config, list):
                raise ProtocolError("lifecycle config must be a list")
            if len(raw_config) > MAX_COLLECTION_ITEMS:
                raise ProtocolError(f"lifecycle config exceeds {MAX_COLLECTION_ITEMS} entries")
            schema = {item.key: item for item in self.module.config.schema()}
            values: dict[str, Any] = {}
            for index, entry in enumerate(raw_config):
                item = object_value(entry, f"config[{index}]")
                key = required_string(item.get("key"), f"config[{index}].key")
                configured_type = required_string(item.get("type"), f"config[{index}].type")
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

    async def _start(self: Any, payload: Mapping[str, JSONValue]) -> dict[str, JSONValue]:
        async with self._state_lock:
            if self.state is not HostState.CONFIGURED:
                raise InvalidStateError(f"start is invalid while host state is {self.state.value}")
            if payload.get("phase", "start") != "start":
                raise ProtocolError("start payload phase must be 'start'")
            await self._call(self.module.client_ready)
            self.state = HostState.RUNNING
            return {"state": "ready"}

    async def _stop(self: Any, payload: Mapping[str, JSONValue]) -> dict[str, JSONValue]:
        async with self._state_lock:
            if self.state not in {HostState.CONFIGURED, HostState.RUNNING}:
                raise InvalidStateError(f"stop is invalid while host state is {self.state.value}")
            if payload.get("phase", "stop") != "stop":
                raise ProtocolError("stop payload phase must be 'stop'")
            await self._call(self.module.on_unload)
            self._unloaded = True
            self.state = HostState.STOPPED
            return {"state": HostState.STOPPED.value}

    async def _dispatch_update(self: Any, payload: Mapping[str, JSONValue]) -> dict[str, JSONValue]:
        if self.state is not HostState.RUNNING:
            raise InvalidStateError(f"dispatch is invalid while host state is {self.state.value}")
        event_value = payload.get("event")
        if event_value is not None:
            event = object_value(event_value, "event")
            handler_id = required_string(event.get("subscription_id"), "event.subscription_id")
            callback = self.registry.event_handlers.get(handler_id)
            if callback is None:
                raise ProtocolError(f"core dispatched unknown event handler {handler_id!r}")
            result = await self._call(
                callback,
                _immutable_event_value(object_value(event.get("envelope"), "event.envelope")),
            )
            wire_result = ensure_json_value(
                result
                if isinstance(result, Mapping)
                else {"event_result": {"outcome": "acknowledged"}},
                field="event.result",
            )
            if not isinstance(wire_result, dict):
                raise ProtocolError("event handler result must be an object")
            return wire_result
        job_value = payload.get("job")
        if job_value is not None:
            job = object_value(job_value, "job")
            name = required_string(job.get("handler"), "job.handler").lower()
            registered = self.registry.job_handlers.get(name)
            if registered is None:
                raise ProtocolError(f"core dispatched unknown job handler {name!r}")
            result = await self._invoke_job(registered[0], job)
            return ensure_json_value(
                result if isinstance(result, Mapping) else {}, field="job.result"
            )
        pre_send_value = payload.get("pre_send")
        if pre_send_value is not None:
            pre_send = object_value(pre_send_value, "pre_send")
            handler_name = required_string(pre_send.get("handler"), "pre_send.handler")
            raw_action = object_value(pre_send.get("action"), "pre_send.action")
            validate_wire_action(raw_action)
            if raw_action.get("kind") != "send":
                raise ProtocolError("pre_send.action.kind must be 'send'")
            handler = self.registry.find(HandlerKind.PRE_SEND, handler_name)
            if handler is None:
                raise ProtocolError(f"core dispatched unknown pre-send handler {handler_name!r}")
            outgoing = OutgoingMessage.from_wire(raw_action)
            result = await self._invoke_handler(handler, outgoing)
            if isinstance(result, str):
                outgoing.text = result
            elif result is not None and not isinstance(result, OutgoingMessage):
                raise ProtocolError("pre-send handlers must mutate OutgoingMessage or return text")
            transformed = outgoing.to_wire()
            validate_wire_action(transformed)
            return {"pre_send": {"action": compact_wire_action(transformed)}}
        update = object_value(payload.get("update"), "update")
        required_string(update.get("id"), "update.id")
        required_int(update.get("occurred_unix_ms"), "update.occurred_unix_ms")
        command_value = payload.get("command")
        if command_value is not None:
            command = object_value(command_value, "command")
            name = required_string(command.get("name"), "command.name")
            for field_name in ("prefix", "raw_args"):
                if not isinstance(command.get(field_name, ""), str):
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
                object_value(command.get("message"), "command.message"), self._sink
            )
            await self._invoke_handler(handler, message)
            for watcher_handler in self.registry.by_kind(HandlerKind.WATCHER):
                if matches_filters(watcher_handler, message, is_command=True):
                    await self._invoke_handler(watcher_handler, message)
            return {}
        kind = required_string(update.get("kind"), "update.kind")
        if kind in {"message", "edited_message"}:
            message = Message.from_dict(
                object_value(update.get("message"), "update.message"), self._sink
            )
            for handler in self.registry.by_kind(HandlerKind.WATCHER):
                if matches_filters(handler, message, is_command=False):
                    await self._invoke_handler(handler, message)
            return {}
        if kind == "inline_query":
            raw_query = object_value(update.get("inline_query"), "inline_query")
            query = InlineQuery(
                id=required_string(raw_query.get("id"), "inline_query.id"),
                sender_id=required_int(raw_query.get("sender_id"), "inline_query.sender_id"),
                query=optional_string(raw_query.get("query"), ""),
                offset=optional_string(raw_query.get("offset"), ""),
                _inline=self.module.inline,
            )
            for handler in self.registry.by_kind(HandlerKind.INLINE):
                await self._invoke_handler(handler, query)
            return {}
        if kind == "callback":
            raw_callback = object_value(update.get("callback"), "update.callback")
            callback = CallbackQuery(
                id=required_string(raw_callback.get("id"), "callback.id"),
                sender_id=required_int(raw_callback.get("sender_id"), "callback.sender_id"),
                data=optional_string(raw_callback.get("data"), ""),
                chat_id=nullable_int(raw_callback.get("chat_id"), "callback.chat_id"),
                message_id=nullable_int(raw_callback.get("message_id"), "callback.message_id"),
                _sink=self._sink,
                message=(
                    Message.from_dict(
                        object_value(raw_callback.get("message"), "callback.message"), self._sink
                    )
                    if raw_callback.get("message") is not None
                    else None
                ),
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
            deleted = DeletedMessages(
                update_id=required_string(update.get("id"), "update.id"),
                message_ids=tuple(cast(int, message_id) for message_id in deleted_ids),
            )
            for handler in self.registry.by_kind(HandlerKind.WATCHER):
                declared = handler.metadata.filters.get("kinds", ()) if handler.metadata else ()
                if not declared or "deleted_message" in declared:
                    await self._invoke_handler(handler, deleted)
            return {}
        raise ProtocolError(f"unsupported update kind {kind!r}")

    async def _invoke_handler(self: Any, handler: RegisteredHandler, argument: Any) -> Any:
        timeout = min(handler.spec.timeout_ms / 1000, self.settings.max_handler_timeout_seconds)
        try:
            return await asyncio.wait_for(self._call(handler.callback, argument), timeout=timeout)
        except TimeoutError:
            raise TimeoutError(
                f"handler {handler.spec.kind.value}:{handler.spec.name} exceeded {timeout}s"
            ) from None

    async def _invoke_job(
        self: Any, callback: Callable[..., Any], argument: Mapping[str, JSONValue]
    ) -> Any:
        return await asyncio.wait_for(
            self._call(callback, argument), timeout=self.settings.max_handler_timeout_seconds
        )

    async def _call(self: Any, callback: Callable[..., Any], *args: Any) -> Any:
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

    def _health(self: Any, payload: Mapping[str, JSONValue]) -> dict[str, JSONValue]:
        detailed_value = payload.get("detailed", False)
        if not isinstance(detailed_value, bool):
            raise ProtocolError("health.detailed must be a boolean")
        response: dict[str, JSONValue] = {
            "ready": self.state is HostState.RUNNING,
            "status": self.state.value,
            "uptime_ms": int((self.clock() - self.started_at) * 1000),
        }
        if detailed_value:
            metrics = dict(self._metrics)
            metrics["queue_depth"] = self._requests.qsize()
            metrics["worker_count"] = self.settings.worker_count
            response["metrics"] = ensure_json_value(metrics, field="metrics")
        return response

    def _wire_metadata(self: Any) -> dict[str, JSONValue]:
        return build_wire_metadata(self)


def _immutable_event_value(value: Any) -> Any:
    if isinstance(value, Mapping):
        return MappingProxyType(
            {str(key): _immutable_event_value(item) for key, item in value.items()}
        )
    if isinstance(value, list):
        return tuple(_immutable_event_value(item) for item in value)
    return value
