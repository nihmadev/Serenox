"""Internal protocol component: handler discovery, configuration, and plugin metadata DTOs."""

from __future__ import annotations

import dataclasses
from collections.abc import Mapping, Sequence

from .protocol_constants import (
    PROTOCOL_VERSION,
    Capability,
    ConfigType,
    ControlActorPolicy,
    ControlResponseMode,
    ControlSurface,
    HandlerKind,
    JSONValue,
    UpdateKind,
)
from .protocol_wire import WireDTO


@dataclasses.dataclass(frozen=True, slots=True)
class HandlerSpecV1(WireDTO):
    """Sidecar-local immutable handler discovery record."""

    kind: HandlerKind
    name: str
    method: str
    filters: Mapping[str, JSONValue] = dataclasses.field(default_factory=dict)
    aliases: Sequence[str] = dataclasses.field(default_factory=tuple)
    timeout_ms: int = 10_000


@dataclasses.dataclass(frozen=True, slots=True)
class CommandSpecV1(WireDTO):
    name: str
    aliases: Sequence[str] = dataclasses.field(default_factory=tuple)
    description: str = ""
    owner_only: bool = False


@dataclasses.dataclass(frozen=True, slots=True)
class ControlCommandV1(WireDTO):
    name: str
    surfaces: Sequence[ControlSurface | str]
    response_mode: ControlResponseMode | str
    aliases: Sequence[str] = dataclasses.field(default_factory=tuple)
    description: str = ""
    actor_policy: ControlActorPolicy | str = "owner_only"
    allowed_actor_ids: Sequence[int] = dataclasses.field(default_factory=tuple)
    mutating: bool = False
    require_confirmation: bool = False
    confirmation_timeout_ms: int = 0
    allow_query_execution: bool = False
    max_argument_bytes: int = 0
    max_state_bytes: int = 0
    handler_id: str = ""


@dataclasses.dataclass(frozen=True, slots=True)
class WatcherFilterV1(WireDTO):
    kinds: Sequence[UpdateKind | str] = dataclasses.field(default_factory=tuple)
    chat_ids: Sequence[int] = dataclasses.field(default_factory=tuple)
    sender_ids: Sequence[int] = dataclasses.field(default_factory=tuple)
    incoming: bool = False
    outgoing: bool = False
    text_prefix: str = ""


@dataclasses.dataclass(frozen=True, slots=True)
class WatcherSpecV1(WireDTO):
    id: str
    filter: WatcherFilterV1
    description: str = ""


@dataclasses.dataclass(frozen=True, slots=True)
class InlineSpecV1(WireDTO):
    name: str
    description: str = ""
    query_prefix: str = ""
    sender_ids: Sequence[int] = dataclasses.field(default_factory=tuple)
    allowed_users: Sequence[int] = dataclasses.field(default_factory=tuple)
    owner_only: bool = False


@dataclasses.dataclass(frozen=True, slots=True)
class CallbackSpecV1(WireDTO):
    name: str
    description: str = ""
    data: str = ""
    data_prefix: str = ""
    chat_ids: Sequence[int] = dataclasses.field(default_factory=tuple)
    sender_ids: Sequence[int] = dataclasses.field(default_factory=tuple)
    allowed_users: Sequence[int] = dataclasses.field(default_factory=tuple)
    owner_only: bool = False
    ttl_ms: int = 0


@dataclasses.dataclass(frozen=True, slots=True)
class PreSendSpecV1(WireDTO):
    name: str
    description: str = ""


@dataclasses.dataclass(frozen=True, slots=True)
class ConfigSpecV1(WireDTO):
    key: str
    type: ConfigType | str
    description: str = ""
    required: bool = False
    secret: bool = False
    default: JSONValue = None


@dataclasses.dataclass(frozen=True, slots=True)
class ConfigValueV1(WireDTO):
    key: str
    type: ConfigType | str
    value: JSONValue
    secret: bool = False


@dataclasses.dataclass(frozen=True, slots=True)
class PluginMetadataV1(WireDTO):
    id: str
    name: str
    version: str
    protocol_version: str = PROTOCOL_VERSION
    description: str = ""
    author: str = ""
    capabilities: Sequence[Capability | str] = dataclasses.field(default_factory=tuple)
    commands: Sequence[CommandSpecV1] = dataclasses.field(default_factory=tuple)
    watchers: Sequence[WatcherSpecV1] = dataclasses.field(default_factory=tuple)
    inline_handlers: Sequence[InlineSpecV1] = dataclasses.field(default_factory=tuple)
    callback_handlers: Sequence[CallbackSpecV1] = dataclasses.field(default_factory=tuple)
    pre_send_handlers: Sequence[PreSendSpecV1] = dataclasses.field(default_factory=tuple)
    control_commands: Sequence[ControlCommandV1] = dataclasses.field(default_factory=tuple)
    config: Sequence[ConfigSpecV1] = dataclasses.field(default_factory=tuple)
    jobs: Sequence[Mapping[str, JSONValue]] = dataclasses.field(default_factory=tuple)
    event_publications: Sequence[Mapping[str, JSONValue]] = dataclasses.field(default_factory=tuple)
    event_subscriptions: Sequence[Mapping[str, JSONValue]] = dataclasses.field(
        default_factory=tuple
    )
