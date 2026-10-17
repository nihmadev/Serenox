"""Internal protocol component: generic dataclass-to-wire encoding."""

from __future__ import annotations

import dataclasses
import enum
from collections.abc import Mapping
from typing import Any

from .errors import ProtocolError
from .protocol_constants import JSONValue
from .protocol_validation import ensure_json_value


def _wire(value: Any) -> JSONValue:
    if dataclasses.is_dataclass(value) and not isinstance(value, type):
        result: dict[str, JSONValue] = {}
        omit_empty = _OMIT_EMPTY_FIELDS.get(type(value).__name__, frozenset())
        for field in dataclasses.fields(value):
            item = getattr(value, field.name)
            if field.name in omit_empty and _is_empty(item):
                continue
            result[field.name] = _wire(item)
        return result
    if isinstance(value, enum.Enum):
        return ensure_json_value(value.value)
    if isinstance(value, Mapping):
        return ensure_json_value({str(key): _wire(item) for key, item in value.items()})
    if isinstance(value, (list, tuple)):
        return ensure_json_value([_wire(item) for item in value])
    return ensure_json_value(value)


def _is_empty(value: Any) -> bool:
    return (
        value is None
        or value is False
        or value == 0
        or value == ""
        or value == ()
        or value == []
        or value == {}
    )


_OMIT_EMPTY_FIELDS: dict[str, frozenset[str]] = {
    "RPCErrorV1": frozenset({"retryable", "field", "details", "operation", "flood_wait_seconds"}),
    "ResponseV1": frozenset({"result", "error"}),
    "EnvelopeV1": frozenset({"method", "result", "error"}),
    "CommandSpecV1": frozenset({"aliases", "description", "owner_only"}),
    "ControlCommandV1": frozenset(
        {
            "aliases",
            "description",
            "actor_policy",
            "allowed_actor_ids",
            "mutating",
            "require_confirmation",
            "confirmation_timeout_ms",
            "allow_query_execution",
            "max_argument_bytes",
            "max_state_bytes",
            "handler_id",
        }
    ),
    "WatcherFilterV1": frozenset(
        {"kinds", "chat_ids", "sender_ids", "incoming", "outgoing", "text_prefix"}
    ),
    "WatcherSpecV1": frozenset({"description"}),
    "InlineSpecV1": frozenset(
        {"description", "query_prefix", "sender_ids", "allowed_users", "owner_only"}
    ),
    "CallbackSpecV1": frozenset(
        {
            "description",
            "data",
            "data_prefix",
            "chat_ids",
            "sender_ids",
            "allowed_users",
            "owner_only",
            "ttl_ms",
        }
    ),
    "ConfigSpecV1": frozenset({"description", "required", "secret", "default"}),
    "ConfigValueV1": frozenset({"secret"}),
    "PluginMetadataV1": frozenset(
        {
            "description",
            "author",
            "capabilities",
            "commands",
            "watchers",
            "inline_handlers",
            "callback_handlers",
            "pre_send_handlers",
            "control_commands",
            "config",
            "jobs",
        }
    ),
    "PreSendSpecV1": frozenset({"description"}),
    "EntityV1": frozenset({"url", "user_id", "language"}),
    "MediaV1": frozenset(
        {"handle", "file_id", "file_name", "mime_type", "size", "checksum", "source", "attributes"}
    ),
    "MediaAttachmentV1": frozenset(
        {"mime_type", "file_name", "size", "checksum", "caption", "parse_mode", "attributes"}
    ),
    "MediaRequestV1": frozenset(
        {"handle", "kind", "mime_type", "file_name", "size", "checksum", "peer_id"}
    ),
    "MediaResultV1": frozenset({"media", "transfer_id", "chunk_size", "max_size", "complete"}),
    "MessageV1": frozenset(
        {
            "sender_id",
            "edit_date_unix_ms",
            "text",
            "outgoing",
            "mentioned",
            "reply_to_message_id",
            "entities",
            "chat_kind",
            "via_bot",
            "forwarded",
            "media",
            "peer",
            "sender",
            "reply_to",
            "forward",
            "grouped_id",
            "service",
            "reactions",
            "poll",
        }
    ),
    "CallbackV1": frozenset({"chat_id", "message_id", "data", "message"}),
    "InlineQueryV1": frozenset({"query", "offset"}),
    "ControlBotIdentityV1": frozenset({"username"}),
    "ControlActorV1": frozenset({"username"}),
    "ControlEventV1": frozenset(
        {
            "chat_id",
            "message_id",
            "inline_message_id",
            "chat_instance",
            "query_id",
            "query",
            "offset",
            "chat_type",
            "callback_data",
            "command",
            "command_arguments",
        }
    ),
    "UpdateV1": frozenset({"message", "deleted_ids", "callback", "inline_query", "control"}),
    "CommandV1": frozenset({"raw_args", "args"}),
    "InlineButtonV1": frozenset({"callback_data", "url", "switch_query"}),
    "InlineResultV1": frozenset({"title", "description", "text", "buttons"}),
    "ClientOperationV1": frozenset({"peer_id", "message_id", "user_id", "value"}),
    "ActionV1": frozenset(
        {
            "idempotency_key",
            "chat_id",
            "message_id",
            "reply_to_message_id",
            "text",
            "parse_mode",
            "callback_id",
            "inline_query_id",
            "response_channel",
            "inline_message_id",
            "callback_text",
            "show_alert",
            "cache_time_seconds",
            "is_personal",
            "next_offset",
            "buttons",
            "inline_results",
            "client_operation",
            "storage_mutation",
            "media",
        }
    ),
    "ActionResultV1": frozenset(
        {
            "operation",
            "message",
            "peer",
            "message_id",
            "date_unix_ms",
            "media_handle",
            "metadata",
            "error",
            "outcome_unknown",
            "retryable",
            "flood_wait_seconds",
        }
    ),
    "ActionBatchRequestV1": frozenset({"stop_on_error"}),
    "ActionBatchResponseV1": frozenset({"stopped", "partial"}),
    "HandshakeRequestV1": frozenset({"host_version"}),
    "LifecycleRequestV1": frozenset({"config"}),
    "LifecycleResponseV1": frozenset({"actions"}),
    "DispatchRequestV1": frozenset({"command", "pre_send"}),
    "DispatchResponseV1": frozenset({"actions", "pre_send"}),
    "HealthRequestV1": frozenset({"detailed"}),
    "HealthResponseV1": frozenset({"metrics", "uptime_ms"}),
    "StorageRequestV1": frozenset({"value", "batch"}),
    "StorageResultV1": frozenset({"value", "entries"}),
    "ClientRequestV1": frozenset(
        {
            "peer_id",
            "username",
            "message_id",
            "message_ids",
            "user_ids",
            "chat_ids",
            "sender_id",
            "limit",
            "offset_id",
            "before",
            "after",
            "cursor",
            "query",
            "handle",
            "media_kind",
            "folder_id",
            "direction",
        }
    ),
    "ClientResultV1": frozenset(
        {
            "message",
            "peer",
            "me",
            "messages",
            "users",
            "chats",
            "dialogs",
            "participants",
            "media",
            "page",
            "read_error",
            "next_offset_id",
        }
    ),
}


class WireDTO:
    def to_dict(self) -> dict[str, JSONValue]:
        value = _wire(self)
        if not isinstance(value, dict):
            raise ProtocolError(f"{type(self).__name__} must encode as a JSON object")
        return value
