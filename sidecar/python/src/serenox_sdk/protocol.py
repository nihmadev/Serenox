"""Exact Python representations of ``pkg/pluginproto/v1`` wire DTOs.

Only JSON-compatible values cross the process boundary. The compatibility
SDK's richer operations are represented separately by :class:`OperationV1`
and are lowered to the stable Go ``Action`` DTO by the sidecar host.
"""

from __future__ import annotations

import dataclasses
import enum
import json
from collections.abc import Mapping, Sequence
from typing import Any, TypeAlias, Union

from .errors import ProtocolError

PROTOCOL_NAME = "serenox.plugin/1"
PROTOCOL_VERSION = "1.0"
SDK_VERSION = "0.1.0"
DEFAULT_MAX_FRAME_BYTES = 1 << 20
MAX_STRING_BYTES = 64 << 10
MAX_COLLECTION_ITEMS = 1_024
MAX_HANDLERS = 256
MAX_ACTIONS = 128

JSONValue: TypeAlias = Union[  # noqa: UP007 - runtime-safe recursive alias on Python 3.10
    bool, int, float, str, list["JSONValue"], dict[str, "JSONValue"], None
]


class EnvelopeKind(str, enum.Enum):
    REQUEST = "request"
    RESPONSE = "response"


class Method(str, enum.Enum):
    HANDSHAKE = "handshake"
    CONFIGURE = "lifecycle.configure"
    START = "lifecycle.start"
    STOP = "lifecycle.stop"
    HEALTH = "health"
    DISPATCH = "dispatch"
    HOST_ACTIONS = "host.actions"
    HOST_STORAGE = "host.storage"
    HOST_CLIENT = "host.client"


class Capability(str, enum.Enum):
    COMMANDS = "commands"
    WATCHERS = "watchers"
    INLINE = "inline"
    STORAGE = "storage"
    CLIENT = "client_operations"


class UpdateKind(str, enum.Enum):
    MESSAGE = "message"
    EDITED_MESSAGE = "edited_message"
    DELETED = "deleted_message"
    CALLBACK = "callback"
    INLINE_QUERY = "inline_query"


class ActionKind(str, enum.Enum):
    """The exact action-kind values declared by the Go v1 protocol."""

    REPLY = "reply"
    EDIT = "edit"
    DELETE = "delete"
    SEND = "send"
    INLINE_ANSWER = "inline_answer"
    CLIENT = "client_operation"


class ConfigType(str, enum.Enum):
    STRING = "string"
    INTEGER = "integer"
    NUMBER = "number"
    BOOLEAN = "boolean"
    STRINGS = "strings"
    JSON = "json"


class ErrorCode(str, enum.Enum):
    INVALID_REQUEST = "invalid_request"
    INVALID_RESPONSE = "invalid_response"
    INCOMPATIBLE_PROTOCOL = "incompatible_protocol"
    AUTHENTICATION = "authentication_failed"
    INVALID_STATE = "invalid_state"
    UNSUPPORTED = "unsupported"
    OVERLOADED = "overloaded"
    DEADLINE_EXCEEDED = "deadline_exceeded"
    PLUGIN = "plugin_error"
    INTERNAL = "internal"


class LifecyclePhase(str, enum.Enum):
    CONFIGURE = "configure"
    START = "start"
    STOP = "stop"


class StorageOperation(str, enum.Enum):
    GET = "get"
    SET = "set"
    DELETE = "delete"


class HandlerKind(str, enum.Enum):
    """Sidecar-local handler categories; these never cross the v1 wire."""

    COMMAND = "command"
    WATCHER = "watcher"
    INLINE = "inline"
    CALLBACK = "callback"


