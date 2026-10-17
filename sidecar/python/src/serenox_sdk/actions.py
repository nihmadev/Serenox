"""Finite action proxies used by isolated Python plugins."""

from __future__ import annotations

import dataclasses
from collections.abc import Awaitable, Callable, Mapping, Sequence
from typing import Any, Protocol

from .actions_auxiliary import (
    CallbackQuery,
    CallbackRegistry,
    DeletedMessages,
    HTTPClient,
    InlineProxy,
    InlineQuery,
    MediaClient,
    http_json,
)
from .client_read import ReadClient
from .errors import UnsupportedAPIError
from .moderation import Moderation
from .protocol import (
    JSONValue,
    MessageV1,
    OperationKind,
    OperationV1,
    ensure_json_value,
)


class ActionSink(Protocol):
    """The only authority an SDK proxy needs from the sidecar host."""

    async def request(self, action: OperationV1) -> JSONValue:
        """Submit one bounded RPC action and return its JSON result."""

    def notify(self, action: OperationV1) -> None:
        """Queue one bounded best-effort action or raise OverloadedError."""


@dataclasses.dataclass(slots=True)
class OutgoingMessage:
    """Mutable, transport-neutral view passed to pre-send hooks."""

    action_id: str
    chat_id: int
    text: str
    parse_mode: str = ""
    reply_to_message_id: int = 0
    _wire: dict[str, JSONValue] = dataclasses.field(default_factory=dict, repr=False)

    @classmethod
    def from_wire(cls, action: Mapping[str, JSONValue]) -> OutgoingMessage:
        return cls(
            action_id=str(action.get("id", "")),
            chat_id=int(action.get("chat_id", 0)),
            text=str(action.get("text", "")),
            parse_mode=str(action.get("parse_mode", "")),
            reply_to_message_id=int(action.get("reply_to_message_id", 0)),
            _wire=dict(action),
        )

    def to_wire(self) -> dict[str, JSONValue]:
        result = dict(self._wire)
        result["id"] = self.action_id
        result["kind"] = "send"
        result["chat_id"] = self.chat_id
        result["text"] = self.text
        result["parse_mode"] = self.parse_mode
        if self.reply_to_message_id:
            result["reply_to_message_id"] = self.reply_to_message_id
        else:
            result.pop("reply_to_message_id", None)
        return result


def _result_object(value: JSONValue) -> dict[str, JSONValue]:
    if not isinstance(value, dict):
        return {}
    return value


@dataclasses.dataclass(slots=True)
class Message:
    """Telegram-library-independent message compatibility object."""

    dto: MessageV1
    _sink: ActionSink

    @classmethod
    def from_dict(cls, value: Mapping[str, Any], sink: ActionSink) -> Message:
        return cls(MessageV1.from_dict(value), sink)

    @property
    def id(self) -> int:
        return self.dto.id

    @property
    def message_id(self) -> int:
        return self.dto.id

    @property
    def chat_id(self) -> int:
        return self.dto.chat_id

    @property
    def sender_id(self) -> int | None:
        return self.dto.sender_id

    @property
    def text(self) -> str:
        return self.dto.text

    @property
    def raw_text(self) -> str:
        return self.dto.text

    @property
    def outgoing(self) -> bool:
        return self.dto.outgoing

    @property
    def out(self) -> bool:
        return self.dto.outgoing

    @property
    def is_reply(self) -> bool:
        return self.dto.reply_to_message_id is not None

    @property
    def reply_to_message_id(self) -> int | None:
        """Read-only ID of the replied message, if Telegram exposed one."""

        return self.dto.reply_to_message_id

    @property
    def chat_kind(self) -> str:
        return self.dto.chat_kind

    @property
    def forwarded(self) -> bool:
        return self.dto.forwarded

    @property
    def via_bot(self) -> bool:
        return self.dto.via_bot

    @property
    def media(self) -> Mapping[str, JSONValue] | None:
        return None if self.dto.media is None else self.dto.media.to_dict()

    async def get_reply_message(self) -> Message | None:
        """Fetch the replied message through the bounded typed read service."""

        if self.reply_to_message_id is None:
            return None
        value = await self._sink.request(
            OperationV1(
                kind=OperationKind.GET_MESSAGE,
                target={"chat_id": self.chat_id, "message_id": self.reply_to_message_id},
            )
        )
        result = _result_object(value)
        raw = result.get("message", value)
        return Message.from_dict(raw, self._sink) if isinstance(raw, Mapping) else None

    def moderation_reply_target(self) -> dict[str, JSONValue]:
        if self.reply_to_message_id is None:
            raise ValueError("message is not a reply")
        return {"reply": {"peer_id": self.chat_id, "message_id": self.reply_to_message_id}}

    async def reply(
        self,
        text: str,
        *,
        parse_mode: str = "html",
        reply_markup: JSONValue = None,
        idempotency_key: str = "",
    ) -> Message:
        value = await self._sink.request(
            OperationV1(
                kind=OperationKind.REPLY,
                target={"chat_id": self.chat_id, "message_id": self.id},
                parameters={
                    "text": text,
                    "parse_mode": parse_mode,
                    "reply_markup": ensure_json_value(reply_markup, field="reply_markup"),
                    "idempotency_key": idempotency_key,
                },
            )
        )
        return _message_from_action_result(value, self._sink, fallback=self.dto)

    async def edit(
        self,
        text: str,
        *,
        parse_mode: str = "html",
        reply_markup: JSONValue = None,
        idempotency_key: str = "",
    ) -> Message:
        value = await self._sink.request(
            OperationV1(
                kind=OperationKind.EDIT,
                target={"chat_id": self.chat_id, "message_id": self.id},
                parameters={
                    "text": text,
                    "parse_mode": parse_mode,
                    "reply_markup": ensure_json_value(reply_markup, field="reply_markup"),
                    "idempotency_key": idempotency_key,
                },
            )
        )
        return _message_from_action_result(value, self._sink, fallback=self.dto)

    async def delete(self, *, idempotency_key: str = "") -> bool:
        value = await self._sink.request(
            OperationV1(
                kind=OperationKind.DELETE,
                target={"chat_id": self.chat_id, "message_id": self.id},
                parameters={"idempotency_key": idempotency_key},
            )
        )
        if isinstance(value, bool):
            return value
        result = _result_object(value)
        deleted = result.get("deleted", True)
        return bool(deleted)

    async def answer(
        self,
        text: str,
        *,
        parse_mode: str = "html",
        reply_markup: JSONValue = None,
    ) -> Message:
        """Legacy answer semantics: edit outgoing messages and reply otherwise."""

        method = self.edit if self.outgoing else self.reply
        return await method(text, parse_mode=parse_mode, reply_markup=reply_markup)

    def __getattr__(self, name: str) -> Any:
        raise UnsupportedAPIError(
            f"Message.{name}",
            "Message.reply/edit/delete or an explicit Client operation",
        )


