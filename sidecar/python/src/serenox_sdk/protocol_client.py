"""Internal protocol component: client request and result DTOs."""

from __future__ import annotations

import dataclasses
from collections.abc import Mapping, Sequence
from typing import Any

from .errors import ProtocolError
from .protocol_constants import JSONValue
from .protocol_updates import MessageV1
from .protocol_validation import _optional_int, _optional_str, _require_object, _require_str
from .protocol_wire import WireDTO


@dataclasses.dataclass(frozen=True, slots=True)
class ClientRequestV1(WireDTO):
    operation: str
    peer_id: int = 0
    username: str = ""
    message_id: int = 0
    message_ids: Sequence[int] = dataclasses.field(default_factory=tuple)
    user_ids: Sequence[int] = dataclasses.field(default_factory=tuple)
    chat_ids: Sequence[int] = dataclasses.field(default_factory=tuple)
    sender_id: int = 0
    limit: int = 0
    offset_id: int = 0
    before: int = 0
    after: int = 0
    cursor: str = ""
    query: str = ""
    handle: str = ""
    media_kind: str = ""
    folder_id: int = 0
    direction: str = ""

    @classmethod
    def from_dict(cls, value: Mapping[str, Any]) -> ClientRequestV1:
        operation = _require_str(value.get("operation"), "client.operation")
        if operation not in {
            "get_me",
            "get_message",
            "resolve_peer",
            "get_user",
            "get_users",
            "get_chat",
            "get_chats",
            "search_public_username",
            "get_messages",
            "get_message_around",
            "get_reply",
            "get_replies",
            "get_pinned_message",
            "get_album",
            "get_message_reactions",
            "get_poll",
            "get_history",
            "get_history_around",
            "search_messages",
            "search_messages_by_sender",
            "search_messages_by_media",
            "search_global_messages",
            "list_dialogs",
            "get_dialog",
            "list_pinned_dialogs",
            "get_common_chats",
            "get_saved_messages_context",
            "list_participants",
            "search_participants",
            "get_participant",
            "get_recent_senders",
            "inspect_media",
            "get_media_attributes",
            "get_message_media_group",
        }:
            raise ProtocolError(f"unknown client operation {operation!r}")
        peer_id = _optional_int(value.get("peer_id"), 0, "client.peer_id")
        message_id = _optional_int(value.get("message_id"), 0, "client.message_id")
        limit = _optional_int(value.get("limit"), 0, "client.limit")
        offset_id = _optional_int(value.get("offset_id"), 0, "client.offset_id")
        before = _optional_int(value.get("before"), 0, "client.before")
        after = _optional_int(value.get("after"), 0, "client.after")
        if limit < 0 or limit > 100 or offset_id < 0 or before < 0 or after < 0:
            raise ProtocolError("client pagination is outside its bounds")
        if operation == "get_message" and (peer_id == 0 or message_id <= 0):
            raise ProtocolError("get_message requires peer_id and message_id")
        if operation == "resolve_peer" and peer_id == 0 and not value.get("username"):
            raise ProtocolError("resolve_peer requires peer_id or username")
        return cls(
            operation,
            peer_id,
            _optional_str(value.get("username"), "", "client.username"),
            message_id,
            tuple(
                _optional_int(item, 0, f"client.message_ids[{i}]")
                for i, item in enumerate(value.get("message_ids", []))
            )
            if isinstance(value.get("message_ids", []), list)
            else (),
            tuple(
                _optional_int(item, 0, f"client.user_ids[{i}]")
                for i, item in enumerate(value.get("user_ids", []))
            )
            if isinstance(value.get("user_ids", []), list)
            else (),
            tuple(
                _optional_int(item, 0, f"client.chat_ids[{i}]")
                for i, item in enumerate(value.get("chat_ids", []))
            )
            if isinstance(value.get("chat_ids", []), list)
            else (),
            _optional_int(value.get("sender_id"), 0, "client.sender_id"),
            limit,
            offset_id,
            before,
            after,
            _optional_str(value.get("cursor"), "", "client.cursor"),
            _optional_str(value.get("query"), "", "client.query"),
            _optional_str(value.get("handle"), "", "client.handle"),
            _optional_str(value.get("media_kind"), "", "client.media_kind"),
            _optional_int(value.get("folder_id"), 0, "client.folder_id"),
            _optional_str(value.get("direction"), "", "client.direction"),
        )


@dataclasses.dataclass(frozen=True, slots=True)
class ClientResultV1(WireDTO):
    message: MessageV1 | None = None
    peer: Mapping[str, JSONValue] | None = None
    me: Mapping[str, JSONValue] | None = None
    messages: Sequence[MessageV1] = dataclasses.field(default_factory=tuple)
    users: Sequence[Mapping[str, JSONValue]] = dataclasses.field(default_factory=tuple)
    chats: Sequence[Mapping[str, JSONValue]] = dataclasses.field(default_factory=tuple)
    dialogs: Sequence[Mapping[str, JSONValue]] = dataclasses.field(default_factory=tuple)
    participants: Sequence[Mapping[str, JSONValue]] = dataclasses.field(default_factory=tuple)
    media: Mapping[str, JSONValue] | None = None
    page: Mapping[str, JSONValue] | None = None
    read_error: Mapping[str, JSONValue] | None = None
    next_offset_id: int = 0

    @classmethod
    def from_dict(cls, value: Mapping[str, Any]) -> ClientResultV1:
        raw_message = value.get("message")
        message = (
            MessageV1.from_dict(_require_object(raw_message, field="client.message"))
            if raw_message is not None
            else None
        )
        raw_messages = value.get("messages", [])
        if not isinstance(raw_messages, list) or len(raw_messages) > 100:
            raise ProtocolError("client.messages exceeds the collection limit")
        messages = tuple(
            MessageV1.from_dict(_require_object(item, field=f"client.messages[{i}]"))
            for i, item in enumerate(raw_messages)
        )
        peer = (
            _require_object(value["peer"], field="client.peer")
            if value.get("peer") is not None
            else None
        )
        me = (
            _require_object(value["me"], field="client.me") if value.get("me") is not None else None
        )
        collections: dict[str, tuple[Mapping[str, JSONValue], ...]] = {}
        for field in ("users", "chats", "dialogs", "participants"):
            raw_items = value.get(field, [])
            if not isinstance(raw_items, list) or len(raw_items) > 100:
                raise ProtocolError(f"client.{field} exceeds the collection limit")
            collections[field] = tuple(
                _require_object(item, field=f"client.{field}[{i}]")
                for i, item in enumerate(raw_items)
            )
        return cls(
            message,
            peer,
            me,
            messages,
            collections["users"],
            collections["chats"],
            collections["dialogs"],
            collections["participants"],
            _require_object(value["media"], field="client.media")
            if value.get("media") is not None
            else None,
            _require_object(value["page"], field="client.page")
            if value.get("page") is not None
            else None,
            _require_object(value["read_error"], field="client.read_error")
            if value.get("read_error") is not None
            else None,
            _optional_int(value.get("next_offset_id"), 0, "client.next_offset_id"),
        )
