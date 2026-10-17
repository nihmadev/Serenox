"""Compatibility facade for the modular Serenox sidecar host.

The implementation is split by responsibility across ``host_*`` modules. This
module keeps the historical import path stable for the CLI and integrations.
"""

from __future__ import annotations

import asyncio
import time
from collections.abc import Callable, Sequence
from concurrent.futures import ThreadPoolExecutor
from typing import Any

from serenox_sdk.actions import CallbackRegistry
from serenox_sdk.module import Module, ModuleRegistry, RuntimeContext
from serenox_sdk.storage import DatabaseProxy

from .host_actions import ActionCollector, HostActionSink, active_collector
from .host_lifecycle import HostLifecycleMixin
from .host_loader import load_plugin_module
from .host_models import (
    AuthenticationError,
    HostSettings,
    HostState,
    IncompatibleProtocolError,
    InvalidResponseError,
    InvalidStateError,
    ResponseItem,
    WorkItem,
)
from .host_protocol import HostProtocolMixin
from .host_transport import HostTransportMixin
from .host_utils import (
    compact_wire_action,
    has_response_identity,
    int_list,
    matches_filters,
    nullable_int,
    object_value,
    optional_int,
    optional_string,
    required_int,
    required_string,
    truncate_utf8,
    validate_wire_action,
    wire_buttons,
    wire_inline_results,
    wire_watcher_filter,
)


class SidecarHost(HostTransportMixin, HostProtocolMixin, HostLifecycleMixin):
    """One-module host with fixed queues, workers, deadlines, and clean ownership."""

    def __init__(
        self,
        module: Module,
        *,
        auth_token: str,
        instance_id: str,
        settings: HostSettings | None = None,
        allowed_capabilities: Sequence[str] | None = None,
        clock: Callable[[], float] = time.monotonic,
        wall_clock: Callable[[], float] = time.time,
    ) -> None:
        if not auth_token:
            raise ValueError("auth_token must not be empty")
        if not instance_id:
            raise ValueError("instance_id must not be empty")
        self.settings = settings or HostSettings()
        self.auth_token = auth_token
        self.instance_id = instance_id
        self.clock = clock
        self.wall_clock = wall_clock
        self.state = HostState.LOADED
        self.started_at = clock()
        self._sink = HostActionSink(self.settings.max_actions)
        self._callbacks = CallbackRegistry(self.settings.callback_capacity)
        self._database = DatabaseProxy(
            self._sink,
            max_entries=self.settings.storage_entries,
            max_value_bytes=self.settings.storage_value_bytes,
        )
        self._http_stream = None
        module._bind(
            RuntimeContext(
                sink=self._sink,
                database=self._database,
                callbacks=self._callbacks,
                clock=clock,
                http_request=self._request_host_http,
                capabilities=frozenset(allowed_capabilities or ()),
            )
        )
        self.module = module
        self.registry = ModuleRegistry(
            module, instance_id=instance_id, allowed_capabilities=allowed_capabilities
        )
        self._requests: asyncio.Queue[WorkItem] = asyncio.Queue(
            maxsize=self.settings.queue_capacity
        )
        self._responses: asyncio.Queue[ResponseItem | None] = asyncio.Queue(
            maxsize=self.settings.response_capacity
        )
        self._state_lock = asyncio.Lock()
        self._shutdown = asyncio.Event()
        self._executor = ThreadPoolExecutor(
            max_workers=self.settings.worker_count,
            thread_name_prefix="serenox-python-plugin",
        )
        self._metrics: dict[str, int] = {
            "accepted": 0,
            "completed": 0,
            "overloaded": 0,
            "failed": 0,
            "timed_out": 0,
            "malformed": 0,
            "request_count": 0,
            "request_latency_ms": 0,
            "rejected": 0,
            "storage_commit_failure": 0,
            "client_failure": 0,
            "unknown_response": 0,
        }
        self._tasks: set[asyncio.Task[Any]] = set()
        self._negotiated_frame_bytes = self.settings.max_frame_bytes
        self._next_host_request_id = 1_000_000_000
        self._streaming_request_ids: set[int] = set()
        self._pending_host_requests: dict[int, asyncio.Future[dict[str, Any]]] = {}
        self._host_request_sem = asyncio.Semaphore(self.settings.reverse_rpc_capacity)
        self._host_request_id = 2_000_000_000
        self._host_request_lock = asyncio.Lock()
        self._streaming_actions_enabled = False
        self._duplex_enabled = False
        self._unloaded = False


# Keep private names importable for extensions that used the old single-file
# implementation, while the canonical implementations live in focused modules.
_ActionCollector = ActionCollector
_active_collector = active_collector
_WorkItem = WorkItem
_ResponseItem = ResponseItem
_AuthenticationError = AuthenticationError
_IncompatibleProtocolError = IncompatibleProtocolError
_InvalidResponseError = InvalidResponseError
_InvalidStateError = InvalidStateError
_wire_watcher_filter = wire_watcher_filter
_matches_filters = matches_filters
_wire_buttons = wire_buttons
_wire_inline_results = wire_inline_results
_object = object_value
_required_string = required_string
_optional_string = optional_string
_required_int = required_int
_optional_int = optional_int
_nullable_int = nullable_int
_int_list = int_list
_validate_wire_action = validate_wire_action
_compact_wire_action = compact_wire_action
_has_response_identity = has_response_identity
_truncate_utf8 = truncate_utf8


__all__ = [
    "HostActionSink",
    "HostSettings",
    "HostState",
    "SidecarHost",
    "load_plugin_module",
]
