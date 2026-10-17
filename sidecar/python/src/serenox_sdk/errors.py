"""Stable SDK exceptions with machine-readable error codes."""

from __future__ import annotations

from collections.abc import Mapping
from typing import Any


class SerenoxError(Exception):
    """Base class for errors intentionally exposed to plugin authors."""

    code = "sdk_error"

    def __init__(self, message: str, *, details: Mapping[str, Any] | None = None) -> None:
        super().__init__(message)
        self.details = dict(details or {})


class UnsupportedAPIError(SerenoxError):
    """A legacy API has no process-safe compatibility implementation."""

    code = "unsupported_api"

    def __init__(self, api: str, replacement: str | None = None) -> None:
        message = f"Legacy API {api!r} is not supported by Serenox protocol v1."
        if replacement:
            message += f" Use {replacement} instead."
        super().__init__(message, details={"api": api, "replacement": replacement})
        self.api = api
        self.replacement = replacement


class ProtocolError(SerenoxError):
    """The peer sent a frame or envelope that violates protocol v1."""

    code = "protocol_error"


class ConnectionClosedError(ProtocolError):
    """The core connection closed before an operation completed."""

    code = "connection_closed"


class OverloadedError(SerenoxError):
    """A configured bounded queue or in-flight limit was reached."""

    code = "overloaded"


class ActionError(SerenoxError):
    """The core rejected a requested Telegram, inline, or storage action."""

    code = "action_failed"

    def __init__(
        self,
        message: str,
        *,
        action_code: str = "action_failed",
        retryable: bool = False,
        operation: str = "",
        flood_wait_seconds: int = 0,
        details: Mapping[str, Any] | None = None,
    ) -> None:
        super().__init__(message, details=details)
        self.action_code = action_code
        self.retryable = retryable
        self.operation = operation
        self.flood_wait_seconds = flood_wait_seconds
        self.outcome_unknown = action_code == "outcome_unknown"


class ConfigurationError(SerenoxError):
    """A configured value failed validation."""

    code = "invalid_configuration"
