"""Plugin-to-host reverse RPC request paths and admission bounds."""

from __future__ import annotations

import asyncio
from typing import Any

from serenox_sdk.errors import ActionError, ConnectionClosedError, OverloadedError, ProtocolError
from serenox_sdk.protocol import PROTOCOL_NAME, HTTPRequestV1, JSONValue, Method, OperationKind

from .framing import FramedJSONStream
from .host_models import HostState


class HostReverseRPCMixin:
    async def _request_host(self: Any, stream: FramedJSONStream, action: Any) -> JSONValue:
        if self._shutdown.is_set():
            raise ConnectionClosedError("core connection is closed")
        try:
            await asyncio.wait_for(
                self._host_request_sem.acquire(), timeout=self.settings.max_handler_timeout_seconds
            )
        except TimeoutError as exc:
            raise OverloadedError("host reverse RPC concurrency limit reached") from exc
        try:
            async with self._host_request_lock:
                self._host_request_id += 1
                request_id = self._host_request_id
            now_ms = int(self.wall_clock() * 1000)
            payload: dict[str, JSONValue]
            method: str
            target = action.target
            parameters = action.parameters
            if action.kind in {
                OperationKind.STORAGE_GET,
                OperationKind.STORAGE_SET,
                OperationKind.STORAGE_DELETE,
            }:
                payload = {
                    "operation": action.kind.value.removeprefix("storage."),
                    "namespace": str(target.get("namespace", "")),
                    "key": str(target.get("key", "")),
                }
                if action.kind is OperationKind.STORAGE_SET:
                    payload["value"] = action.parameters.get("value")
                method = Method.HOST_STORAGE.value
            elif action.kind is OperationKind.EVENT_PUBLISH:
                payload = dict(parameters)
                method = Method.HOST_EVENTS.value
            elif action.kind in {
                OperationKind.JOB_CREATE,
                OperationKind.JOB_UPDATE,
                OperationKind.JOB_CANCEL,
                OperationKind.JOB_PAUSE,
                OperationKind.JOB_RESUME,
                OperationKind.JOB_TRIGGER,
                OperationKind.JOB_GET,
                OperationKind.JOB_LIST,
                OperationKind.JOB_INSPECT,
                OperationKind.JOB_CLEAR,
                OperationKind.JOB_DELETE,
            }:
                payload = dict(action.parameters)
                payload = {"operation": action.kind.value.removeprefix("job."), "payload": payload}
                method = Method.HOST_JOBS.value
            elif action.kind in {
                OperationKind.MEDIA_INSPECT,
                OperationKind.MEDIA_DOWNLOAD,
                OperationKind.MEDIA_UPLOAD,
                OperationKind.MEDIA_ATTACH,
                OperationKind.MEDIA_DELETE,
            }:
                payload = {
                    "operation": action.kind.value.removeprefix("media."),
                    "handle": target.get("handle", ""),
                    "peer_id": target.get("chat_id", 0),
                }
                payload.update(
                    {
                        key: value
                        for key, value in parameters.items()
                        if key
                        in {
                            "kind",
                            "file_name",
                            "mime_type",
                            "size",
                            "checksum",
                            "caption",
                            "parse_mode",
                            "reply_to_message_id",
                        }
                    }
                )
                method = Method.HOST_MEDIA.value
            elif action.kind.value.startswith("moderation."):
                payload = {
                    "operation": action.kind.value.removeprefix("moderation."),
                    "request_id": f"py-{request_id}",
                }
                payload.update(dict(action.target))
                payload.update(dict(action.parameters))
                method = Method.HOST_MODERATION.value
            else:
                peer = target.get("peer")
                payload = {"operation": action.kind.value.removeprefix("client.")}
                if isinstance(peer, int):
                    payload["peer_id"] = peer
                elif isinstance(peer, str):
                    payload["username"] = peer.lstrip("@")
                for key in {
                    "peer_id",
                    "message_id",
                    "message_ids",
                    "user_ids",
                    "chat_ids",
                    "sender_id",
                    "query",
                    "media_kind",
                    "folder_id",
                    "before",
                    "after",
                    "cursor",
                    "direction",
                    "handle",
                }:
                    if key in target:
                        payload[key] = target[key]
                if "chat_id" in target:
                    payload["peer_id"] = target["chat_id"]
                for key in {
                    "peer_id",
                    "message_id",
                    "message_ids",
                    "user_ids",
                    "chat_ids",
                    "sender_id",
                    "query",
                    "media_kind",
                    "folder_id",
                    "before",
                    "after",
                    "cursor",
                    "direction",
                    "handle",
                }:
                    if key in parameters:
                        payload[key] = parameters[key]
                method = Method.HOST_CLIENT.value
            request = {
                "protocol": PROTOCOL_NAME,
                "request_id": request_id,
                "kind": "request",
                "deadline_unix_ms": now_ms + int(self.settings.max_handler_timeout_seconds * 1000),
                "method": method,
                "payload": payload,
            }
            loop = asyncio.get_running_loop()
            future: asyncio.Future[dict[str, Any]] = loop.create_future()
            self._pending_host_requests[request_id] = future
            try:
                await stream.write(request)
                response = await asyncio.wait_for(
                    future, timeout=self.settings.max_handler_timeout_seconds
                )
            except asyncio.CancelledError:
                self._pending_host_requests.pop(request_id, None)
                raise
            except TimeoutError as exc:
                self._pending_host_requests.pop(request_id, None)
                raise ActionError(
                    "host reverse RPC deadline exceeded",
                    action_code="deadline_exceeded",
                    retryable=True,
                ) from exc
            if "error" in response:
                error = response.get("error")
                if isinstance(error, dict):
                    code = str(error.get("code", "plugin_error"))
                    self._metrics[
                        "storage_commit_failure"
                        if method == Method.HOST_STORAGE.value and code == "storage_commit_failed"
                        else "client_failure"
                        if method in {Method.HOST_CLIENT.value, Method.HOST_MODERATION.value}
                        else "failed"
                    ] += 1
                    raise ActionError(
                        str(error.get("message", "host RPC failed")),
                        action_code=code,
                        retryable=bool(error.get("retryable", False)),
                        operation=str(error.get("operation", payload.get("operation", ""))),
                        flood_wait_seconds=int(error.get("flood_wait_seconds", 0) or 0),
                        details=error.get("details")
                        if isinstance(error.get("details"), dict)
                        else None,
                    )
                raise ActionError("host RPC failed")
            result = response.get("result")
            if not isinstance(result, dict):
                raise ProtocolError("host RPC result must be an object")
            if method == Method.HOST_STORAGE.value:
                operation = payload["operation"]
                if operation == "get":
                    return result.get("value") if result.get("found") is True else None
                return bool(result.get("committed", False))
            if method == Method.HOST_JOBS.value:
                return result
            if method == Method.HOST_EVENTS.value:
                return result
            if method == Method.HOST_MEDIA.value:
                return result
            if method == Method.HOST_MODERATION.value:
                return result
            if payload["operation"] == "get_me":
                return result.get("me") or {}
            if payload["operation"] == "get_message":
                return (
                    {"message": result["message"]}
                    if isinstance(result.get("message"), dict)
                    else None
                )
            return result.get("peer") or {}
        finally:
            self._host_request_sem.release()

    async def _request_host_actions(
        self: Any, stream: FramedJSONStream, actions: list[dict[str, JSONValue]]
    ) -> JSONValue:
        if self._shutdown.is_set():
            raise ConnectionClosedError("core connection is closed")
        try:
            await asyncio.wait_for(
                self._host_request_sem.acquire(), timeout=self.settings.max_handler_timeout_seconds
            )
        except TimeoutError as exc:
            raise OverloadedError("host action concurrency limit reached") from exc
        try:
            async with self._host_request_lock:
                self._host_request_id += 1
                request_id = self._host_request_id
            request = {
                "protocol": PROTOCOL_NAME,
                "request_id": request_id,
                "kind": "request",
                "deadline_unix_ms": int(self.wall_clock() * 1000)
                + int(self.settings.max_handler_timeout_seconds * 1000),
                "method": Method.HOST_ACTIONS.value,
                "payload": {"actions": actions},
            }
            loop = asyncio.get_running_loop()
            future: asyncio.Future[dict[str, Any]] = loop.create_future()
            self._pending_host_requests[request_id] = future
            try:
                await stream.write(request)
                response = await asyncio.wait_for(
                    future, timeout=self.settings.max_handler_timeout_seconds
                )
            except asyncio.CancelledError:
                self._pending_host_requests.pop(request_id, None)
                raise
            except TimeoutError as exc:
                self._pending_host_requests.pop(request_id, None)
                raise ActionError(
                    "host action deadline exceeded",
                    action_code="outcome_unknown",
                    retryable=False,
                ) from exc
            if "error" in response:
                error = response.get("error")
                if isinstance(error, dict):
                    raise ActionError(
                        str(error.get("message", "host action failed")),
                        action_code=str(error.get("code", "plugin_error")),
                        retryable=bool(error.get("retryable", False)),
                        details=error.get("details")
                        if isinstance(error.get("details"), dict)
                        else None,
                    )
                raise ActionError("host action failed")
            result = response.get("result")
            if not isinstance(result, dict):
                raise ProtocolError("host action result must be an object")
            results = result.get("results")
            if not isinstance(results, list) or not results:
                raise ProtocolError("host action result must contain results")
            return results[0] if len(results) == 1 else result
        finally:
            self._host_request_sem.release()

    async def _request_host_http(self: Any, request: HTTPRequestV1) -> dict[str, Any]:
        stream = self._http_stream
        if stream is None or self._shutdown.is_set() or self.state not in {HostState.RUNNING}:
            raise ConnectionClosedError(
                "host HTTP service is unavailable outside a running lifecycle"
            )
        try:
            await asyncio.wait_for(
                self._host_request_sem.acquire(), timeout=self.settings.max_handler_timeout_seconds
            )
        except TimeoutError as exc:
            raise OverloadedError("host HTTP concurrency limit reached") from exc
        async with self._host_request_lock:
            self._host_request_id += 1
            request_id = self._host_request_id
        timeout = (
            request.timeout_ms / 1000
            if request.timeout_ms > 0
            else self.settings.max_handler_timeout_seconds
        )
        timeout = min(timeout, self.settings.max_handler_timeout_seconds)
        wire = {
            "protocol": PROTOCOL_NAME,
            "request_id": request_id,
            "kind": "request",
            "deadline_unix_ms": int(self.wall_clock() * 1000) + int(timeout * 1000),
            "method": Method.HOST_HTTP.value,
            "payload": request.to_dict(),
        }
        loop = asyncio.get_running_loop()
        future: asyncio.Future[dict[str, Any]] = loop.create_future()
        self._pending_host_requests[request_id] = future
        try:
            await stream.write(wire)
            response = await asyncio.wait_for(future, timeout=timeout)
        except asyncio.CancelledError:
            self._pending_host_requests.pop(request_id, None)
            raise
        except TimeoutError as exc:
            self._pending_host_requests.pop(request_id, None)
            raise ActionError(
                "host HTTP deadline exceeded", action_code="http_timeout", retryable=True
            ) from exc
        finally:
            self._host_request_sem.release()
        if "error" in response:
            error = response.get("error")
            if isinstance(error, dict):
                raise ActionError(
                    str(error.get("message", "host HTTP request failed")),
                    action_code=str(error.get("code", "plugin_error")),
                    retryable=bool(error.get("retryable", False)),
                    details=error.get("details")
                    if isinstance(error.get("details"), dict)
                    else None,
                )
            raise ActionError("host HTTP request failed")
        result = response.get("result")
        if not isinstance(result, dict):
            raise ProtocolError("host HTTP result must be an object")
        return result

    async def _send_streaming_actions(
        self: Any, stream: FramedJSONStream, actions: list[dict[str, JSONValue]]
    ) -> None:
        if not actions:
            return
        self._next_host_request_id += 1
        self._streaming_request_ids.add(self._next_host_request_id)
        await stream.write(
            {
                "protocol": PROTOCOL_NAME,
                "request_id": self._next_host_request_id,
                "kind": "request",
                "deadline_unix_ms": int(self.wall_clock() * 1000) + 30_000,
                "method": Method.HOST_ACTIONS.value,
                "payload": {"actions": actions},
            }
        )
