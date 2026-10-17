"""Validation and wire-format helpers used by the host subsystems."""

from __future__ import annotations

import re
from collections.abc import Mapping
from typing import Any

from serenox_sdk.actions import Message
from serenox_sdk.errors import ProtocolError
from serenox_sdk.module import RegisteredHandler
from serenox_sdk.protocol import (
    MAX_COLLECTION_ITEMS,
    MAX_STRING_BYTES,
    PROTOCOL_NAME,
    JSONValue,
    ensure_json_value,
)

from .host_models import InvalidResponseError


def wire_watcher_filter(filters: Mapping[str, JSONValue]) -> dict[str, JSONValue]:
    result: dict[str, JSONValue] = {}
    if filters.get("in") is True:
        result["incoming"] = True
    if filters.get("out") is True:
        result["outgoing"] = True
    if "startswith" in filters:
        result["text_prefix"] = optional_string(filters.get("startswith"), "")
    chat_ids = int_list(filters.get("chat_id"))
    if chat_ids:
        result["chat_ids"] = chat_ids
    sender_ids = int_list(filters.get("from_id"))
    if sender_ids:
        result["sender_ids"] = sender_ids
    declared_kinds = filters.get("kinds")
    if isinstance(declared_kinds, list) and all(isinstance(kind, str) for kind in declared_kinds):
        result["kinds"] = list(declared_kinds)
    else:
        result["kinds"] = ["message", "edited_message"]
    return result


def matches_filters(handler: RegisteredHandler, message: Message, *, is_command: bool) -> bool:
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
    if filters.get("only_groups") is True and message.dto.chat_kind not in {"group", "supergroup"}:
        return False
    if filters.get("no_groups") is True and message.dto.chat_kind in {"group", "supergroup"}:
        return False
    if filters.get("only_channels") is True and message.dto.chat_kind != "channel":
        return False
    if filters.get("no_channels") is True and message.dto.chat_kind == "channel":
        return False
    chat_ids = int_list(filters.get("chat_id"))
    if chat_ids and message.chat_id not in chat_ids:
        return False
    sender_ids = int_list(filters.get("from_id"))
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


def wire_buttons(value: JSONValue) -> list[JSONValue]:
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
                    "text": optional_string(raw_button.get("text"), ""),
                    "callback_data": optional_string(raw_button.get("callback"), ""),
                    "url": optional_string(raw_button.get("url"), ""),
                    "switch_query": optional_string(raw_button.get("switch_inline_query"), ""),
                }
            )
        rows.append(row)
    return rows


def wire_inline_results(value: JSONValue) -> list[JSONValue]:
    if not isinstance(value, list):
        return []
    results: list[JSONValue] = []
    for index, item in enumerate(value):
        if not isinstance(item, dict):
            continue
        results.append(
            {
                "id": optional_string(item.get("id"), str(index)),
                "type": optional_string(item.get("type"), "article"),
                "title": optional_string(item.get("title"), ""),
                "description": optional_string(item.get("description"), ""),
                "text": optional_string(item.get("text"), ""),
                "buttons": wire_buttons(item.get("buttons")),
            }
        )
    return results


def object_value(value: Any, field: str) -> dict[str, JSONValue]:
    if not isinstance(value, Mapping):
        raise ProtocolError(f"{field} must be a JSON object")
    checked = ensure_json_value(value, field=field)
    if not isinstance(checked, dict):
        raise ProtocolError(f"{field} must be a JSON object")
    return checked


def required_string(value: Any, field: str) -> str:
    if not isinstance(value, str) or not value:
        raise ProtocolError(f"{field} must be a non-empty string")
    return value


def optional_string(value: Any, default: str) -> str:
    return value if isinstance(value, str) else default


def required_int(value: Any, field: str) -> int:
    if isinstance(value, bool) or not isinstance(value, int):
        raise ProtocolError(f"{field} must be an integer")
    return value


def optional_int(value: Any, default: int) -> int:
    return value if isinstance(value, int) and not isinstance(value, bool) else default


