"""Internal protocol component: media DTOs and media operations."""

from __future__ import annotations

import dataclasses
import enum
from collections.abc import Mapping

from .protocol_constants import JSONValue
from .protocol_wire import WireDTO


@dataclasses.dataclass(frozen=True, slots=True)
class MediaV1(WireDTO):
    kind: str
    handle: str = ""
    file_id: str = ""
    file_name: str = ""
    mime_type: str = ""
    size: int = 0
    checksum: str = ""
    source: Mapping[str, JSONValue] | None = None
    attributes: MediaAttributesV1 | None = None


@dataclasses.dataclass(frozen=True, slots=True)
class MediaAttributesV1(WireDTO):
    width: int = 0
    height: int = 0
    duration_seconds: int = 0
    animated: bool = False


@dataclasses.dataclass(frozen=True, slots=True)
class MediaAttachmentV1(WireDTO):
    handle: str
    kind: str
    mime_type: str = ""
    file_name: str = ""
    size: int = 0
    checksum: str = ""
    caption: str = ""
    parse_mode: str = ""
    attributes: MediaAttributesV1 | None = None


class MediaOperation(str, enum.Enum):
    INSPECT = "inspect"
    DOWNLOAD = "download"
    UPLOAD = "upload"
    ATTACH = "attach"
    DELETE = "delete"


@dataclasses.dataclass(frozen=True, slots=True)
class MediaRequestV1(WireDTO):
    operation: MediaOperation | str
    handle: str = ""
    kind: str = ""
    mime_type: str = ""
    file_name: str = ""
    size: int = 0
    checksum: str = ""
    peer_id: int = 0


@dataclasses.dataclass(frozen=True, slots=True)
class MediaResultV1(WireDTO):
    media: MediaV1 | None = None
    transfer_id: str = ""
    chunk_size: int = 0
    max_size: int = 0
    complete: bool = False
