"""Finite action proxies used by isolated Python plugins."""

from __future__ import annotations

import dataclasses
from collections.abc import Awaitable, Callable, Mapping, Sequence
from typing import Any, Protocol

from .errors import UnsupportedAPIError
from .protocol import JSONValue, MessageV1, OperationKind, OperationV1, ensure_json_value


class ActionSink(Protocol):
    """The only authority an SDK proxy needs from the sidecar host."""

    async def request(self, action: OperationV1) -> JSONValue:
        """Submit one bounded RPC action and return its JSON result."""

    def notify(self, action: OperationV1) -> None:
        """Queue one bounded best-effort action or raise OverloadedError."""


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

    async def reply(
        self,
        text: str,
        *,
        parse_mode: str = "html",
        reply_markup: JSONValue = None,
    ) -> Message:
        value = await self._sink.request(
            OperationV1(
                kind=OperationKind.REPLY,
                target={"chat_id": self.chat_id, "message_id": self.id},
                parameters={
                    "text": text,
                    "parse_mode": parse_mode,
                    "reply_markup": ensure_json_value(reply_markup, field="reply_markup"),
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
    ) -> Message:
        value = await self._sink.request(
            OperationV1(
                kind=OperationKind.EDIT,
                target={"chat_id": self.chat_id, "message_id": self.id},
                parameters={
                    "text": text,
                    "parse_mode": parse_mode,
                    "reply_markup": ensure_json_value(reply_markup, field="reply_markup"),
                },
            )
        )
        return _message_from_action_result(value, self._sink, fallback=self.dto)

    async def delete(self) -> bool:
        value = await self._sink.request(
            OperationV1(
                kind=OperationKind.DELETE,
                target={"chat_id": self.chat_id, "message_id": self.id},
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

    async def send_message(
        self,
        chat_id: int,
        text: str,
        *,
        reply_to_message_id: int | None = None,
        parse_mode: str = "html",
        reply_markup: JSONValue = None,
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
    ) -> Message:
        message = Message(MessageV1(id=message_id, chat_id=chat_id, outgoing=True), self._sink)
        return await message.edit(text, parse_mode=parse_mode, reply_markup=reply_markup)

    async def delete_messages(self, chat_id: int, message_ids: int | Sequence[int]) -> bool:
        ids = [message_ids] if isinstance(message_ids, int) else list(message_ids)
        if not ids or len(ids) > 100:
            raise ValueError("delete_messages requires between 1 and 100 message IDs")
        value = await self._sink.request(
            OperationV1(
                kind=OperationKind.DELETE,
                target={"chat_id": chat_id},
                parameters={"message_ids": ensure_json_value(ids, field="message_ids")},
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
        value = await self._sink.request(
            OperationV1(kind=OperationKind.JOIN_CHAT, target={"chat": chat})
        )
        return _result_object(value)

    async def leave_chat(self, chat: int | str) -> bool:
        value = await self._sink.request(
            OperationV1(kind=OperationKind.LEAVE_CHAT, target={"chat": chat})
        )
        return bool(value)

    async def send_reaction(self, chat_id: int, message_id: int, reaction: str) -> bool:
        value = await self._sink.request(
            OperationV1(
                kind=OperationKind.REACT,
                target={"chat_id": chat_id, "message_id": message_id},
                parameters={"reaction": reaction},
            )
        )
        return bool(value)

    def __getattr__(self, name: str) -> Any:
        raise UnsupportedAPIError(
            f"Client.{name}",
            "one of send_message, edit_message, delete_messages, get_message, "
            "get_messages, get_me, get_entity, join_chat, leave_chat, or send_reaction",
        )


class CallbackRegistry:
    """Bounded callback table swept opportunistically without per-item timers."""

    def __init__(self, capacity: int = 256) -> None:
        if capacity < 1:
            raise ValueError("callback capacity must be positive")
        self._capacity = capacity
        self._callbacks: dict[str, tuple[float, Callable[..., Any], tuple[Any, ...]]] = {}
        self._sequence = 0

    def register(
        self,
        callback: Callable[..., Any],
        args: tuple[Any, ...],
        *,
        expires_at: float,
        now: float,
    ) -> str:
        self.sweep(now)
        if len(self._callbacks) >= self._capacity:
            raise UnsupportedAPIError(
                "Inline.form callback allocation",
                "fewer live callbacks or a larger configured callback_capacity",
            )
        self._sequence = (self._sequence + 1) % (2**63)
        token = f"sdk-{self._sequence:x}"
        self._callbacks[token] = (expires_at, callback, args)
        return token

    def resolve(
        self, token: str, *, now: float
    ) -> tuple[Callable[..., Any], tuple[Any, ...]] | None:
        self.sweep(now)
        entry = self._callbacks.get(token)
        return None if entry is None else (entry[1], entry[2])

    def sweep(self, now: float) -> None:
        expired = [token for token, (expiry, _, _) in self._callbacks.items() if expiry <= now]
        for token in expired:
            del self._callbacks[token]

    def __len__(self) -> int:
        return len(self._callbacks)


class InlineProxy:
    """Inline actions and a bounded registry for Python callback callables."""

    def __init__(
        self,
        sink: ActionSink,
        callbacks: CallbackRegistry,
        *,
        clock: Callable[[], float],
        max_buttons: int = 100,
    ) -> None:
        self._sink = sink
        self._callbacks = callbacks
        self._clock = clock
        self._max_buttons = max_buttons

    async def answer(
        self,
        query_id: str,
        results: Sequence[Mapping[str, Any]],
        *,
        cache_time: int = 0,
        is_personal: bool = True,
    ) -> bool:
        if len(results) > 50:
            raise ValueError("Telegram permits at most 50 inline results")
        value = await self._sink.request(
            OperationV1(
                kind=OperationKind.INLINE_ANSWER,
                target={"query_id": query_id},
                parameters={
                    "results": ensure_json_value(list(results), field="inline.results"),
                    "cache_time": cache_time,
                    "is_personal": is_personal,
                },
            )
        )
        return bool(value)

    async def form(
        self,
        text: str,
        *,
        message: Message | int | None = None,
        reply_markup: Sequence[Sequence[Mapping[str, Any]]] | None = None,
        ttl: int = 300,
    ) -> JSONValue:
        if ttl < 1 or ttl > 86_400:
            raise ValueError("inline form ttl must be between 1 and 86400 seconds")
        markup = self._normalize_markup(reply_markup or (), ttl=ttl)
        target: dict[str, JSONValue] = {}
        if isinstance(message, Message):
            target = {"chat_id": message.chat_id, "message_id": message.id}
        elif isinstance(message, int):
            target = {"chat_id": message}
        return await self._sink.request(
            OperationV1(
                kind=OperationKind.INLINE_FORM,
                target=target,
                parameters={"text": text, "reply_markup": markup, "ttl_seconds": ttl},
            )
        )

    async def edit(
        self,
        inline_message_id: str,
        text: str,
        *,
        reply_markup: Sequence[Sequence[Mapping[str, Any]]] | None = None,
        ttl: int = 300,
    ) -> bool:
        markup = self._normalize_markup(reply_markup or (), ttl=ttl)
        value = await self._sink.request(
            OperationV1(
                kind=OperationKind.INLINE_EDIT,
                target={"inline_message_id": inline_message_id},
                parameters={"text": text, "reply_markup": markup},
            )
        )
        return bool(value)

    async def delete(self, inline_message_id: str) -> bool:
        value = await self._sink.request(
            OperationV1(
                kind=OperationKind.INLINE_DELETE,
                target={"inline_message_id": inline_message_id},
            )
        )
        return bool(value)

    def _normalize_markup(
        self, rows: Sequence[Sequence[Mapping[str, Any]]], *, ttl: int
    ) -> list[JSONValue]:
        if sum(len(row) for row in rows) > self._max_buttons:
            raise ValueError(f"inline markup exceeds the {self._max_buttons}-button limit")
        now = self._clock()
        normalized: list[JSONValue] = []
        for row in rows:
            normalized_row: list[JSONValue] = []
            for original in row:
                button = dict(original)
                callback = button.get("callback")
                if callable(callback):
                    args_value = button.pop("args", ())
                    args = (
                        tuple(args_value)
                        if isinstance(args_value, (list, tuple))
                        else (args_value,)
                    )
                    for index, arg in enumerate(args):
                        ensure_json_value(arg, field=f"inline.callback.args[{index}]")
                    button["callback"] = self._callbacks.register(
                        callback,
                        args,
                        expires_at=now + ttl,
                        now=now,
                    )
                normalized_row.append(ensure_json_value(button, field="inline.button"))
            normalized.append(normalized_row)
        return normalized

    def __getattr__(self, name: str) -> Any:
        raise UnsupportedAPIError(
            f"Inline.{name}", "Inline.answer/form/edit/delete or a native Go inline module"
        )


@dataclasses.dataclass(slots=True)
class InlineQuery:
    """Library-independent inline query passed to decorated handlers."""

    id: str
    sender_id: int
    query: str
    offset: str
    _inline: InlineProxy

    async def answer(
        self,
        results: Sequence[Mapping[str, Any]],
        *,
        cache_time: int = 0,
        is_personal: bool = True,
    ) -> bool:
        return await self._inline.answer(
            self.id, results, cache_time=cache_time, is_personal=is_personal
        )


@dataclasses.dataclass(slots=True)
class CallbackQuery:
    """Library-independent callback event with explicit inline actions."""

    id: str
    sender_id: int
    data: str
    chat_id: int | None
    message_id: int | None
    _sink: ActionSink

    async def answer(self, text: str = "", *, show_alert: bool = False) -> bool:
        value = await self._sink.request(
            OperationV1(
                kind=OperationKind.INLINE_ANSWER,
                target={"callback_id": self.id},
                parameters={"text": text, "show_alert": show_alert},
            )
        )
        return bool(value)

    async def edit(self, text: str, *, reply_markup: JSONValue = None) -> bool:
        if self.chat_id is None or self.message_id is None:
            raise UnsupportedAPIError(
                "CallbackQuery.edit without chat/message IDs",
                "an inline callback update that carries message identifiers",
            )
        value = await self._sink.request(
            OperationV1(
                kind=OperationKind.EDIT,
                target={"chat_id": self.chat_id, "message_id": self.message_id},
                parameters={
                    "text": text,
                    "reply_markup": ensure_json_value(reply_markup, field="reply_markup"),
                },
            )
        )
        return bool(value)

    async def delete(self) -> bool:
        if self.chat_id is None or self.message_id is None:
            raise UnsupportedAPIError(
                "CallbackQuery.delete without chat/message IDs",
                "an inline callback update that carries message identifiers",
            )
        value = await self._sink.request(
            OperationV1(
                kind=OperationKind.DELETE,
                target={"chat_id": self.chat_id, "message_id": self.message_id},
            )
        )
        return bool(value)


def _message_from_action_result(
    value: JSONValue, sink: ActionSink, *, fallback: MessageV1
) -> Message:
    result = _result_object(value)
    message_value: Any = result.get("message", value)
    if isinstance(message_value, Mapping):
        return Message.from_dict(message_value, sink)
    return Message(fallback, sink)


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
    "InlineProxy",
    "InlineQuery",
    "Message",
    "RecordingActionSink",
]
