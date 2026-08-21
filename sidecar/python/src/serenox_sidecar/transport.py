"""Portable connector abstraction; Unix sockets are the production default."""

from __future__ import annotations

import asyncio
import dataclasses
from typing import Protocol
from urllib.parse import urlparse


@dataclasses.dataclass(slots=True)
class StreamConnection:
    reader: asyncio.StreamReader
    writer: asyncio.StreamWriter


class TransportConnector(Protocol):
    async def connect(self, address: str) -> StreamConnection:
        """Connect to one daemon-owned local endpoint."""


class UnixSocketConnector:
    async def connect(self, address: str) -> StreamConnection:
        path = address.removeprefix("unix://")
        if not path:
            raise ValueError("Unix socket path is empty")
        reader, writer = await asyncio.open_unix_connection(path)
        return StreamConnection(reader, writer)


class TCPConnector:
    """Portable development fallback for platforms without Unix sockets."""

    async def connect(self, address: str) -> StreamConnection:
        parsed = urlparse(address if "://" in address else f"tcp://{address}")
        if parsed.scheme != "tcp" or parsed.hostname is None or parsed.port is None:
            raise ValueError("TCP address must have the form tcp://host:port")
        reader, writer = await asyncio.open_connection(parsed.hostname, parsed.port)
        return StreamConnection(reader, writer)


def connector_for(address: str) -> TransportConnector:
    if address.startswith("tcp://"):
        return TCPConnector()
    if address.startswith("unix://") or "://" not in address:
        return UnixSocketConnector()
    raise ValueError(f"unsupported sidecar transport in address {address!r}")


__all__ = [
    "StreamConnection",
    "TCPConnector",
    "TransportConnector",
    "UnixSocketConnector",
    "connector_for",
]
