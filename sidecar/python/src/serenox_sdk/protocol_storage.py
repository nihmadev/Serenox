"""Internal protocol component: storage request, mutation, and result DTOs."""

from __future__ import annotations

import dataclasses
import json
from collections.abc import Mapping, Sequence
from typing import Any

from .errors import ProtocolError
from .protocol_constants import (
    MAX_COLLECTION_ITEMS,
    MAX_STORAGE_VALUE_BYTES,
    JSONValue,
    StorageOperation,
)
from .protocol_validation import (
    _optional_bool,
    _optional_str,
    _require_object,
    _require_str,
    ensure_json_value,
)
from .protocol_wire import WireDTO


@dataclasses.dataclass(frozen=True, slots=True)
class StorageRequestV1(WireDTO):
    operation: StorageOperation | str
    namespace: str
    key: str
    value: JSONValue = None
    batch: Sequence[StorageMutationV1] = dataclasses.field(default_factory=tuple)

    @classmethod
    def from_dict(cls, value: Mapping[str, Any]) -> StorageRequestV1:
        operation = _require_str(value.get("operation"), "storage.operation")
        if operation not in {item.value for item in StorageOperation}:
            raise ProtocolError(f"unknown storage operation {operation!r}")
        namespace = _optional_str(value.get("namespace"), "", "storage.namespace")
        key = _optional_str(value.get("key"), "", "storage.key")
        raw_batch = value.get("batch", [])
        if not isinstance(raw_batch, list) or len(raw_batch) > 128:
            raise ProtocolError("storage.batch must contain at most 128 items")
        batch = tuple(
            StorageMutationV1.from_dict(item, index) for index, item in enumerate(raw_batch)
        )
        checked = ensure_json_value(value.get("value"), field="storage.value")
        if (
            len(json.dumps(checked, ensure_ascii=False, separators=(",", ":")).encode("utf-8"))
            > MAX_STORAGE_VALUE_BYTES
        ):
            raise ProtocolError("storage.value exceeds the configured byte limit")
        if operation in {StorageOperation.SET.value} and "value" not in value:
            raise ProtocolError("storage.value is required for set")
        if operation != StorageOperation.BATCH.value and raw_batch:
            raise ProtocolError("storage.batch is only valid for batch")
        if operation == StorageOperation.BATCH.value and not batch:
            raise ProtocolError("storage.batch must not be empty")
        if operation != StorageOperation.BATCH.value and not namespace:
            raise ProtocolError("storage.namespace is required")
        return cls(operation, namespace, key, checked, batch)


@dataclasses.dataclass(frozen=True, slots=True)
class StorageMutationV1(WireDTO):
    operation: StorageOperation | str
    namespace: str
    key: str
    value: JSONValue = None

    @classmethod
    def from_dict(cls, value: Any, index: int = 0) -> StorageMutationV1:
        item = _require_object(value, field=f"storage.batch[{index}]")
        operation = _require_str(item.get("operation"), f"storage.batch[{index}].operation")
        if operation not in {StorageOperation.SET.value, StorageOperation.DELETE.value}:
            raise ProtocolError(f"storage.batch[{index}].operation must be set or delete")
        return cls(
            operation,
            _require_str(item.get("namespace"), f"storage.batch[{index}].namespace"),
            _require_str(item.get("key"), f"storage.batch[{index}].key"),
            ensure_json_value(item.get("value"), field=f"storage.batch[{index}].value"),
        )


@dataclasses.dataclass(frozen=True, slots=True)
class StorageResultV1(WireDTO):
    found: bool
    value: JSONValue = None
    entries: Sequence[Mapping[str, JSONValue]] = dataclasses.field(default_factory=tuple)

    @classmethod
    def from_dict(cls, value: Mapping[str, Any]) -> StorageResultV1:
        found = _optional_bool(value.get("found"), False, "storage.found")
        entries = value.get("entries", [])
        if not isinstance(entries, list) or len(entries) > MAX_COLLECTION_ITEMS:
            raise ProtocolError("storage.entries exceeds the collection limit")
        checked_entries: list[Mapping[str, JSONValue]] = []
        for index, item in enumerate(entries):
            checked = _require_object(item, field=f"storage.entries[{index}]")
            checked_entries.append(checked)
        return cls(
            found,
            ensure_json_value(value.get("value"), field="storage.value"),
            tuple(checked_entries),
        )
