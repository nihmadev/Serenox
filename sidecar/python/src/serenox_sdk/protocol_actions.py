"""Internal protocol component: action and action batch DTOs."""

from __future__ import annotations

import dataclasses
from collections.abc import Mapping, Sequence

from .protocol_constants import ActionKind, ResponseChannel
from .protocol_envelopes import RPCErrorV1
from .protocol_media import MediaAttachmentV1
from .protocol_storage import StorageMutationV1
from .protocol_updates import InlineButtonV1, InlineResultV1, MessageV1
from .protocol_wire import WireDTO


@dataclasses.dataclass(frozen=True, slots=True)
class ClientOperationV1(WireDTO):
    name: str
    peer_id: int = 0
    message_id: int = 0
    user_id: int = 0
    value: str = ""


@dataclasses.dataclass(frozen=True, slots=True)
class ActionV1(WireDTO):
    id: str
    kind: ActionKind | str
    response_channel: ResponseChannel | str = ""
    idempotency_key: str = ""
    chat_id: int = 0
    message_id: int = 0
    reply_to_message_id: int = 0
    text: str = ""
    parse_mode: str = ""
    callback_id: str = ""
    inline_query_id: str = ""
    inline_message_id: str = ""
    callback_text: str = ""
    show_alert: bool = False
    cache_time_seconds: int = 0
    is_personal: bool = False
    next_offset: str = ""
    buttons: Sequence[Sequence[InlineButtonV1]] = dataclasses.field(default_factory=tuple)
    inline_results: Sequence[InlineResultV1] = dataclasses.field(default_factory=tuple)
    client_operation: ClientOperationV1 | None = None
    storage_mutation: StorageMutationV1 | None = None
    media: MediaAttachmentV1 | None = None


@dataclasses.dataclass(frozen=True, slots=True)
class ActionResultV1(WireDTO):
    action_id: str
    operation: str = ""
    status: str = "success"
    success: bool = True
    outcome_unknown: bool = False
    retryable: bool = False
    flood_wait_seconds: int = 0
    message: MessageV1 | None = None
    peer: Mapping[str, object] | None = None
    message_id: int = 0
    date_unix_ms: int = 0
    media_handle: str = ""
    metadata: Mapping[str, str] = dataclasses.field(default_factory=dict)
    error: RPCErrorV1 | None = None


@dataclasses.dataclass(frozen=True, slots=True)
class ActionBatchRequestV1(WireDTO):
    actions: Sequence[ActionV1]
    stop_on_error: bool = False


@dataclasses.dataclass(frozen=True, slots=True)
class ActionBatchResponseV1(WireDTO):
    results: Sequence[ActionResultV1]
    stopped: bool = False
    partial: bool = False
