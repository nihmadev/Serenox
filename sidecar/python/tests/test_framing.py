from __future__ import annotations

import asyncio
import json
import struct
import unittest
from typing import Any
from unittest.mock import AsyncMock, patch

from serenox_sdk.errors import ProtocolError
from serenox_sidecar.framing import (
    FramedJSONStream,
    FrameTooLargeError,
    MalformedFrameError,
    encode_frame,
)
from serenox_sidecar.transport import TCPConnector, UnixSocketConnector, connector_for


class _MemoryWriter:
    def __init__(self) -> None:
        self.data = bytearray()
        self.closed = False

    def write(self, data: bytes) -> None:
        self.data.extend(data)

    async def drain(self) -> None:
        await asyncio.sleep(0)

    def close(self) -> None:
        self.closed = True

    async def wait_closed(self) -> None:
        await asyncio.sleep(0)


class FramingTests(unittest.IsolatedAsyncioTestCase):
    def make_stream(self, data: bytes, *, limit: int = 1024) -> FramedJSONStream:
        reader = asyncio.StreamReader()
        reader.feed_data(data)
        reader.feed_eof()
        return FramedJSONStream(reader, _MemoryWriter(), max_frame_bytes=limit)  # type: ignore[arg-type]

    async def test_length_prefixed_round_trip(self) -> None:
        frame = encode_frame({"kind": "health", "ok": True})
        self.assertEqual(struct.unpack(">I", frame[:4])[0], len(frame) - 4)
        self.assertEqual(await self.make_stream(frame).read(), {"kind": "health", "ok": True})

    async def test_rejects_oversized_declared_frame_before_body(self) -> None:
        stream = self.make_stream(struct.pack(">I", 1025), limit=1024)
        with self.assertRaisesRegex(FrameTooLargeError, "configured limit is 1024"):
            await stream.read()

    async def test_rejects_zero_truncated_and_malformed_frames(self) -> None:
        with self.assertRaisesRegex(MalformedFrameError, "zero-length"):
            await self.make_stream(struct.pack(">I", 0)).read()
        with self.assertRaisesRegex(MalformedFrameError, "truncated frame body"):
            await self.make_stream(struct.pack(">I", 8) + b"{}").read()
        body = b"{bad"
        with self.assertRaisesRegex(MalformedFrameError, "malformed JSON"):
            await self.make_stream(struct.pack(">I", len(body)) + body).read()

    async def test_write_is_bounded(self) -> None:
        reader = asyncio.StreamReader()
        writer = _MemoryWriter()
        stream = FramedJSONStream(reader, writer, max_frame_bytes=256)  # type: ignore[arg-type]
        with self.assertRaises(FrameTooLargeError):
            await stream.write({"text": "x" * 250})
        self.assertEqual(writer.data, b"")

    def test_encoder_rejects_non_json_values(self) -> None:
        with self.assertRaises(ProtocolError):
            encode_frame({"bad": object()})
        data: dict[str, Any] = json.loads(encode_frame({"ok": 1})[4:])
        self.assertEqual(data, {"ok": 1})

    async def test_unix_connector_and_portable_transport_selection(self) -> None:
        reader = asyncio.StreamReader()
        writer = _MemoryWriter()
        with patch(
            "serenox_sidecar.transport.asyncio.open_unix_connection",
            new=AsyncMock(return_value=(reader, writer)),
        ) as dial:
            connection = await UnixSocketConnector().connect("unix:///tmp/plugin.sock")
        dial.assert_awaited_once_with("/tmp/plugin.sock")
        self.assertIs(connection.reader, reader)
        self.assertIsInstance(connector_for("/tmp/plugin.sock"), UnixSocketConnector)
        self.assertIsInstance(connector_for("tcp://127.0.0.1:9999"), TCPConnector)


if __name__ == "__main__":
    unittest.main()
