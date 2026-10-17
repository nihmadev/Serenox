"""Internal protocol component: update, message, command, and inline DTOs."""

from __future__ import annotations

import dataclasses
from collections.abc import Mapping, Sequence
from typing import Any

from .errors import ProtocolError
from .protocol_constants import JSONValue, UpdateKind
from .protocol_media import MediaV1
from .protocol_validation import (
    _nullable_int,
    _optional_bool,
    _optional_int,
    _optional_str,
    _require_int,
    _require_object,
    _require_str,
)
from .protocol_wire import WireDTO


@dataclasses.dataclass(frozen=True, slots=True)
class EntityV1(WireDTO):
    type: str
    offset: int
    length: int
    url: str = ""
    user_id: int = 0
    language: str = ""


@dataclasses.dataclass(frozen=True, slots=True)
class MessageV1(WireDTO):
    id: int
    chat_id: int
    sender_id: int | None = None
    date_unix_ms: int = 0
    edit_date_unix_ms: int = 0
    text: str = ""
    outgoing: bool = False
    mentioned: bool = False
    reply_to_message_id: int | None = None
    entities: Sequence[EntityV1] = dataclasses.field(default_factory=tuple)
    chat_kind: str = ""
    via_bot: bool = False
    forwarded: bool = False
    media: MediaV1 | None = None
    peer: Mapping[str, JSONValue] | None = None
    sender: Mapping[str, JSONValue] | None = None
    reply_to: Mapping[str, JSONValue] | None = None
    forward: Mapping[str, JSONValue] | None = None
    grouped_id: int = 0
    service: Mapping[str, JSONValue] | None = None
    reactions: Mapping[str, JSONValue] | None = None
    poll: Mapping[str, JSONValue] | None = None

    @classmethod
    def from_dict(cls, value: Mapping[str, Any]) -> MessageV1:
        raw_entities = value.get("entities", [])
        if not isinstance(raw_entities, list):
            raise ProtocolError("message.entities must be a list")
        entities = tuple(_entity_from_dict(raw, index) for index, raw in enumerate(raw_entities))
        media_value = value.get("media")
        media: MediaV1 | None = None
        if media_value is not None:
            media_item = _require_object(media_value, field="message.media")
            media_size = _optional_int(media_item.get("size"), 0, "message.media.size")
            if media_size < 0:
                raise ProtocolError("message.media.size must not be negative")
            media = MediaV1(
                kind=_require_str(media_item.get("kind"), "message.media.kind"),
                handle=_optional_str(media_item.get("handle"), "", "message.media.handle"),
                file_id=_optional_str(media_item.get("file_id"), "", "message.media.file_id"),
                file_name=_optional_str(media_item.get("file_name"), "", "message.media.file_name"),
                mime_type=_optional_str(media_item.get("mime_type"), "", "message.media.mime_type"),
                size=media_size,
                checksum=_optional_str(media_item.get("checksum"), "", "message.media.checksum"),
                source=media_item.get("source")
                if isinstance(media_item.get("source"), Mapping)
                else None,
                attributes=media_item.get("attributes")
                if isinstance(media_item.get("attributes"), Mapping)
                else None,
            )
        return cls(
            id=_require_int(value.get("id"), "message.id"),
            chat_id=_require_int(value.get("chat_id"), "message.chat_id"),
            sender_id=_nullable_int(value.get("sender_id"), "message.sender_id"),
            date_unix_ms=_optional_int(value.get("date_unix_ms"), 0, "message.date_unix_ms"),
            edit_date_unix_ms=_optional_int(
                value.get("edit_date_unix_ms"), 0, "message.edit_date_unix_ms"
            ),
            text=_optional_str(value.get("text"), "", "message.text"),
            outgoing=_optional_bool(value.get("outgoing"), False, "message.outgoing"),
            mentioned=_optional_bool(value.get("mentioned"), False, "message.mentioned"),
            reply_to_message_id=_nullable_int(
                value.get("reply_to_message_id"), "message.reply_to_message_id"
            ),
            entities=entities,
            chat_kind=_optional_str(value.get("chat_kind"), "", "message.chat_kind"),
            via_bot=_optional_bool(value.get("via_bot"), False, "message.via_bot"),
            forwarded=_optional_bool(value.get("forwarded"), False, "message.forwarded"),
            media=media,
            peer=value.get("peer") if isinstance(value.get("peer"), Mapping) else None,
            sender=value.get("sender") if isinstance(value.get("sender"), Mapping) else None,
            reply_to=value.get("reply_to") if isinstance(value.get("reply_to"), Mapping) else None,
            forward=value.get("forward") if isinstance(value.get("forward"), Mapping) else None,
            grouped_id=_optional_int(value.get("grouped_id"), 0, "message.grouped_id"),
            service=value.get("service") if isinstance(value.get("service"), Mapping) else None,
            reactions=value.get("reactions")
            if isinstance(value.get("reactions"), Mapping)
            else None,
            poll=value.get("poll") if isinstance(value.get("poll"), Mapping) else None,
        )