def nullable_int(value: Any, field: str) -> int | None:
    if value is None:
        return None
    return required_int(value, field)


def int_list(value: JSONValue) -> list[JSONValue]:
    if isinstance(value, int) and not isinstance(value, bool):
        return [value]
    if isinstance(value, list):
        return [item for item in value if isinstance(item, int) and not isinstance(item, bool)]
    return []


def validate_wire_action(action: Mapping[str, JSONValue]) -> None:
    action_id = required_string(action.get("id"), "action.id")
    kind = required_string(action.get("kind"), "action.kind")
    if len(action_id.encode("utf-8")) > MAX_STRING_BYTES:
        raise InvalidResponseError("action.id exceeds the protocol string limit")
    idempotency_key = optional_string(action.get("idempotency_key"), "")
    if len(idempotency_key.encode("utf-8")) > 128:
        raise InvalidResponseError("action.idempotency_key exceeds 128 bytes")
    chat_id = optional_int(action.get("chat_id"), 0)
    message_id = optional_int(action.get("message_id"), 0)
    if kind in {"reply", "send"} and chat_id == 0:
        raise InvalidResponseError(f"{kind} action requires chat_id")
    if kind in {"edit", "delete"} and (chat_id == 0 or message_id == 0):
        raise InvalidResponseError(f"{kind} action requires chat_id and message_id")
    if kind == "inline_answer" and not (
        optional_string(action.get("inline_query_id"), "")
        or optional_string(action.get("callback_id"), "")
    ):
        raise InvalidResponseError("inline_answer action requires inline_query_id or callback_id")
    if kind == "client_operation":
        operation = action.get("client_operation")
        if not isinstance(operation, dict) or not optional_string(operation.get("name"), ""):
            raise InvalidResponseError("client_operation action requires client_operation.name")
    elif kind not in {"reply", "edit", "delete", "send", "inline_answer"}:
        raise InvalidResponseError(f"unknown wire action kind {kind!r}")
    if "media" in action:
        media = action.get("media")
        if (
            not isinstance(media, dict)
            or not optional_string(media.get("handle"), "")
            or not optional_string(media.get("kind"), "")
        ):
            raise InvalidResponseError("media action requires opaque media.handle and media.kind")
        size = optional_int(media.get("size"), 0)
        if size < 0 or size > 64 * 1024 * 1024:
            raise InvalidResponseError("media.size exceeds the 64 MiB limit")
    for field in ("text", "parse_mode", "callback_id", "inline_query_id"):
        value = optional_string(action.get(field), "")
        if len(value.encode("utf-8")) > MAX_STRING_BYTES:
            raise InvalidResponseError(f"action.{field} exceeds the protocol string limit")
    for field in ("buttons", "inline_results"):
        value = action.get(field)
        if isinstance(value, list) and len(value) > MAX_COLLECTION_ITEMS:
            raise InvalidResponseError(f"action.{field} exceeds {MAX_COLLECTION_ITEMS} entries")


def compact_wire_action(action: Mapping[str, JSONValue]) -> dict[str, JSONValue]:
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
    media = result.get("media")
    if isinstance(media, dict):
        result["media"] = {
            key: value
            for key, value in media.items()
            if value not in (None, "", 0, False, [], {}) or key in {"handle", "kind"}
        }
    return result


def has_response_identity(request: Mapping[str, JSONValue]) -> bool:
    return (
        request.get("protocol") == PROTOCOL_NAME
        and isinstance(request.get("request_id"), int)
        and not isinstance(request.get("request_id"), bool)
        and bool(request.get("request_id"))
        and isinstance(request.get("deadline_unix_ms"), int)
        and not isinstance(request.get("deadline_unix_ms"), bool)
        and bool(request.get("deadline_unix_ms"))
    )


def truncate_utf8(value: str, limit: int) -> str:
    encoded = value.encode("utf-8")
    if len(encoded) <= limit:
        return value
    return encoded[:limit].decode("utf-8", errors="ignore")
