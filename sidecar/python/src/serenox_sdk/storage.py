"""Namespaced, JSON-typed storage proxy with a bounded local compatibility cache."""

from __future__ import annotations

import copy
import json
from collections.abc import Mapping
from typing import Any

from .actions import ActionSink
from .errors import OverloadedError
from .protocol import JSONValue, OperationKind, OperationV1, ensure_json_value


class DatabaseProxy:
    """Legacy synchronous cache plus explicit asynchronous durable operations."""

    def __init__(
        self,
        sink: ActionSink,
        snapshot: Mapping[str, Any] | None = None,
        *,
        max_entries: int = 1_024,
        max_value_bytes: int = 256 * 1_024,
    ) -> None:
        if max_entries < 1 or max_value_bytes < 1:
            raise ValueError("storage bounds must be positive")
        self._sink = sink
        self._max_entries = max_entries
        self._max_value_bytes = max_value_bytes
        self._values: dict[str, dict[str, JSONValue]] = {}
        self.replace_snapshot(snapshot or {})

    def replace_snapshot(self, snapshot: Mapping[str, Any]) -> None:
        values: dict[str, dict[str, JSONValue]] = {}
        entries = 0
        for namespace, raw_items in snapshot.items():
            if not isinstance(namespace, str) or not isinstance(raw_items, Mapping):
                raise TypeError("storage snapshot must contain namespace objects")
            items: dict[str, JSONValue] = {}
            for key, value in raw_items.items():
                if not isinstance(key, str):
                    raise TypeError("storage keys must be strings")
                checked = self._check_value(value)
                items[key] = checked
                entries += 1
                if entries > self._max_entries:
                    raise OverloadedError(
                        f"storage snapshot exceeds configured {self._max_entries}-entry limit"
                    )
            values[namespace] = items
        self._values = values

    def get(self, namespace: str, key: str, default: Any = None) -> Any:
        return copy.deepcopy(self._values.get(namespace, {}).get(key, default))

    def set(self, namespace: str, key: str, value: Any) -> bool:
        checked = self._check_value(value)
        bucket = self._values.setdefault(namespace, {})
        is_new = key not in bucket
        if is_new and self.entry_count >= self._max_entries:
            raise OverloadedError(
                f"storage cache reached configured {self._max_entries}-entry limit"
            )
        previous = bucket.get(key)
        bucket[key] = checked
        try:
            self._sink.notify(
                OperationV1(
                    kind=OperationKind.STORAGE_SET,
                    target={"namespace": namespace, "key": key},
                    parameters={"value": checked},
                )
            )
        except Exception:
            if is_new:
                del bucket[key]
            else:
                bucket[key] = previous
            raise
        return True

    def delete(self, namespace: str, key: str) -> bool:
        bucket = self._values.get(namespace)
        existed = bucket is not None and key in bucket
        previous = bucket.get(key) if bucket is not None else None
        if existed and bucket is not None:
            del bucket[key]
        try:
            self._sink.notify(
                OperationV1(
                    kind=OperationKind.STORAGE_DELETE,
                    target={"namespace": namespace, "key": key},
                )
            )
        except Exception:
            if existed and bucket is not None:
                bucket[key] = previous
            raise
        return existed

    async def async_get(self, namespace: str, key: str, default: Any = None) -> Any:
        value = await self._sink.request(
            OperationV1(
                kind=OperationKind.STORAGE_GET,
                target={"namespace": namespace, "key": key},
            )
        )
        if value is None:
            return copy.deepcopy(default)
        checked = self._check_value(value)
        bucket = self._values.setdefault(namespace, {})
        if key not in bucket and self.entry_count >= self._max_entries:
            raise OverloadedError("storage response would exceed local cache bound")
        bucket[key] = checked
        return copy.deepcopy(checked)

    async def async_set(self, namespace: str, key: str, value: Any) -> bool:
        checked = self._check_value(value)
        result = await self._sink.request(
            OperationV1(
                kind=OperationKind.STORAGE_SET,
                target={"namespace": namespace, "key": key},
                parameters={"value": checked},
            )
        )
        bucket = self._values.setdefault(namespace, {})
        if key not in bucket and self.entry_count >= self._max_entries:
            raise OverloadedError("storage result would exceed local cache bound")
        bucket[key] = checked
        return result is not False

    @property
    def entry_count(self) -> int:
        return sum(len(bucket) for bucket in self._values.values())

    def snapshot(self) -> dict[str, dict[str, JSONValue]]:
        return copy.deepcopy(self._values)

    def _check_value(self, value: Any) -> JSONValue:
        checked = ensure_json_value(value, field="storage.value")
        encoded = json.dumps(checked, ensure_ascii=False, separators=(",", ":")).encode()
        if len(encoded) > self._max_value_bytes:
            raise OverloadedError(
                f"storage value exceeds configured {self._max_value_bytes}-byte limit"
            )
        return checked


class ModuleStorage:
    """A namespace-bound view used by Module.get/set."""

    def __init__(self, database: DatabaseProxy, namespace: str) -> None:
        self._database = database
        self._namespace = namespace

    def get(self, key: str, default: Any = None) -> Any:
        return self._database.get(self._namespace, key, default)

    def set(self, key: str, value: Any) -> bool:
        return self._database.set(self._namespace, key, value)

    def delete(self, key: str) -> bool:
        return self._database.delete(self._namespace, key)

    async def async_get(self, key: str, default: Any = None) -> Any:
        return await self._database.async_get(self._namespace, key, default)

    async def async_set(self, key: str, value: Any) -> bool:
        return await self._database.async_set(self._namespace, key, value)


__all__ = ["DatabaseProxy", "ModuleStorage"]