class OperationKind(str, enum.Enum):
    """SDK operation surface lowered to one or more exact wire actions."""

    REPLY = "message.reply"
    EDIT = "message.edit"
    DELETE = "message.delete"
    SEND = "client.send_message"
    GET_MESSAGE = "client.get_message"
    GET_ME = "client.get_me"
    RESOLVE_PEER = "client.resolve_peer"
    JOIN_CHAT = "client.join_chat"
    LEAVE_CHAT = "client.leave_chat"
    REACT = "client.react"
    INLINE_ANSWER = "inline.answer"
    INLINE_FORM = "inline.form"
    INLINE_EDIT = "inline.edit"
    INLINE_DELETE = "inline.delete"
    STORAGE_GET = "storage.get"
    STORAGE_SET = "storage.set"
    STORAGE_DELETE = "storage.delete"
    INVOKE_COMMAND = "command.invoke"


def ensure_json_value(value: Any, *, field: str = "value") -> JSONValue:
    """Validate and copy a value into the finite JSON type used by the wire."""

    if value is None or isinstance(value, (bool, str)):
        if isinstance(value, str):
            _check_string(value, field)
        return value
    if isinstance(value, int):
        return value
    if isinstance(value, float):
        if value != value or value in (float("inf"), float("-inf")):
            raise ProtocolError(f"{field} must not contain NaN or infinity")
        return value
    if isinstance(value, (list, tuple)):
        if len(value) > MAX_COLLECTION_ITEMS:
            raise ProtocolError(
                f"{field} contains more than {MAX_COLLECTION_ITEMS} collection items"
            )
        return [ensure_json_value(item, field=f"{field}[]") for item in value]
    if isinstance(value, Mapping):
        if len(value) > MAX_COLLECTION_ITEMS:
            raise ProtocolError(f"{field} contains more than {MAX_COLLECTION_ITEMS} object members")
        result: dict[str, JSONValue] = {}
        for key, item in value.items():
            if not isinstance(key, str):
                raise ProtocolError(f"{field} object keys must be strings")
            _check_string(key, f"{field} key", required=True)
            result[key] = ensure_json_value(item, field=f"{field}.{key}")
        return result
    raise ProtocolError(f"{field} contains unsupported value type {type(value).__name__}")


def _wire(value: Any) -> JSONValue:
    if dataclasses.is_dataclass(value) and not isinstance(value, type):
        result: dict[str, JSONValue] = {}
        omit_empty = _OMIT_EMPTY_FIELDS.get(type(value).__name__, frozenset())
        for field in dataclasses.fields(value):
            item = getattr(value, field.name)
            if field.name in omit_empty and _is_empty(item):
                continue
            result[field.name] = _wire(item)
        return result
    if isinstance(value, enum.Enum):
        return ensure_json_value(value.value)
    if isinstance(value, Mapping):
        return ensure_json_value({str(key): _wire(item) for key, item in value.items()})
    if isinstance(value, (list, tuple)):
        return ensure_json_value([_wire(item) for item in value])
    return ensure_json_value(value)


def _is_empty(value: Any) -> bool:
    return (
        value is None
        or value is False
        or value == 0
        or value == ""
        or value == ()
        or value == []
        or value == {}
    )


