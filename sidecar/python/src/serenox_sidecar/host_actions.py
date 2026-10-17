"""Action collection and conversion from SDK operations to protocol actions."""

from __future__ import annotations

import contextvars
import json
from collections.abc import Awaitable, Callable, Sequence

from serenox_sdk.actions import ActionSink
from serenox_sdk.errors import ActionError, OverloadedError, UnsupportedAPIError
from serenox_sdk.protocol import JSONValue, OperationKind, OperationV1, ensure_json_value

from .host_utils import (
    compact_wire_action,
    optional_int,
    optional_string,
    validate_wire_action,
    wire_buttons,
    wire_inline_results,
)


class ActionCollector:
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


active_collector: contextvars.ContextVar[ActionCollector | None] = contextvars.ContextVar(
    "serenox_active_action_collector", default=None
)


class HostActionSink(ActionSink):
    """Collect actions into the current bounded lifecycle/dispatch response."""

    def __init__(self, max_actions: int) -> None:
        self.max_actions = max_actions
        self._stream_sender: Callable[[list[dict[str, JSONValue]]], Awaitable[None]] | None = None
        self._action_sender: Callable[[list[dict[str, JSONValue]]], Awaitable[JSONValue]] | None = (
            None
        )
        self._duplex_sender: Callable[[OperationV1], Awaitable[JSONValue]] | None = None

    def set_stream_sender(
        self, sender: Callable[[list[dict[str, JSONValue]]], Awaitable[None]] | None
    ) -> None:
        self._stream_sender = sender

    def set_duplex_sender(
        self, sender: Callable[[OperationV1], Awaitable[JSONValue]] | None
    ) -> None:
        self._duplex_sender = sender

    def set_action_sender(
        self, sender: Callable[[list[dict[str, JSONValue]]], Awaitable[JSONValue]] | None
    ) -> None:
        self._action_sender = sender

    async def request(self, action: OperationV1) -> JSONValue:
        if action.kind in {
            OperationKind.INLINE_ANSWER,
            OperationKind.INLINE_FORM,
            OperationKind.INLINE_EDIT,
            OperationKind.INLINE_DELETE,
            OperationKind.JOIN_CHAT,
            OperationKind.LEAVE_CHAT,
            OperationKind.REACT,
        }:
            raise UnsupportedAPIError(
                action.kind.value,
                "this operation is not implemented by the native Telegram user-session adapter",
            )
        if action.kind in {
            OperationKind.STORAGE_GET,
            OperationKind.STORAGE_SET,
            OperationKind.STORAGE_DELETE,
            OperationKind.JOB_CREATE,
            OperationKind.JOB_UPDATE,
            OperationKind.JOB_CANCEL,
            OperationKind.JOB_PAUSE,
            OperationKind.JOB_RESUME,
            OperationKind.JOB_TRIGGER,
            OperationKind.JOB_GET,
            OperationKind.JOB_LIST,
            OperationKind.JOB_INSPECT,
            OperationKind.JOB_CLEAR,
            OperationKind.JOB_DELETE,
            OperationKind.EVENT_PUBLISH,
            OperationKind.GET_MESSAGE,
            OperationKind.GET_ME,
            OperationKind.RESOLVE_PEER,
            OperationKind.MEDIA_INSPECT,
            OperationKind.MEDIA_DOWNLOAD,
            OperationKind.MEDIA_UPLOAD,
            OperationKind.MEDIA_ATTACH,
            OperationKind.MEDIA_DELETE,
        } or action.kind.value.startswith("moderation."):
            if self._duplex_sender is None:
                raise UnsupportedAPIError(
                    action.kind.value,
                    "a future duplex host.client RPC (or host.storage RPC); response reads "
                    "are not available on this protocol connection",
                )
            return await self._duplex_sender(action)
        collector = active_collector.get()
        if collector is None:
            raise UnsupportedAPIError(
                "background Python plugin actions",
                "an action awaited inside a lifecycle hook or dispatched handler",
            )
        actions = [
            compact_wire_action(wire_action)
            for wire_action in self._wire_actions(action, collector)
        ]
        for wire_action in actions:
            validate_wire_action(wire_action)
        if self._action_sender is not None:
            value = await self._action_sender(actions)
            result = value if isinstance(value, dict) else {}
            status = str(result.get("status", "success"))
            if status in {"failure", "outcome_unknown"}:
                error = result.get("error") if isinstance(result.get("error"), dict) else {}
                raise ActionError(
                    str(error.get("message", "action failed")),
                    action_code=str(error.get("code", "action_failed")),
                    retryable=bool(result.get("retryable", error.get("retryable", False))),
                    details=error.get("details")
                    if isinstance(error.get("details"), dict)
                    else None,
                )
            return value
        if self._stream_sender is None:
            collector.append(actions)
        else:
            await self._stream_sender(actions)
        # These are intentionally asynchronous action acknowledgements. Reads
        # and storage never use this path; they return typed duplex results.
        return self._async_action_result(action)

    def notify(self, action: OperationV1) -> None:
        if (
            action.kind in {OperationKind.STORAGE_SET, OperationKind.STORAGE_DELETE}
            and self._duplex_sender is not None
        ):
            method = "async_set" if action.kind is OperationKind.STORAGE_SET else "async_delete"
            raise UnsupportedAPIError(
                action.kind.value,
                f"await Module.{method} for a durable host.storage commit",
            )
        collector = active_collector.get()
        if collector is None:
            raise UnsupportedAPIError(
                "background Python plugin actions",
                "an action awaited inside a lifecycle hook or dispatched handler",
            )
        actions = [
            compact_wire_action(wire_action)
            for wire_action in self._wire_actions(action, collector)
        ]
        for wire_action in actions:
            validate_wire_action(wire_action)
        collector.append(actions)

    def _wire_actions(
        self, action: OperationV1, collector: ActionCollector
    ) -> list[dict[str, JSONValue]]:
        target = action.target
        parameters = action.parameters
        action_id = collector.action_id()
        chat_id = optional_int(target.get("chat_id"), 0)
        message_id = optional_int(target.get("message_id"), 0)
        text = optional_string(parameters.get("text"), "")
        parse_mode = optional_string(parameters.get("parse_mode"), "")
        base: dict[str, JSONValue] = {
            "id": action_id,
            "idempotency_key": optional_string(parameters.get("idempotency_key"), ""),
        }

        if action.kind is OperationKind.REPLY:
            return [
                {
                    **base,
                    "kind": "reply",
                    "chat_id": chat_id,
                    "reply_to_message_id": message_id,
                    "text": text,
                    "parse_mode": parse_mode,
                    "buttons": wire_buttons(parameters.get("reply_markup")),
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
                    "buttons": wire_buttons(parameters.get("reply_markup")),
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
                    "reply_to_message_id": optional_int(parameters.get("reply_to_message_id"), 0),
                    "text": text,
                    "parse_mode": parse_mode,
                    "buttons": wire_buttons(parameters.get("reply_markup")),
                }
            ]
        if action.kind is OperationKind.PROFILE:
            return [
                {
                    **base,
                    "kind": "profile_update_name",
                    "profile": {
                        "operation": optional_string(parameters.get("operation"), ""),
                        "first_name": optional_string(parameters.get("first_name"), ""),
                        "last_name": optional_string(parameters.get("last_name"), ""),
                        "generation": optional_int(parameters.get("generation"), 0),
                        "lease_token": optional_string(parameters.get("lease_token"), ""),
                    },
                }
            ]
        if action.kind is OperationKind.INLINE_ANSWER:
            return [
                {
                    **base,
                    "kind": "inline_answer",
                    "inline_query_id": optional_string(target.get("query_id"), ""),
                    "callback_id": optional_string(target.get("callback_id"), ""),
                    "text": text,
                    "inline_results": wire_inline_results(parameters.get("results")),
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
                    "buttons": wire_buttons(parameters.get("reply_markup")),
                }
            ]
        return [self._wire_client_action(action, action_id)]

    def _wire_client_action(self, action: OperationV1, action_id: str) -> dict[str, JSONValue]:
        target = action.target
        parameters = action.parameters
        operation_name = action.kind.value.removeprefix("client.")
        if action.kind in {OperationKind.STORAGE_SET, OperationKind.STORAGE_DELETE}:
            mutation: dict[str, JSONValue] = {
                "operation": operation_name.removeprefix("storage."),
                "namespace": str(target.get("namespace", "")),
                "key": str(target.get("key", "")),
            }
            if action.kind is OperationKind.STORAGE_SET:
                mutation["value"] = parameters.get("value")
            return {
                "id": action_id,
                "idempotency_key": optional_string(parameters.get("idempotency_key"), ""),
                "kind": "client_operation",
                "client_operation": {"name": action.kind.value, "peer_id": 0, "message_id": 0},
                "storage_mutation": mutation,
            }
        payload = {
            "target": ensure_json_value(target, field="action.target"),
            "parameters": ensure_json_value(parameters, field="action.parameters"),
        }
        peer = target.get("chat", target.get("peer", target.get("chat_id", 0)))
        return {
            "id": action_id,
            "idempotency_key": optional_string(parameters.get("idempotency_key"), ""),
            "kind": "client_operation",
            "client_operation": {
                "name": operation_name,
                "peer_id": peer if isinstance(peer, int) else 0,
                "message_id": optional_int(target.get("message_id"), 0),
                "value": json.dumps(payload, ensure_ascii=False, separators=(",", ":")),
            },
        }

    def _async_action_result(self, action: OperationV1) -> JSONValue:
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
                    "id": optional_int(target.get("message_id"), 0)
                    if action.kind is OperationKind.EDIT
                    else 0,
                    "chat_id": optional_int(target.get("chat_id"), 0),
                    "text": optional_string(parameters.get("text"), ""),
                    "outgoing": True,
                }
            }
        return {}


__all__ = ["ActionCollector", "HostActionSink", "active_collector"]
