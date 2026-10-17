"""Internal protocol component: RPC error and request/response envelope DTOs."""

from __future__ import annotations

import dataclasses
import enum
import json
from collections.abc import Mapping
from typing import Any

from .errors import ProtocolError
from .protocol_constants import (
    MAX_COLLECTION_ITEMS,
    PROTOCOL_NAME,
    EnvelopeKind,
    JSONValue,
    Method,
)
from .protocol_validation import (
    _check_string,
    _optional_bool,
    _optional_str,
    _require_int,
    _require_object,
    _require_str,
    ensure_json_value,
)
from .protocol_wire import WireDTO


@dataclasses.dataclass(frozen=True, slots=True)
class RPCErrorV1(WireDTO):
    code: str
    message: str
    retryable: bool = False
    field: str = ""
    details: Mapping[str, str] = dataclasses.field(default_factory=dict)
    operation: str = ""
    flood_wait_seconds: int = 0

    def validate(self) -> None:
        _check_string(self.code, "error.code", required=True)
        _check_string(self.message, "error.message", required=True)
        _check_string(self.field, "error.field")
        _check_string(self.operation, "error.operation")
        if self.flood_wait_seconds < 0:
            raise ProtocolError("error.flood_wait_seconds must not be negative")
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
            operation=_optional_str(value.get("operation"), "", "error.operation"),
            flood_wait_seconds=_require_int(
                value.get("flood_wait_seconds"), "error.flood_wait_seconds"
            )
            if value.get("flood_wait_seconds") is not None
            else 0,
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