_OMIT_EMPTY_FIELDS: dict[str, frozenset[str]] = {
    "RPCErrorV1": frozenset({"retryable", "field", "details"}),
    "ResponseV1": frozenset({"result", "error"}),
    "EnvelopeV1": frozenset({"method", "result", "error"}),
    "CommandSpecV1": frozenset({"aliases", "description", "owner_only"}),
    "WatcherFilterV1": frozenset(
        {"kinds", "chat_ids", "sender_ids", "incoming", "outgoing", "text_prefix"}
    ),
    "WatcherSpecV1": frozenset({"description"}),
    "ConfigSpecV1": frozenset({"description", "required", "secret", "default"}),
    "ConfigValueV1": frozenset({"secret"}),
    "PluginMetadataV1": frozenset(
        {"description", "author", "capabilities", "commands", "watchers", "config"}
    ),
    "EntityV1": frozenset({"url", "user_id", "language"}),
    "MediaV1": frozenset({"file_id", "file_name", "mime_type", "size"}),
    "MessageV1": frozenset(
        {
            "sender_id",
            "edit_date_unix_ms",
            "text",
            "outgoing",
            "mentioned",
            "reply_to_message_id",
            "entities",
            "chat_kind",
            "via_bot",
            "forwarded",
            "media",
        }
    ),
    "CallbackV1": frozenset({"chat_id", "message_id", "data", "message"}),
    "InlineQueryV1": frozenset({"query", "offset"}),
    "UpdateV1": frozenset({"message", "deleted_ids", "callback", "inline_query"}),
    "CommandV1": frozenset({"raw_args", "args"}),
    "InlineButtonV1": frozenset({"callback_data", "url", "switch_query"}),
    "InlineResultV1": frozenset({"title", "description", "text", "buttons"}),
    "ClientOperationV1": frozenset({"peer_id", "message_id", "user_id", "value"}),
    "ActionV1": frozenset(
        {
            "chat_id",
            "message_id",
            "reply_to_message_id",
            "text",
            "parse_mode",
            "callback_id",
            "inline_query_id",
            "buttons",
            "inline_results",
            "client_operation",
        }
    ),
    "ActionResultV1": frozenset({"message", "error"}),
    "HandshakeRequestV1": frozenset({"host_version"}),
    "LifecycleRequestV1": frozenset({"config"}),
    "LifecycleResponseV1": frozenset({"actions"}),
    "DispatchRequestV1": frozenset({"command"}),
    "DispatchResponseV1": frozenset({"actions"}),
    "HealthRequestV1": frozenset({"detailed"}),
    "HealthResponseV1": frozenset({"metrics", "uptime_ms"}),
    "StorageRequestV1": frozenset({"value"}),
    "StorageResultV1": frozenset({"value"}),
    "ClientResultV1": frozenset({"message", "value"}),
}


class WireDTO:
    def to_dict(self) -> dict[str, JSONValue]:
        value = _wire(self)
        if not isinstance(value, dict):
            raise ProtocolError(f"{type(self).__name__} must encode as a JSON object")
        return value


@dataclasses.dataclass(frozen=True, slots=True)
class RPCErrorV1(WireDTO):
    code: str
    message: str
    retryable: bool = False
    field: str = ""
    details: Mapping[str, str] = dataclasses.field(default_factory=dict)

    def validate(self) -> None:
        _check_string(self.code, "error.code", required=True)
        _check_string(self.message, "error.message", required=True)
        _check_string(self.field, "error.field")
        if len(self.details) > MAX_COLLECTION_ITEMS:
            raise ProtocolError("error.details has too many entries")
        for key, value in self.details.items():
            _check_string(key, "error.details key", required=True)
            _check_string(value, "error.details value")

    @classmethod
    def from_dict(cls, value: Mapping[str, Any]) -> RPCErrorV1:
        raw_details = _require_object(value.get("details", {}), field="error.details")
        details: dict[str, str] = {}
        for key, item in raw_details.items():
            if not isinstance(item, str):
                raise ProtocolError(f"error.details.{key} must be a string")
            details[key] = item
        result = cls(
            code=_require_str(value.get("code"), "error.code"),
            message=_require_str(value.get("message"), "error.message"),
            retryable=_optional_bool(value.get("retryable"), False, "error.retryable"),
            field=_optional_str(value.get("field"), "", "error.field"),
            details=details,
        )
        result.validate()
        return result


@dataclasses.dataclass(frozen=True, slots=True)
class RequestV1(WireDTO):
    request_id: int
    deadline_unix_ms: int
    method: Method | str
    payload: Mapping[str, JSONValue] = dataclasses.field(default_factory=dict)
    protocol: str = PROTOCOL_NAME
    kind: EnvelopeKind | str = EnvelopeKind.REQUEST

    def validate(self) -> None:
        _validate_common_envelope(self.protocol, self.request_id, self.kind, self.deadline_unix_ms)
        if _enum_value(self.kind) != EnvelopeKind.REQUEST.value:
            raise ProtocolError("request kind must be 'request'")
        try:
            Method(_enum_value(self.method))
        except ValueError as exc:
            raise ProtocolError(f"unknown request method {_enum_value(self.method)!r}") from exc
        _require_object(self.payload, field="payload")

    @classmethod
    def from_dict(cls, value: Mapping[str, Any]) -> RequestV1:
        result = cls(
            protocol=_require_str(value.get("protocol"), "protocol"),
            request_id=_require_int(value.get("request_id"), "request_id"),
            kind=_require_str(value.get("kind"), "kind"),
            deadline_unix_ms=_require_int(value.get("deadline_unix_ms"), "deadline_unix_ms"),
            method=_require_str(value.get("method"), "method"),
            payload=_require_object(value.get("payload", {}), field="payload"),
        )
        result.validate()
        return result


