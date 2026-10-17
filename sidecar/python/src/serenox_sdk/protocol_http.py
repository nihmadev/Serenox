"""Internal protocol component: HTTP request and response DTOs."""

from __future__ import annotations

import base64
import dataclasses
import json
from collections.abc import Mapping
from typing import Any

from .errors import ProtocolError
from .protocol_constants import JSONValue
from .protocol_wire import WireDTO


@dataclasses.dataclass(frozen=True, slots=True)
class HTTPHeaderV1(WireDTO):
    name: str
    value: str


@dataclasses.dataclass(frozen=True, slots=True)
class HTTPRequestV1(WireDTO):
    method: str
    url: str
    headers: tuple[HTTPHeaderV1, ...] = ()
    body: bytes = b""
    max_response_bytes: int = 0
    timeout_ms: int = 0

    def to_dict(self) -> dict[str, JSONValue]:
        result: dict[str, JSONValue] = {"method": self.method, "url": self.url}
        if self.headers:
            result["headers"] = [header.to_dict() for header in self.headers]
        if self.body:
            result["body"] = base64.b64encode(self.body).decode("ascii")
        if self.max_response_bytes:
            result["max_response_bytes"] = self.max_response_bytes
        if self.timeout_ms:
            result["timeout_ms"] = self.timeout_ms
        return result

    @classmethod
    def from_dict(cls, value: Mapping[str, Any]) -> HTTPRequestV1:
        method = value.get("method")
        url = value.get("url")
        if not isinstance(method, str) or not isinstance(url, str):
            raise ProtocolError("HTTP request method and URL must be strings")
        raw_headers = value.get("headers", [])
        if not isinstance(raw_headers, list) or len(raw_headers) > 64:
            raise ProtocolError("HTTP headers exceed 64 entries")
        headers = tuple(
            HTTPHeaderV1(str(item.get("name", "")), str(item.get("value", "")))
            for item in raw_headers
            if isinstance(item, Mapping)
        )
        raw_body = value.get("body", "")
        if not isinstance(raw_body, str):
            raise ProtocolError("HTTP body must use base64 wire encoding")
        try:
            body = base64.b64decode(raw_body, validate=True) if raw_body else b""
        except ValueError as exc:
            raise ProtocolError("HTTP body is not valid base64") from exc
        if len(body) > 512 << 10:
            raise ProtocolError("HTTP request body exceeds 512 KiB")
        return cls(
            method,
            url,
            headers,
            body,
            int(value.get("max_response_bytes", 0)),
            int(value.get("timeout_ms", 0)),
        )


@dataclasses.dataclass(frozen=True, slots=True)
class HTTPResponseV1(WireDTO):
    status: int
    headers: tuple[HTTPHeaderV1, ...]
    final_url: str
    body: bytes
    redirects: int = 0
    elapsed_ms: int = 0
    truncated: bool = False
    rejected: bool = False
    content_type: str = ""

    def to_dict(self) -> dict[str, JSONValue]:
        result: dict[str, JSONValue] = {
            "status": self.status,
            "final_url": self.final_url,
            "elapsed_ms": self.elapsed_ms,
        }
        if self.headers:
            result["headers"] = [header.to_dict() for header in self.headers]
        if self.body:
            result["body"] = base64.b64encode(self.body).decode("ascii")
        if self.redirects:
            result["redirects"] = self.redirects
        if self.truncated:
            result["truncated"] = True
        if self.rejected:
            result["rejected"] = True
        if self.content_type:
            result["content_type"] = self.content_type
        return result

    def json(self, *, max_bytes: int) -> Any:
        if max_bytes <= 0 or len(self.body) > max_bytes or self.truncated or self.rejected:
            raise ValueError("HTTP JSON response exceeds the caller limit")
        try:
            return json.loads(self.body.decode("utf-8"))
        except (UnicodeDecodeError, json.JSONDecodeError) as exc:
            raise ValueError("HTTP response is not valid JSON") from exc

    @classmethod
    def from_dict(cls, value: Mapping[str, Any]) -> HTTPResponseV1:
        raw_body = value.get("body", "")
        if not isinstance(raw_body, str):
            raise ProtocolError("HTTP response body must use base64 wire encoding")
        try:
            body = base64.b64decode(raw_body, validate=True) if raw_body else b""
        except ValueError as exc:
            raise ProtocolError("HTTP response body is not valid base64") from exc
        raw_headers = value.get("headers", [])
        if not isinstance(raw_headers, list) or len(raw_headers) > 64:
            raise ProtocolError("HTTP response headers exceed 64 entries")
        headers = tuple(
            HTTPHeaderV1(str(item.get("name", "")), str(item.get("value", "")))
            for item in raw_headers
            if isinstance(item, Mapping)
        )
        return cls(
            int(value.get("status", 0)),
            headers,
            str(value.get("final_url", "")),
            body,
            int(value.get("redirects", 0)),
            int(value.get("elapsed_ms", 0)),
            bool(value.get("truncated", False)),
            bool(value.get("rejected", False)),
            str(value.get("content_type", "")),
        )