@dataclasses.dataclass(frozen=True, slots=True)
class CallbackV1(WireDTO):
    id: str
    sender_id: int
    chat_id: int = 0
    message_id: int = 0
    data: str = ""
    message: MessageV1 | None = None


@dataclasses.dataclass(frozen=True, slots=True)
class InlineQueryV1(WireDTO):
    id: str
    sender_id: int
    query: str = ""
    offset: str = ""


@dataclasses.dataclass(frozen=True, slots=True)
class ControlBotIdentityV1(WireDTO):
    id: int
    username: str = ""


@dataclasses.dataclass(frozen=True, slots=True)
class ControlActorV1(WireDTO):
    id: int
    username: str = ""


@dataclasses.dataclass(frozen=True, slots=True)
class ControlEventV1(WireDTO):
    bot: ControlBotIdentityV1
    actor: ControlActorV1
    chat_id: int = 0
    message_id: int = 0
    inline_message_id: str = ""
    chat_instance: str = ""
    query_id: str = ""
    query: str = ""
    offset: str = ""
    chat_type: str = ""
    callback_data: str = ""
    command: str = ""
    command_arguments: str = ""


@dataclasses.dataclass(frozen=True, slots=True)
class UpdateV1(WireDTO):
    id: str
    kind: UpdateKind | str
    occurred_unix_ms: int
    message: MessageV1 | None = None
    deleted_ids: Sequence[int] = dataclasses.field(default_factory=tuple)
    callback: CallbackV1 | None = None
    inline_query: InlineQueryV1 | None = None
    control: ControlEventV1 | None = None


@dataclasses.dataclass(frozen=True, slots=True)
class CommandV1(WireDTO):
    name: str
    prefix: str
    message: MessageV1
    raw_args: str = ""
    args: Sequence[str] = dataclasses.field(default_factory=tuple)


@dataclasses.dataclass(frozen=True, slots=True)
class InlineButtonV1(WireDTO):
    text: str
    callback_data: str = ""
    url: str = ""
    switch_query: str = ""


@dataclasses.dataclass(frozen=True, slots=True)
class InlineResultV1(WireDTO):
    id: str
    type: str
    title: str = ""
    description: str = ""
    text: str = ""
    buttons: Sequence[Sequence[InlineButtonV1]] = dataclasses.field(default_factory=tuple)


def _entity_from_dict(value: Any, index: int) -> EntityV1:
    item = _require_object(value, field=f"message.entities[{index}]")
    return EntityV1(
        type=_require_str(item.get("type"), f"message.entities[{index}].type"),
        offset=_require_int(item.get("offset"), f"message.entities[{index}].offset"),
        length=_require_int(item.get("length"), f"message.entities[{index}].length"),
        url=_optional_str(item.get("url"), "", f"message.entities[{index}].url"),
        user_id=_optional_int(item.get("user_id"), 0, f"message.entities[{index}].user_id"),
        language=_optional_str(item.get("language"), "", f"message.entities[{index}].language"),
    )
