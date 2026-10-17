"""Bounded HTTP, media, inline, and callback action helpers."""

from __future__ import annotations

import dataclasses
import json
from collections.abc import Awaitable, Callable, Mapping, Sequence
from typing import TYPE_CHECKING, Any, Protocol

from .errors import UnsupportedAPIError
from .protocol import (
    HTTPHeaderV1,
    HTTPRequestV1,
    HTTPResponseV1,
    JSONValue,
    OperationKind,
    OperationV1,
    ensure_json_value,
)

if TYPE_CHECKING:
    from .actions import Message


class ActionSink(Protocol):
    async def request(self, action: OperationV1) -> JSONValue: ...


def _result_object(value: JSONValue) -> dict[str, JSONValue]:
    return value if isinstance(value, dict) else {}


class HTTPClient:
    """Typed async HTTP client backed only by the host reverse-RPC channel."""

    def __init__(
        self,
        request_callback: Callable[..., Awaitable[Mapping[str, Any]]],
        capabilities: frozenset[str] = frozenset(),
    ) -> None:
        self._request_callback = request_callback
        self._capabilities = capabilities

    async def request(
        self,
        method: str,
        url: str,
        *,
        headers: Mapping[str, str] | None = None,
        body: bytes | bytearray = b"",
        timeout: float | None = None,
        max_response_bytes: int | None = None,
    ) -> HTTPResponseV1:
        if self._capabilities and "http" not in self._capabilities:
            raise UnsupportedAPIError(
                "Module.http", "the verified package must declare the http capability"
            )
        if method != method.upper() or method not in {
            "GET",
            "HEAD",
            "POST",
            "PUT",
            "PATCH",
            "DELETE",
        }:
            raise ValueError("unsupported HTTP method")
        if not isinstance(url, str) or not url or any(value in url for value in "\r\n"):
            raise ValueError("HTTP URL must be a non-empty string")
        if not isinstance(body, (bytes, bytearray)) or len(body) > 512 << 10:
            raise ValueError("HTTP request body exceeds the 512 KiB SDK limit")
        request = HTTPRequestV1(
            method,
            url,
            tuple(HTTPHeaderV1(name, value) for name, value in (headers or {}).items()),
            bytes(body),
            max_response_bytes or 0,
            int(timeout * 1000) if timeout is not None else 0,
        )
        return HTTPResponseV1.from_dict(await self._request_callback(request))