class ClientProxy:
    """Finite client-operation surface implemented by core-side adapters."""

    def __init__(self, sink: ActionSink) -> None:
        self._sink = sink
        self.read = ReadClient(sink)
        self.identity = self.read.identity
        self.peers = self.read.peers
        self.users = self.read.users
        self.chats = self.read.chats
        self.messages = self.read.messages
        self.history = self.read.history
        self.search = self.read.search
        self.dialogs = self.read.dialogs
        self.participants = self.read.participants
        self.media_metadata = self.read.media_metadata
        self.moderation = Moderation(sink)
        self.profile = ProfileClient(sink)

    async def send_message(
        self,
        chat_id: int,
        text: str,
        *,
        reply_to_message_id: int | None = None,
        parse_mode: str = "html",
        reply_markup: JSONValue = None,
        idempotency_key: str = "",
    ) -> Message:
        value = await self._sink.request(
            OperationV1(
                kind=OperationKind.SEND,
                target={"chat_id": chat_id},
                parameters={
                    "text": text,
                    "reply_to_message_id": reply_to_message_id,
                    "parse_mode": parse_mode,
                    "reply_markup": ensure_json_value(reply_markup, field="reply_markup"),
                    "idempotency_key": idempotency_key,
                },
            )
        )
        fallback = MessageV1(id=0, chat_id=chat_id, text=text, outgoing=True)
        return _message_from_action_result(value, self._sink, fallback=fallback)

    async def edit_message(
        self,
        chat_id: int,
        message_id: int,
        text: str,
        *,
        parse_mode: str = "html",
        reply_markup: JSONValue = None,
        idempotency_key: str = "",
    ) -> Message:
        message = Message(MessageV1(id=message_id, chat_id=chat_id, outgoing=True), self._sink)
        return await message.edit(
            text, parse_mode=parse_mode, reply_markup=reply_markup, idempotency_key=idempotency_key
        )

    async def delete_messages(
        self, chat_id: int, message_ids: int | Sequence[int], *, idempotency_key: str = ""
    ) -> bool:
        ids = [message_ids] if isinstance(message_ids, int) else list(message_ids)
        if not ids or len(ids) > 100:
            raise ValueError("delete_messages requires between 1 and 100 message IDs")
        value = await self._sink.request(
            OperationV1(
                kind=OperationKind.DELETE,
                target={"chat_id": chat_id},
                parameters={
                    "message_ids": ensure_json_value(ids, field="message_ids"),
                    "idempotency_key": idempotency_key,
                },
            )
        )
        return bool(
            value if isinstance(value, bool) else _result_object(value).get("deleted", True)
        )

    async def get_message(self, chat_id: int, message_id: int) -> Message | None:
        value = await self._sink.request(
            OperationV1(
                kind=OperationKind.GET_MESSAGE,
                target={"chat_id": chat_id, "message_id": message_id},
            )
        )
        result = _result_object(value)
        message_value: Any = result.get("message", value)
        return (
            Message.from_dict(message_value, self._sink)
            if isinstance(message_value, Mapping)
            else None
        )

    async def get_messages(self, chat_id: int, message_ids: int | Sequence[int]) -> list[Message]:
        ids = [message_ids] if isinstance(message_ids, int) else list(message_ids)
        if len(ids) > 100:
            raise ValueError("get_messages accepts at most 100 message IDs")
        messages: list[Message] = []
        for message_id in ids:
            message = await self.get_message(chat_id, message_id)
            if message is not None:
                messages.append(message)
        return messages

    async def get_me(self) -> dict[str, JSONValue]:
        value = await self._sink.request(OperationV1(kind=OperationKind.GET_ME, target={}))
        return _result_object(value)

    async def get_entity(self, peer: int | str) -> dict[str, JSONValue]:
        value = await self._sink.request(
            OperationV1(kind=OperationKind.RESOLVE_PEER, target={"peer": peer})
        )
        return _result_object(value)

    async def join_chat(self, chat: int | str) -> dict[str, JSONValue]:
        raise UnsupportedAPIError(
            "Client.join_chat",
            "the native Telegram user-session adapter does not implement chat membership actions",
        )

    async def leave_chat(self, chat: int | str) -> bool:
        raise UnsupportedAPIError(
            "Client.leave_chat",
            "the native Telegram user-session adapter does not implement chat membership actions",
        )

    async def send_reaction(self, chat_id: int, message_id: int, reaction: str) -> bool:
        raise UnsupportedAPIError(
            "Client.send_reaction",
            "the native Telegram user-session adapter does not implement reactions",
        )

    def __getattr__(self, name: str) -> Any:
        raise UnsupportedAPIError(
            f"Client.{name}",
            "one of send_message, edit_message, delete_messages, get_message, "
            "get_messages, get_me, get_entity, join_chat, leave_chat, or send_reaction",
        )


