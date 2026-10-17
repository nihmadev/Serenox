"""Protocol request processing and response construction."""

from __future__ import annotations

import asyncio
import json
from collections.abc import Mapping
from typing import Any

from serenox_sdk.errors import (
    ActionError,
    ConfigurationError,
    OverloadedError,
    ProtocolError,
    SerenoxError,
    UnsupportedAPIError,
)
from serenox_sdk.protocol import (
    MAX_STRING_BYTES,
    PROTOCOL_NAME,
    JSONValue,
    Method,
    ensure_json_value,
)

from .host_actions import ActionCollector, active_collector
from .host_models import (
    AuthenticationError,
    IncompatibleProtocolError,
    InvalidResponseError,
    InvalidStateError,
)
from .host_utils import object_value, required_int, truncate_utf8


class HostProtocolMixin:
    async def _process_request(
        self: Any, request: Mapping[str, JSONValue]
    ) -> tuple[dict[str, JSONValue], bool]:
        started = self.clock()
        self._metrics["request_count"] += 1
        method = str(request["method"])
        payload = object_value(request.get("payload"), "payload")
        deadline_ms = required_int(request.get("deadline_unix_ms"), "deadline_unix_ms")
        remaining = deadline_ms / 1000 - self.wall_clock()
        if remaining <= 0:
            self._metrics["timed_out"] += 1
            return (
                self._error_response(
                    request,
                    "deadline_exceeded",
                    "request deadline elapsed before Python execution began",
                    retryable=True,
                ),
                False,
            )
        timeout = min(remaining, self.settings.max_handler_timeout_seconds)
        collector = ActionCollector(
            self.settings.max_actions,
            self.settings.max_action_bytes,
            max(256, self._negotiated_frame_bytes - 4_096),
        )
        token = active_collector.set(collector)
        try:
            result = await asyncio.wait_for(self._dispatch_method(method, payload), timeout=timeout)
        except TimeoutError:
            self._metrics["timed_out"] += 1
            return (
                self._error_response(
                    request,
                    "deadline_exceeded",
                    f"Python handler exceeded its {timeout:.3f}-second deadline",
                    retryable=False,
                ),
                False,
            )
        finally:
            active_collector.reset(token)
        if collector.actions and (
            method.startswith("lifecycle.") or method == Method.DISPATCH.value
        ):
            result["actions"] = ensure_json_value(collector.actions, field="actions")
        close_after = method == Method.STOP.value
        self._metrics["request_latency_ms"] += int(max(0.0, self.clock() - started) * 1000)
        return self._success_response(request, result), close_after

    async def _dispatch_method(
        self: Any, method: str, payload: Mapping[str, JSONValue]
    ) -> dict[str, JSONValue]:
        if method == Method.HANDSHAKE.value:
            return await self._handshake(payload)
        if method == Method.CONFIGURE.value:
            return await self._configure(payload)
        if method == Method.START.value:
            return await self._start(payload)
        if method == Method.STOP.value:
            return await self._stop(payload)
        if method == Method.DISPATCH.value:
            return await self._dispatch_update(payload)
        if method == Method.HEALTH.value:
            return self._health(payload)
        if method.startswith("host."):
            raise UnsupportedAPIError(
                method,
                "a negotiated duplex core transport; this connection currently accepts "
                "host requests",
            )
        raise UnsupportedAPIError(method, "one of the protocol v1 lifecycle methods")

    def _validate_request(self: Any, request: Mapping[str, JSONValue]) -> None:
        if "payload" not in request:
            from serenox_sdk.errors import ProtocolError

            raise ProtocolError("payload is required by every protocol v1 method")
        from serenox_sdk.protocol import RequestV1

        RequestV1.from_dict(request)

    def _success_response(
        self: Any, request: Mapping[str, JSONValue], result: Mapping[str, Any]
    ) -> dict[str, JSONValue]:
        response: dict[str, JSONValue] = {
            "protocol": PROTOCOL_NAME,
            "request_id": required_int(request.get("request_id"), "request_id"),
            "kind": "response",
            "deadline_unix_ms": required_int(request.get("deadline_unix_ms"), "deadline_unix_ms"),
            "result": ensure_json_value(result, field="result"),
        }
        self._ensure_response_size(response)
        return response

    def _error_response(
        self: Any,
        request: Mapping[str, JSONValue],
        code: str,
        message: str,
        *,
        retryable: bool = False,
        field: str = "",
    ) -> dict[str, JSONValue]:
        error: dict[str, JSONValue] = {
            "code": code,
            "message": truncate_utf8(message, MAX_STRING_BYTES),
        }
        if retryable:
            error["retryable"] = True
        if field:
            error["field"] = field
        return {
            "protocol": PROTOCOL_NAME,
            "request_id": required_int(request.get("request_id"), "request_id"),
            "kind": "response",
            "deadline_unix_ms": required_int(request.get("deadline_unix_ms"), "deadline_unix_ms"),
            "error": error,
        }

    def _exception_response(
        self: Any, request: Mapping[str, JSONValue], exc: Exception
    ) -> dict[str, JSONValue]:
        if isinstance(exc, AuthenticationError):
            code = "authentication_failed"
        elif isinstance(exc, IncompatibleProtocolError):
            code = "incompatible_protocol"
        elif isinstance(exc, InvalidStateError):
            code = "invalid_state"
        elif isinstance(exc, InvalidResponseError):
            code = "invalid_response"
        elif isinstance(exc, OverloadedError):
            code = "overloaded"
        elif isinstance(exc, UnsupportedAPIError):
            code = "unsupported"
        elif isinstance(exc, (ProtocolError, ConfigurationError, TypeError, ValueError)):
            code = "invalid_request"
        elif isinstance(exc, TimeoutError):
            code = "deadline_exceeded"
        elif isinstance(exc, ActionError):
            code = exc.action_code
        elif isinstance(exc, SerenoxError):
            code = "plugin_error"
        else:
            code = "plugin_error"
        response = self._error_response(
            request,
            code,
            str(exc),
            retryable=exc.retryable if isinstance(exc, ActionError) else False,
        )
        if isinstance(exc, ActionError):
            error = response.get("error")
            if isinstance(error, dict):
                if exc.operation:
                    error["operation"] = truncate_utf8(exc.operation, MAX_STRING_BYTES)
                if exc.flood_wait_seconds > 0:
                    error["flood_wait_seconds"] = exc.flood_wait_seconds
                if exc.details:
                    error["details"] = ensure_json_value(exc.details, field="error.details")
        if isinstance(exc, UnsupportedAPIError):
            error = response.get("error")
            if isinstance(error, dict):
                details = {
                    "api": exc.api,
                    "replacement": exc.replacement
                    or "consult the protocol v1 compatibility matrix",
                }
                error["details"] = ensure_json_value(details, field="error.details")
        return response

    def _ensure_response_size(self: Any, response: Mapping[str, JSONValue]) -> None:
        size = len(
            json.dumps(response, ensure_ascii=False, allow_nan=False, separators=(",", ":")).encode(
                "utf-8"
            )
        )
        if size > self._negotiated_frame_bytes:
            raise InvalidResponseError(
                f"plugin response is {size} bytes; negotiated limit is "
                f"{self._negotiated_frame_bytes}"
            )