@dataclasses.dataclass(frozen=True, slots=True)
class ResponseV1(WireDTO):
    request_id: int
    deadline_unix_ms: int
    result: JSONValue = None
    error: RPCErrorV1 | None = None
    protocol: str = PROTOCOL_NAME
    kind: EnvelopeKind | str = EnvelopeKind.RESPONSE

    def validate(self) -> None:
        _validate_common_envelope(self.protocol, self.request_id, self.kind, self.deadline_unix_ms)
        if _enum_value(self.kind) != EnvelopeKind.RESPONSE.value:
            raise ProtocolError("response kind must be 'response'")
        if self.error is not None and self.result is not None:
            raise ProtocolError("response result must be absent when error is present")
        if self.error is not None:
            self.error.validate()
        else:
            ensure_json_value(self.result, field="result")

    @classmethod
    def from_dict(cls, value: Mapping[str, Any]) -> ResponseV1:
        error_value = value.get("error")
        result = cls(
            protocol=_require_str(value.get("protocol"), "protocol"),
            request_id=_require_int(value.get("request_id"), "request_id"),
            kind=_require_str(value.get("kind"), "kind"),
            deadline_unix_ms=_require_int(value.get("deadline_unix_ms"), "deadline_unix_ms"),
            result=ensure_json_value(value.get("result"), field="result"),
            error=(
                RPCErrorV1.from_dict(_require_object(error_value, field="error"))
                if error_value is not None
                else None
            ),
        )
        result.validate()
        return result


@dataclasses.dataclass(frozen=True, slots=True)
class EnvelopeV1(WireDTO):
    """Compatibility facade that strictly parses either exact envelope shape."""

    request_id: int
    kind: str
    deadline_unix_ms: int
    method: str | None = None
    payload: Mapping[str, JSONValue] = dataclasses.field(default_factory=dict)
    result: JSONValue = None
    error: RPCErrorV1 | None = None
    protocol: str = PROTOCOL_NAME

    @classmethod
    def from_dict(cls, value: Mapping[str, Any]) -> EnvelopeV1:
        kind = _require_str(value.get("kind"), "kind")
        if kind == EnvelopeKind.REQUEST.value:
            request = RequestV1.from_dict(value)
            return cls(
                protocol=request.protocol,
                request_id=request.request_id,
                kind=kind,
                deadline_unix_ms=request.deadline_unix_ms,
                method=_enum_value(request.method),
                payload=request.payload,
            )
        if kind == EnvelopeKind.RESPONSE.value:
            response = ResponseV1.from_dict(value)
            return cls(
                protocol=response.protocol,
                request_id=response.request_id,
                kind=kind,
                deadline_unix_ms=response.deadline_unix_ms,
                result=response.result,
                error=response.error,
            )
        raise ProtocolError("kind must be 'request' or 'response'")

    @classmethod
    def from_json(cls, data: bytes) -> EnvelopeV1:
        try:
            value = json.loads(data)
        except (UnicodeDecodeError, json.JSONDecodeError) as exc:
            raise ProtocolError(f"malformed JSON envelope: {exc}") from exc
        return cls.from_dict(_require_object(value, field="envelope"))

    def to_json(self) -> bytes:
        return json.dumps(
            self.to_dict(), ensure_ascii=False, allow_nan=False, separators=(",", ":")
        ).encode("utf-8")


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
    config: Sequence[ConfigSpecV1] = dataclasses.field(default_factory=tuple)