def http_json(response: HTTPResponseV1, *, max_bytes: int) -> Any:
    if max_bytes <= 0 or len(response.body) > max_bytes or response.truncated or response.rejected:
        raise ValueError("HTTP JSON response exceeds the caller limit")
    try:
        return json.loads(response.body.decode("utf-8"))
    except (UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise ValueError("HTTP response is not valid JSON") from exc


class MediaClient:
    """Bounded async media API; binary content is always streamed."""

    def __init__(self, sink: ActionSink) -> None:
        self._sink = sink

    @staticmethod
    def _stream(value: Any, field: str) -> Any:
        if isinstance(value, (bytes, bytearray, memoryview, str)) or (
            not hasattr(value, "read") and not hasattr(value, "write")
        ):
            raise TypeError(f"{field} must be a bounded binary file-like object")
        return value

    async def inspect(self, handle: str) -> dict[str, JSONValue]:
        if not isinstance(handle, str) or not handle:
            raise ValueError("media handle must be a non-empty opaque string")
        return _result_object(
            await self._sink.request(
                OperationV1(kind=OperationKind.MEDIA_INSPECT, target={"handle": handle})
            )
        )

    async def download(self, handle: str, destination: Any, *, size: int) -> dict[str, JSONValue]:
        self._stream(destination, "destination")
        if size <= 0 or size > 64 * 1024 * 1024:
            raise ValueError("media size is outside the 64 MiB limit")
        return _result_object(
            await self._sink.request(
                OperationV1(
                    kind=OperationKind.MEDIA_DOWNLOAD,
                    target={"handle": handle},
                    parameters={"size": size, "stream": True},
                )
            )
        )

    async def upload(
        self,
        source: Any,
        *,
        kind: str,
        filename: str = "",
        mime_type: str = "",
        size: int,
        checksum: str = "",
    ) -> dict[str, JSONValue]:
        source = self._stream(source, "source")
        if not hasattr(source, "read"):
            raise TypeError("source must be readable")
        if kind not in {"photo", "video", "audio", "voice", "document", "sticker", "animation"}:
            raise ValueError(f"unsupported media kind: {kind}")
        if size <= 0 or size > 64 * 1024 * 1024:
            raise ValueError("media size is outside the 64 MiB limit")
        return _result_object(
            await self._sink.request(
                OperationV1(
                    kind=OperationKind.MEDIA_UPLOAD,
                    target={},
                    parameters={
                        "kind": kind,
                        "file_name": filename,
                        "mime_type": mime_type,
                        "size": size,
                        "checksum": checksum,
                        "stream": True,
                    },
                )
            )
        )

    async def attach(
        self,
        handle: str,
        chat_id: int,
        *,
        caption: str = "",
        parse_mode: str = "",
        filename: str = "",
        mime_type: str = "",
        reply_to_message_id: int | None = None,
    ) -> dict[str, JSONValue]:
        if not isinstance(handle, str) or not handle:
            raise ValueError("media handle must be a non-empty opaque string")
        return _result_object(
            await self._sink.request(
                OperationV1(
                    kind=OperationKind.MEDIA_ATTACH,
                    target={"handle": handle, "chat_id": chat_id},
                    parameters={
                        "caption": caption,
                        "parse_mode": parse_mode,
                        "file_name": filename,
                        "mime_type": mime_type,
                        "reply_to_message_id": reply_to_message_id,
                    },
                )
            )
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
        self, callback: Callable[..., Any], args: tuple[Any, ...], *, expires_at: float, now: float
    ) -> str:
        self.sweep(now)
        if len(self._callbacks) >= self._capacity:
            raise UnsupportedAPIError("Inline.form callback allocation", "fewer live callbacks")
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
        for token in [key for key, (expiry, _, _) in self._callbacks.items() if expiry <= now]:
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
        self._sink, self._callbacks, self._clock = sink, callbacks, clock
        self._max_buttons = max_buttons

    async def answer(self, *args: Any, **kwargs: Any) -> bool:
        raise UnsupportedAPIError("Inline.answer", "a native inline-query adapter")

    async def form(self, *args: Any, **kwargs: Any) -> JSONValue:
        raise UnsupportedAPIError("Inline.form", "a native inline-form adapter")

    async def edit(self, *args: Any, **kwargs: Any) -> bool:
        raise UnsupportedAPIError("Inline.edit", "a native inline-message adapter")

    async def delete(self, *args: Any, **kwargs: Any) -> bool:
        raise UnsupportedAPIError("Inline.delete", "a native inline-message adapter")

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
                        callback, args, expires_at=now + ttl, now=now
                    )
                normalized_row.append(ensure_json_value(button, field="inline.button"))
            normalized.append(normalized_row)
        return normalized

    def __getattr__(self, name: str) -> Any:
        raise UnsupportedAPIError(f"Inline.{name}", "Inline.answer/form/edit/delete")


@dataclasses.dataclass(slots=True)
class InlineQuery:
    id: str
    sender_id: int
    query: str
    offset: str
    _inline: InlineProxy

    async def answer(
        self, results: Sequence[Mapping[str, Any]], *, cache_time: int = 0, is_personal: bool = True
    ) -> bool:
        return await self._inline.answer(
            self.id, results, cache_time=cache_time, is_personal=is_personal
        )


@dataclasses.dataclass(slots=True)
class CallbackQuery:
    id: str
    sender_id: int
    data: str
    chat_id: int | None
    message_id: int | None
    _sink: ActionSink
    message: Message | None = None

    async def answer(self, text: str = "", *, show_alert: bool = False) -> bool:
        raise UnsupportedAPIError("CallbackQuery.answer", "a native callback adapter")

    async def edit(self, text: str, *, reply_markup: JSONValue = None) -> bool:
        if self.chat_id is None or self.message_id is None:
            raise UnsupportedAPIError("CallbackQuery.edit", "callback message identifiers")
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
            raise UnsupportedAPIError("CallbackQuery.delete", "callback message identifiers")
        return bool(
            await self._sink.request(
                OperationV1(
                    kind=OperationKind.DELETE,
                    target={"chat_id": self.chat_id, "message_id": self.message_id},
                )
            )
        )


@dataclasses.dataclass(frozen=True, slots=True)
class DeletedMessages:
    update_id: str
    message_ids: tuple[int, ...]


__all__ = [
    "CallbackQuery",
    "CallbackRegistry",
    "DeletedMessages",
    "HTTPClient",
    "InlineProxy",
    "InlineQuery",
    "MediaClient",
    "http_json",
]
