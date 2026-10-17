"""Internal protocol component: handshake, lifecycle, dispatch, and health DTOs."""

from __future__ import annotations

import dataclasses
from collections.abc import Mapping, Sequence

from .protocol_actions import ActionV1
from .protocol_constants import JSONValue, LifecyclePhase
from .protocol_metadata import ConfigValueV1, PluginMetadataV1
from .protocol_updates import CommandV1, UpdateV1
from .protocol_wire import WireDTO


@dataclasses.dataclass(frozen=True, slots=True)
class HandshakeRequestV1(WireDTO):
    supported_versions: Sequence[str]
    auth_token: str
    instance_id: str
    nonce: str
    max_frame_bytes: int
    host_version: str = ""


@dataclasses.dataclass(frozen=True, slots=True)
class HandshakeResponseV1(WireDTO):
    selected_version: str
    nonce: str
    max_frame_bytes: int
    metadata: PluginMetadataV1


@dataclasses.dataclass(frozen=True, slots=True)
class LifecycleRequestV1(WireDTO):
    phase: LifecyclePhase | str
    config: Sequence[ConfigValueV1] = dataclasses.field(default_factory=tuple)


@dataclasses.dataclass(frozen=True, slots=True)
class LifecycleResponseV1(WireDTO):
    state: str
    actions: Sequence[ActionV1] = dataclasses.field(default_factory=tuple)


@dataclasses.dataclass(frozen=True, slots=True)
class DispatchRequestV1(WireDTO):
    update: UpdateV1 | None = None
    command: CommandV1 | None = None
    pre_send: PreSendRequestV1 | None = None
    job: Mapping[str, JSONValue] | None = None
    event: Mapping[str, JSONValue] | None = None


@dataclasses.dataclass(frozen=True, slots=True)
class DispatchResponseV1(WireDTO):
    actions: Sequence[ActionV1] = dataclasses.field(default_factory=tuple)
    pre_send: PreSendResponseV1 | None = None
    events: Sequence[Mapping[str, JSONValue]] = dataclasses.field(default_factory=tuple)
    event_result: Mapping[str, JSONValue] | None = None


@dataclasses.dataclass(frozen=True, slots=True)
class PreSendRequestV1(WireDTO):
    handler: str
    action: ActionV1


@dataclasses.dataclass(frozen=True, slots=True)
class PreSendResponseV1(WireDTO):
    action: ActionV1


@dataclasses.dataclass(frozen=True, slots=True)
class HealthRequestV1(WireDTO):
    detailed: bool = False


@dataclasses.dataclass(frozen=True, slots=True)
class HealthResponseV1(WireDTO):
    ready: bool
    status: str
    metrics: Mapping[str, int] = dataclasses.field(default_factory=dict)
    uptime_ms: int = 0