@dataclasses.dataclass(frozen=True, slots=True)
class EntityV1(WireDTO):
    type: str
    offset: int
    length: int
    url: str = ""
    user_id: int = 0
    language: str = ""


@dataclasses.dataclass(frozen=True, slots=True)
class MediaV1(WireDTO):
    kind: str
    file_id: str = ""
    file_name: str = ""
    mime_type: str = ""
    size: int = 0


@dataclasses.dataclass(frozen=True, slots=True)
class MessageV1(WireDTO):
    id: int
    chat_id: int
    sender_id: int | None = None
    date_unix_ms: int = 0
    edit_date_unix_ms: int = 0
    text: str = ""
    outgoing: bool = False
    mentioned: bool = False
    reply_to_message_id: int | None = None
    entities: Sequence[EntityV1] = dataclasses.field(default_factory=tuple)
    chat_kind: str = ""
    via_bot: bool = False
    forwarded: bool = False
    media: MediaV1 | None = None

    @classmethod
    def from_dict(cls, value: Mapping[str, Any]) -> MessageV1:
        raw_entities = value.get("entities", [])
        if not isinstance(raw_entities, list):
            raise ProtocolError("message.entities must be a list")
        entities = tuple(_entity_from_dict(raw, index) for index, raw in enumerate(raw_entities))
        media_value = value.get("media")
        media: MediaV1 | None = None
        if media_value is not None:
            media_item = _require_object(media_value, field="message.media")
            media_size = _optional_int(media_item.get("size"), 0, "message.media.size")
            if media_size < 0:
                raise ProtocolError("message.media.size must not be negative")
            media = MediaV1(
                kind=_require_str(media_item.get("kind"), "message.media.kind"),
                file_id=_optional_str(media_item.get("file_id"), "", "message.media.file_id"),
                file_name=_optional_str(media_item.get("file_name"), "", "message.media.file_name"),
                mime_type=_optional_str(media_item.get("mime_type"), "", "message.media.mime_type"),
                size=media_size,
            )
        return cls(
            id=_require_int(value.get("id"), "message.id"),
            chat_id=_require_int(value.get("chat_id"), "message.chat_id"),
            sender_id=_nullable_int(value.get("sender_id"), "message.sender_id"),
            date_unix_ms=_optional_int(value.get("date_unix_ms"), 0, "message.date_unix_ms"),
            edit_date_unix_ms=_optional_int(
                value.get("edit_date_unix_ms"), 0, "message.edit_date_unix_ms"
            ),
            text=_optional_str(value.get("text"), "", "message.text"),
            outgoing=_optional_bool(value.get("outgoing"), False, "message.outgoing"),
            mentioned=_optional_bool(value.get("mentioned"), False, "message.mentioned"),
            reply_to_message_id=_nullable_int(
                value.get("reply_to_message_id"), "message.reply_to_message_id"
            ),
            entities=entities,
            chat_kind=_optional_str(value.get("chat_kind"), "", "message.chat_kind"),
            via_bot=_optional_bool(value.get("via_bot"), False, "message.via_bot"),
            forwarded=_optional_bool(value.get("forwarded"), False, "message.forwarded"),
            media=media,
        )


@dataclasses.dataclass(frozen=True, slots=True)
class CallbackV1(WireDTO):
    id: str
    sender_id: int
    chat_id: int = 0
    message_id: int = 0
    data: str = ""
    message: MessageV1 | None = None


@dataclasses.dataclass(frozen=True, slots=True)
class InlineQueryV1(WireDTO):
    id: str
    sender_id: int
    query: str = ""
    offset: str = ""


@dataclasses.dataclass(frozen=True, slots=True)
class UpdateV1(WireDTO):
    id: str
    kind: UpdateKind | str
    occurred_unix_ms: int
    message: MessageV1 | None = None
    deleted_ids: Sequence[int] = dataclasses.field(default_factory=tuple)
    callback: CallbackV1 | None = None
    inline_query: InlineQueryV1 | None = None


@dataclasses.dataclass(frozen=True, slots=True)
class CommandV1(WireDTO):
    name: str
    prefix: str
    message: MessageV1
    raw_args: str = ""
    args: Sequence[str] = dataclasses.field(default_factory=tuple)


