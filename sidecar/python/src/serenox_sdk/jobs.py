"""Typed, bounded jobs facade backed by the sidecar's host RPC."""

from __future__ import annotations

import dataclasses
from collections.abc import Mapping

from .actions import ActionSink
from .protocol import JSONValue, OperationKind, OperationV1


@dataclasses.dataclass(frozen=True, slots=True)
class JobSpec:
    job_id: str
    handler: str
    schedule: Mapping[str, JSONValue]
    timezone: str = "UTC"
    persistent: bool = True
    deadline_ms: int = 86_400_000
    max_runtime_ms: int = 30_000
    misfire: str = "skip"
    overlap: str = "skip_if_running"
    payload: JSONValue = None
    payload_type: str = "application/json"

    def to_dict(self) -> dict[str, JSONValue]:
        return {
            "identity": {"id": self.job_id},
            "handler": self.handler,
            "schedule": dict(self.schedule),
            "timezone": {"name": self.timezone},
            "persistent": self.persistent,
            "deadline_ms": self.deadline_ms,
            "max_runtime_ms": self.max_runtime_ms,
            "misfire": self.misfire,
            "overlap": self.overlap,
            "payload": self.payload,
            "payload_type": self.payload_type,
        }


class JobsProxy:
    """The only job authority exposed to a Python module."""

    def __init__(self, sink: ActionSink) -> None:
        self._sink = sink

    async def create(self, spec: JobSpec | Mapping[str, JSONValue]) -> dict[str, JSONValue]:
        return await self._call(OperationKind.JOB_CREATE, self._spec(spec))

    async def update(self, request: Mapping[str, JSONValue]) -> dict[str, JSONValue]:
        return await self._call(OperationKind.JOB_UPDATE, request)

    async def cancel(
        self, identity: Mapping[str, JSONValue], revision: int = 0
    ) -> dict[str, JSONValue]:
        return await self._action(OperationKind.JOB_CANCEL, identity, revision)

    async def pause(
        self, identity: Mapping[str, JSONValue], revision: int = 0
    ) -> dict[str, JSONValue]:
        return await self._action(OperationKind.JOB_PAUSE, identity, revision)

    async def resume(
        self, identity: Mapping[str, JSONValue], revision: int = 0
    ) -> dict[str, JSONValue]:
        return await self._action(OperationKind.JOB_RESUME, identity, revision)

    async def trigger(
        self, identity: Mapping[str, JSONValue], revision: int = 0
    ) -> dict[str, JSONValue]:
        return await self._action(OperationKind.JOB_TRIGGER, identity, revision)

    async def get(self, identity: Mapping[str, JSONValue]) -> dict[str, JSONValue]:
        return await self._call(OperationKind.JOB_GET, {"identity": dict(identity)})

    async def list(self, limit: int = 100) -> dict[str, JSONValue]:
        return await self._call(OperationKind.JOB_LIST, {"limit": limit})

    async def inspect(
        self, identity: Mapping[str, JSONValue], include_history: bool = False
    ) -> dict[str, JSONValue]:
        return await self._call(
            OperationKind.JOB_INSPECT,
            {"identity": dict(identity), "include_history": include_history},
        )

    async def clear(
        self, identity: Mapping[str, JSONValue], revision: int = 0
    ) -> dict[str, JSONValue]:
        return await self._call(
            OperationKind.JOB_CLEAR, {"identity": dict(identity), "revision": revision}
        )

    async def delete(
        self, identity: Mapping[str, JSONValue], revision: int = 0
    ) -> dict[str, JSONValue]:
        return await self._call(
            OperationKind.JOB_DELETE, {"identity": dict(identity), "revision": revision}
        )

    async def _action(
        self, operation: OperationKind, identity: Mapping[str, JSONValue], revision: int
    ) -> dict[str, JSONValue]:
        return await self._call(operation, {"identity": dict(identity), "revision": revision})

    async def _call(
        self, operation: OperationKind, payload: Mapping[str, JSONValue]
    ) -> dict[str, JSONValue]:
        value = await self._sink.request(
            OperationV1(kind=operation, target={}, parameters=dict(payload))
        )
        if not isinstance(value, dict):
            raise TypeError("host.jobs response must be an object")
        return value

    @staticmethod
    def _spec(spec: JobSpec | Mapping[str, JSONValue]) -> Mapping[str, JSONValue]:
        if isinstance(spec, JobSpec):
            return {"spec": spec.to_dict()}
        return {"spec": dict(spec)}