def _message_from_action_result(
    value: JSONValue, sink: ActionSink, *, fallback: MessageV1
) -> Message:
    result = _result_object(value)
    message_value: Any = result.get("message")
    if isinstance(message_value, Mapping):
        return Message.from_dict(message_value, sink)
    return Message(fallback, sink)


class ProfileClient:
    """Typed first/last-name lease and mutation surface."""

    def __init__(self, sink: ActionSink) -> None:
        self._sink = sink

    async def acquire(self, *, idempotency_key: str) -> Mapping[str, JSONValue]:
        return await self._call("acquire", idempotency_key=idempotency_key)

    async def update(
        self,
        first_name: str,
        last_name: str = "",
        *,
        generation: int,
        lease_token: str,
        idempotency_key: str,
    ) -> Mapping[str, JSONValue]:
        return await self._call(
            "update",
            idempotency_key=idempotency_key,
            first_name=first_name,
            last_name=last_name,
            generation=generation,
            lease_token=lease_token,
        )

    async def restore(
        self, *, generation: int, lease_token: str, idempotency_key: str
    ) -> Mapping[str, JSONValue]:
        return await self._call(
            "restore",
            idempotency_key=idempotency_key,
            generation=generation,
            lease_token=lease_token,
        )

    async def release(
        self, *, generation: int, lease_token: str, idempotency_key: str
    ) -> Mapping[str, JSONValue]:
        return await self._call(
            "release",
            idempotency_key=idempotency_key,
            generation=generation,
            lease_token=lease_token,
        )

    async def _call(
        self, operation: str, *, idempotency_key: str, **values: JSONValue
    ) -> Mapping[str, JSONValue]:
        value = await self._sink.request(
            OperationV1(
                kind=OperationKind.PROFILE,
                target={},
                parameters={"operation": operation, "idempotency_key": idempotency_key, **values},
            )
        )
        return _result_object(value)


class RecordingActionSink:
    """Deterministic in-memory sink for SDK tests and plugin fixtures."""

    def __init__(
        self,
        response: JSONValue = None,
        responder: Callable[[OperationV1], Awaitable[JSONValue]] | None = None,
        *,
        capacity: int = 1_024,
    ) -> None:
        self.response = response
        self.responder = responder
        self.capacity = capacity
        self.actions: list[OperationV1] = []

    async def request(self, action: OperationV1) -> JSONValue:
        self.notify(action)
        if self.responder is not None:
            return await self.responder(action)
        return self.response

    def notify(self, action: OperationV1) -> None:
        if len(self.actions) >= self.capacity:
            from .errors import OverloadedError

            raise OverloadedError("recording action sink is full")
        self.actions.append(action)


__all__ = [
    "ActionSink",
    "CallbackQuery",
    "CallbackRegistry",
    "ClientProxy",
    "DeletedMessages",
    "HTTPClient",
    "InlineProxy",
    "InlineQuery",
    "MediaClient",
    "Message",
    "ProfileClient",
    "RecordingActionSink",
    "http_json",
]