@dataclasses.dataclass(frozen=True, slots=True)
class InlineButtonV1(WireDTO):
    text: str
    callback_data: str = ""
    url: str = ""
    switch_query: str = ""


@dataclasses.dataclass(frozen=True, slots=True)
class InlineResultV1(WireDTO):
    id: str
    type: str
    title: str = ""
    description: str = ""
    text: str = ""
    buttons: Sequence[Sequence[InlineButtonV1]] = dataclasses.field(default_factory=tuple)


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
    chat_id: int = 0
    message_id: int = 0
    reply_to_message_id: int = 0
    text: str = ""
    parse_mode: str = ""
    callback_id: str = ""
    inline_query_id: str = ""
    buttons: Sequence[Sequence[InlineButtonV1]] = dataclasses.field(default_factory=tuple)
    inline_results: Sequence[InlineResultV1] = dataclasses.field(default_factory=tuple)
    client_operation: ClientOperationV1 | None = None


@dataclasses.dataclass(frozen=True, slots=True)
class ActionResultV1(WireDTO):
    action_id: str
    success: bool
    message: MessageV1 | None = None
    error: RPCErrorV1 | None = None


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
    update: UpdateV1
    command: CommandV1 | None = None


@dataclasses.dataclass(frozen=True, slots=True)
class DispatchResponseV1(WireDTO):
    actions: Sequence[ActionV1] = dataclasses.field(default_factory=tuple)


@dataclasses.dataclass(frozen=True, slots=True)
class HealthRequestV1(WireDTO):
    detailed: bool = False


@dataclasses.dataclass(frozen=True, slots=True)
class HealthResponseV1(WireDTO):
    ready: bool
    status: str
    metrics: Mapping[str, int] = dataclasses.field(default_factory=dict)
    uptime_ms: int = 0


@dataclasses.dataclass(frozen=True, slots=True)
class StorageRequestV1(WireDTO):
    operation: StorageOperation | str
    namespace: str
    key: str
    value: JSONValue = None


@dataclasses.dataclass(frozen=True, slots=True)
class StorageResultV1(WireDTO):
    found: bool
    value: JSONValue = None


@dataclasses.dataclass(frozen=True, slots=True)
class ActionBatchRequestV1(WireDTO):
    actions: Sequence[ActionV1]


@dataclasses.dataclass(frozen=True, slots=True)
class ActionBatchResponseV1(WireDTO):
    results: Sequence[ActionResultV1]


@dataclasses.dataclass(frozen=True, slots=True)
class ClientRequestV1(WireDTO):
    operation: ClientOperationV1


@dataclasses.dataclass(frozen=True, slots=True)
class ClientResultV1(WireDTO):
    message: MessageV1 | None = None
    value: JSONValue = None


@dataclasses.dataclass(frozen=True, slots=True)
class OperationV1:
    """Sidecar-local operation emitted by compatibility proxies."""

    kind: OperationKind
    target: Mapping[str, JSONValue]
    parameters: Mapping[str, JSONValue] = dataclasses.field(default_factory=dict)


def _entity_from_dict(value: Any, index: int) -> EntityV1:
    item = _require_object(value, field=f"message.entities[{index}]")
    return EntityV1(
        type=_require_str(item.get("type"), f"message.entities[{index}].type"),
        offset=_require_int(item.get("offset"), f"message.entities[{index}].offset"),
        length=_require_int(item.get("length"), f"message.entities[{index}].length"),
        url=_optional_str(item.get("url"), "", f"message.entities[{index}].url"),
        user_id=_optional_int(item.get("user_id"), 0, f"message.entities[{index}].user_id"),
        language=_optional_str(item.get("language"), "", f"message.entities[{index}].language"),
    )


