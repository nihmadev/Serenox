"""Internal protocol component: compatibility SDK operation DTOs."""

from __future__ import annotations

import dataclasses
from collections.abc import Mapping

from .protocol_constants import JSONValue, OperationKind


@dataclasses.dataclass(frozen=True)
class OperationV1:
    """Sidecar-local operation emitted by compatibility proxies."""

    kind: OperationKind
    target: Mapping[str, JSONValue]
    parameters: Mapping[str, JSONValue] = dataclasses.field(default_factory=dict)
