"""Bounded uint32-big-endian JSON framing for protocol v1."""

from __future__ import annotations

import asyncio
import contextlib
import json
import struct
from collections.abc import Mapping
from typing import Any

from serenox_sdk.errors import ConnectionClosedError, ProtocolError
from serenox_sdk.protocol import JSONValue, ensure_json_value

HEADER_SIZE = 4
DEFAULT_MAX_FRAME_BYTES = 1 << 20


class FrameTooLargeError(ProtocolError):
    code = "frame_too_large"


class MalformedFrameError(ProtocolError):
    code = "malformed_frame"


class FramedJSONStream:
    """Read/write JSON objects with one allocation bounded by max_frame_bytes."""

    def __init__(
        self,
        reader: asyncio.StreamReader,
        writer: asyncio.StreamWriter,
        *,
        max_frame_bytes: int = DEFAULT_MAX_FRAME_BYTES,
    ) -> None:
        if max_frame_bytes < 256 or max_frame_bytes > 64 << 20:
            raise ValueError("max_frame_bytes must be between 256 bytes and 64 MiB")
        self.reader = reader
        self.writer = writer
        self.max_frame_bytes = max_frame_bytes
        self._write_lock = asyncio.Lock()

    async def read(self) -> dict[str, JSONValue]:
        try:
            header = await self.reader.readexactly(HEADER_SIZE)
        except asyncio.IncompleteReadError as exc:
            if not exc.partial:
                raise ConnectionClosedError("peer closed the framed stream") from exc
            raise MalformedFrameError(
                f"truncated frame header: received {len(exc.partial)} of {HEADER_SIZE} bytes"
            ) from exc
        (size,) = struct.unpack(">I", header)
        if size == 0:
            raise MalformedFrameError("zero-length JSON frames are forbidden")
        if size > self.max_frame_bytes:
            raise FrameTooLargeError(
                f"frame declares {size} bytes; configured limit is {self.max_frame_bytes}"
            )
        try:
            data = await self.reader.readexactly(size)
        except asyncio.IncompleteReadError as exc:
            raise MalformedFrameError(
                f"truncated frame body: received {len(exc.partial)} of {size} bytes"
            ) from exc
        try:
            value = json.loads(data)
        except (UnicodeDecodeError, json.JSONDecodeError) as exc:
            raise MalformedFrameError(f"malformed JSON frame: {exc}") from exc
        if not isinstance(value, Mapping):
            raise MalformedFrameError("frame root must be a JSON object")
        checked = ensure_json_value(value, field="frame")
        if not isinstance(checked, dict):
            raise MalformedFrameError("frame root must be a JSON object")
        return checked

    async def write(self, value: Mapping[str, Any]) -> None:
        checked = ensure_json_value(value, field="frame")
        data = json.dumps(
            checked, ensure_ascii=False, allow_nan=False, separators=(",", ":")
        ).encode("utf-8")
        if not data:
            raise MalformedFrameError("refusing to write an empty frame")
        if len(data) > self.max_frame_bytes:
            raise FrameTooLargeError(
                f"encoded frame is {len(data)} bytes; configured limit is {self.max_frame_bytes}"
            )
        async with self._write_lock:
            self.writer.write(struct.pack(">I", len(data)))
            self.writer.write(data)
            await self.writer.drain()

    async def close(self) -> None:
        self.writer.close()
        with contextlib.suppress(BrokenPipeError, ConnectionResetError):
            await self.writer.wait_closed()


def encode_frame(
    value: Mapping[str, Any], *, max_frame_bytes: int = DEFAULT_MAX_FRAME_BYTES
) -> bytes:
    """Synchronous fixture helper using the same strict limits as FramedJSONStream."""

    checked = ensure_json_value(value, field="frame")
    data = json.dumps(checked, ensure_ascii=False, allow_nan=False, separators=(",", ":")).encode(
        "utf-8"
    )
    if not data or len(data) > max_frame_bytes:
        raise FrameTooLargeError(f"encoded frame size {len(data)} is outside 1..{max_frame_bytes}")
    return struct.pack(">I", len(data)) + data


__all__ = [
    "DEFAULT_MAX_FRAME_BYTES",
    "FrameTooLargeError",
    "FramedJSONStream",
    "MalformedFrameError",
    "encode_frame",
]