def _validate_common_envelope(
    protocol: str, request_id: int, kind: EnvelopeKind | str, deadline_unix_ms: int
) -> None:
    if protocol != PROTOCOL_NAME:
        raise ProtocolError(f"protocol must be {PROTOCOL_NAME!r}")
    if request_id <= 0:
        raise ProtocolError("request_id must be positive")
    if _enum_value(kind) not in {EnvelopeKind.REQUEST.value, EnvelopeKind.RESPONSE.value}:
        raise ProtocolError("kind must be 'request' or 'response'")
    if deadline_unix_ms <= 0:
        raise ProtocolError("deadline_unix_ms must be positive")


def _enum_value(value: enum.Enum | str) -> str:
    return str(value.value) if isinstance(value, enum.Enum) else value


def _check_string(value: str, field: str, *, required: bool = False) -> None:
    if required and not value.strip():
        raise ProtocolError(f"{field} must be a non-empty string")
    if len(value.encode("utf-8")) > MAX_STRING_BYTES:
        raise ProtocolError(f"{field} exceeds {MAX_STRING_BYTES} UTF-8 bytes")


def _require_object(value: Any, *, field: str) -> dict[str, JSONValue]:
    if not isinstance(value, Mapping):
        raise ProtocolError(f"{field} must be a JSON object")
    checked = ensure_json_value(value, field=field)
    if not isinstance(checked, dict):
        raise ProtocolError(f"{field} must be a JSON object")
    return checked


def _require_str(value: Any, field: str) -> str:
    if not isinstance(value, str) or not value:
        raise ProtocolError(f"{field} must be a non-empty string")
    _check_string(value, field, required=True)
    return value


def _optional_str(value: Any, default: str, field: str) -> str:
    if value is None:
        return default
    if not isinstance(value, str):
        raise ProtocolError(f"{field} must be a string")
    _check_string(value, field)
    return value


def _require_int(value: Any, field: str) -> int:
    if isinstance(value, bool) or not isinstance(value, int):
        raise ProtocolError(f"{field} must be an integer")
    return value


def _optional_int(value: Any, default: int, field: str) -> int:
    if value is None:
        return default
    return _require_int(value, field)


def _nullable_int(value: Any, field: str) -> int | None:
    if value is None:
        return None
    return _require_int(value, field)


def _optional_bool(value: Any, default: bool, field: str) -> bool:
    if value is None:
        return default
    if not isinstance(value, bool):
        raise ProtocolError(f"{field} must be a boolean")
    return value


__all__ = [
    "DEFAULT_MAX_FRAME_BYTES",
    "MAX_ACTIONS",
    "MAX_COLLECTION_ITEMS",
    "MAX_HANDLERS",
    "MAX_STRING_BYTES",
    "PROTOCOL_NAME",
    "PROTOCOL_VERSION",
    "SDK_VERSION",
    "ActionBatchRequestV1",
    "ActionBatchResponseV1",
    "ActionKind",
    "ActionResultV1",
    "ActionV1",
    "CallbackV1",
    "Capability",
    "ClientOperationV1",
    "ClientRequestV1",
    "ClientResultV1",
    "CommandSpecV1",
    "CommandV1",
    "ConfigSpecV1",
    "ConfigType",
    "ConfigValueV1",
    "DispatchRequestV1",
    "DispatchResponseV1",
    "EntityV1",
    "EnvelopeKind",
    "EnvelopeV1",
    "ErrorCode",
    "HandlerKind",
    "HandlerSpecV1",
    "HandshakeRequestV1",
    "HandshakeResponseV1",
    "HealthRequestV1",
    "HealthResponseV1",
    "InlineButtonV1",
    "InlineQueryV1",
    "InlineResultV1",
    "JSONValue",
    "LifecyclePhase",
    "LifecycleRequestV1",
    "LifecycleResponseV1",
    "MediaV1",
    "MessageV1",
    "Method",
    "OperationKind",
    "OperationV1",
    "PluginMetadataV1",
    "RPCErrorV1",
    "RequestV1",
    "ResponseV1",
    "StorageOperation",
    "StorageRequestV1",
    "StorageResultV1",
    "UpdateKind",
    "UpdateV1",
    "WatcherFilterV1",
    "WatcherSpecV1",
    "ensure_json_value",
]
