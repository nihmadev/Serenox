"""Internal protocol component: JSON value validation and primitive field checks."""

from __future__ import annotations

from collections.abc import Mapping
from typing import Any

from .errors import ProtocolError
from .protocol_constants import MAX_COLLECTION_ITEMS, MAX_STRING_BYTES, JSONValue


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
