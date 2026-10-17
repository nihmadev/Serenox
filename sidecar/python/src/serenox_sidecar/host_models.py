"""Shared models and internal exceptions for the Python sidecar host."""

from __future__ import annotations

import dataclasses
import enum

from serenox_sdk.errors import ProtocolError
from serenox_sdk.protocol import MAX_ACTIONS, JSONValue

from .framing import DEFAULT_MAX_FRAME_BYTES


class HostState(str, enum.Enum):
    LOADED = "loaded"
    NEGOTIATED = "negotiated"
    CONFIGURED = "configured"
    RUNNING = "running"
    STOPPING = "stopping"
    STOPPED = "stopped"


@dataclasses.dataclass(frozen=True, slots=True)
class HostSettings:
    queue_capacity: int = 64
    worker_count: int = 2
    response_capacity: int = 64
    reverse_rpc_capacity: int = 8
    callback_capacity: int = 256
    max_actions: int = 128
    max_action_bytes: int = 256 * 1_024
    max_frame_bytes: int = DEFAULT_MAX_FRAME_BYTES
    max_handler_timeout_seconds: float = 30.0
    shutdown_timeout_seconds: float = 5.0
    storage_entries: int = 1_024
    storage_value_bytes: int = 256 * 1_024
    media_max_size_bytes: int = 64 * 1_024 * 1_024
    media_chunk_size_bytes: int = 64 * 1_024
    media_max_concurrent: int = 2
    media_quota_bytes: int = 256 * 1_024 * 1_024
    media_transfer_timeout_seconds: float = 300.0
    media_idle_timeout_seconds: float = 30.0

    def __post_init__(self) -> None:
        bounds = {
            "queue_capacity": (self.queue_capacity, 1, 65_536),
            "worker_count": (self.worker_count, 1, 32),
            "response_capacity": (self.response_capacity, 1, 65_536),
            "reverse_rpc_capacity": (self.reverse_rpc_capacity, 1, 1_024),
            "callback_capacity": (self.callback_capacity, 1, 65_536),
            "max_actions": (self.max_actions, 1, MAX_ACTIONS),
            "max_action_bytes": (self.max_action_bytes, 256, 1 << 20),
            "max_frame_bytes": (self.max_frame_bytes, 256, 64 << 20),
            "storage_entries": (self.storage_entries, 1, 1_000_000),
            "storage_value_bytes": (self.storage_value_bytes, 1, 16 << 20),
            "media_max_size_bytes": (self.media_max_size_bytes, 1, 64 << 20),
            "media_chunk_size_bytes": (self.media_chunk_size_bytes, 1, 64 << 10),
            "media_max_concurrent": (self.media_max_concurrent, 1, 64),
            "media_quota_bytes": (self.media_quota_bytes, 1, 256 << 20),
        }
        for name, (value, minimum, maximum) in bounds.items():
            if value < minimum or value > maximum:
                raise ValueError(f"{name} must be between {minimum} and {maximum}")
        if not 0.01 <= self.max_handler_timeout_seconds <= 300:
            raise ValueError("max_handler_timeout_seconds must be between 0.01 and 300")
        if not 0.01 <= self.shutdown_timeout_seconds <= 60:
            raise ValueError("shutdown_timeout_seconds must be between 0.01 and 60")
        if (
            not 0.01 <= self.media_transfer_timeout_seconds <= 300
            or not 0.01 <= self.media_idle_timeout_seconds <= 30
        ):
            raise ValueError("media timeouts are outside configured bounds")
        if self.media_quota_bytes < self.media_max_size_bytes:
            raise ValueError("media_quota_bytes cannot be smaller than media_max_size_bytes")
        if self.max_action_bytes > self.max_frame_bytes:
            raise ValueError("max_action_bytes cannot exceed max_frame_bytes")


@dataclasses.dataclass(slots=True)
class WorkItem:
    request: dict[str, JSONValue]


@dataclasses.dataclass(slots=True)
class ResponseItem:
    response: dict[str, JSONValue]
    close_after: bool = False


class AuthenticationError(ProtocolError):
    pass


class IncompatibleProtocolError(ProtocolError):
    pass


class InvalidStateError(ProtocolError):
    pass


class InvalidResponseError(ProtocolError):
    pass


__all__ = [
    "AuthenticationError",
    "HostSettings",
    "HostState",
    "IncompatibleProtocolError",
    "InvalidResponseError",
    "InvalidStateError",
    "ResponseItem",
    "WorkItem",
]
