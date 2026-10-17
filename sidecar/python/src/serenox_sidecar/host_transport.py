"""Connection lifecycle, bounded queues, and worker/writer loops."""

from __future__ import annotations

import asyncio
import contextlib
from typing import Any

from serenox_sdk.errors import ConnectionClosedError, ProtocolError
from serenox_sdk.protocol import JSONValue, Method

from .framing import FramedJSONStream
from .host_models import HostState, ResponseItem, WorkItem
from .host_reverse_rpc import HostReverseRPCMixin
from .host_utils import has_response_identity, required_string
from .transport import TransportConnector


class HostTransportMixin(HostReverseRPCMixin):
    async def connect_and_serve(
        self: Any, address: str, connector: TransportConnector | None = None
    ) -> None:
        from .transport import connector_for

        selected_connector = connector or connector_for(address)
        connection = await selected_connector.connect(address)
        await self.serve(connection.reader, connection.writer)

    async def serve(self: Any, reader: asyncio.StreamReader, writer: asyncio.StreamWriter) -> None:
        stream = FramedJSONStream(reader, writer, max_frame_bytes=self.settings.max_frame_bytes)
        writer_task: asyncio.Task[None] | None = None
        worker_tasks: list[asyncio.Task[None]] = []
        try:
            await self._handshake_exchange(stream)
            stream.max_frame_bytes = self._negotiated_frame_bytes
            self._http_stream = stream
            if self._streaming_actions_enabled:
                self._sink.set_stream_sender(
                    lambda actions: self._send_streaming_actions(stream, actions)
                )
            if self._duplex_enabled:
                self._sink.set_duplex_sender(lambda action: self._request_host(stream, action))
                self._sink.set_action_sender(
                    lambda actions: self._request_host_actions(stream, actions)
                )
            writer_task = asyncio.create_task(self._writer_loop(stream), name="sidecar-writer")
            worker_tasks = [
                asyncio.create_task(self._worker_loop(), name=f"sidecar-worker-{index}")
                for index in range(self.settings.worker_count)
            ]
            self._tasks = {writer_task, *worker_tasks}
            await self._reader_loop(stream)
        except ConnectionClosedError:
            pass
        finally:
            self._shutdown.set()
            with contextlib.suppress(TimeoutError):
                await asyncio.wait_for(
                    self._requests.join(), timeout=self.settings.shutdown_timeout_seconds
                )
            for task in worker_tasks:
                task.cancel()
            await asyncio.gather(*worker_tasks, return_exceptions=True)
            if writer_task is not None and not writer_task.done():
                try:
                    self._responses.put_nowait(None)
                except asyncio.QueueFull:
                    writer_task.cancel()
            if writer_task is not None:
                try:
                    await asyncio.wait_for(
                        asyncio.shield(writer_task), timeout=self.settings.shutdown_timeout_seconds
                    )
                except (TimeoutError, asyncio.CancelledError):
                    writer_task.cancel()
                    await asyncio.gather(writer_task, return_exceptions=True)
            await self._unload_after_disconnect()
            self._sink.set_stream_sender(None)
            self._sink.set_duplex_sender(None)
            self._sink.set_action_sender(None)
            self._http_stream = None
            for future in tuple(self._pending_host_requests.values()):
                if not future.done():
                    future.set_exception(ConnectionClosedError("core connection closed"))
            self._pending_host_requests.clear()
            await stream.close()
            self._executor.shutdown(wait=False, cancel_futures=True)
            self._tasks.clear()
            self.state = HostState.STOPPED

    async def _handshake_exchange(self: Any, stream: FramedJSONStream) -> None:
        request = await stream.read()
        try:
            self._validate_request(request)
            if request.get("method") != Method.HANDSHAKE.value:
                from .host_models import InvalidStateError

                raise InvalidStateError("first request must be handshake")
            response, _ = await self._process_request(request)
        except Exception as exc:
            if has_response_identity(request):
                await stream.write(self._exception_response(request, exc))
            raise
        stream.max_frame_bytes = self._negotiated_frame_bytes
        await stream.write(response)

    async def _reader_loop(self: Any, stream: FramedJSONStream) -> None:
        while not self._shutdown.is_set():
            request: dict[str, JSONValue] | None = None
            try:
                request = await stream.read()
                response_id = request.get("request_id")
                if request.get("kind") == "response":
                    if (
                        isinstance(response_id, int)
                        and not isinstance(response_id, bool)
                        and response_id in self._streaming_request_ids
                    ):
                        self._streaming_request_ids.discard(response_id)
                        continue
                    if isinstance(response_id, int) and not isinstance(response_id, bool):
                        future = self._pending_host_requests.pop(response_id, None)
                        if future is not None and not future.done():
                            future.set_result(request)
                        else:
                            self._metrics["unknown_response"] = (
                                self._metrics.get("unknown_response", 0) + 1
                            )
                    continue
                self._validate_request(request)
            except ConnectionClosedError:
                raise
            except ProtocolError as exc:
                self._metrics["malformed"] += 1
                if request is not None and has_response_identity(request):
                    await self._responses.put(
                        ResponseItem(self._error_response(request, "invalid_request", str(exc)))
                    )
                    continue
                raise
            method = required_string(request.get("method"), "method")
            if method == Method.HANDSHAKE.value:
                await self._responses.put(
                    ResponseItem(
                        self._error_response(
                            request, "invalid_state", "handshake is already complete"
                        )
                    )
                )
                continue
            if method == Method.DISPATCH.value:
                if self.state is not HostState.RUNNING:
                    await self._responses.put(
                        ResponseItem(
                            self._error_response(
                                request, "invalid_state", "dispatch requires started state"
                            )
                        )
                    )
                    continue
                try:
                    self._requests.put_nowait(WorkItem(request))
                    self._metrics["accepted"] += 1
                except asyncio.QueueFull:
                    self._metrics["overloaded"] += 1
                    await self._responses.put(
                        ResponseItem(
                            self._error_response(
                                request,
                                "overloaded",
                                "Python plugin dispatch queue is full",
                                retryable=True,
                            )
                        )
                    )
                continue
            if method == Method.STOP.value:
                with contextlib.suppress(TimeoutError):
                    await asyncio.wait_for(
                        self._requests.join(), timeout=self.settings.shutdown_timeout_seconds
                    )
            try:
                response, close_after = await self._process_request(request)
            except Exception as exc:
                self._metrics["failed"] += 1
                response = self._exception_response(request, exc)
                close_after = False
            await self._responses.put(ResponseItem(response, close_after=close_after))
            if close_after:
                return

    async def _writer_loop(self: Any, stream: FramedJSONStream) -> None:
        while True:
            item = await self._responses.get()
            try:
                if item is None:
                    return
                try:
                    await stream.write(item.response)
                except (ProtocolError, ConnectionError, OSError):
                    self._shutdown.set()
                    await stream.close()
                    raise
                if item.close_after:
                    self._shutdown.set()
                    await stream.close()
                    return
            finally:
                self._responses.task_done()

    async def _worker_loop(self: Any) -> None:
        while True:
            item = await self._requests.get()
            try:
                response, close_after = await self._process_request(item.request)
                await self._responses.put(ResponseItem(response, close_after=close_after))
                self._metrics["completed"] += 1
            except Exception as exc:
                self._metrics["failed"] += 1
                await self._responses.put(ResponseItem(self._exception_response(item.request, exc)))
            finally:
                self._requests.task_done()

    async def _unload_after_disconnect(self: Any) -> None:
        if self._unloaded or self.state not in {
            HostState.CONFIGURED,
            HostState.RUNNING,
            HostState.STOPPING,
        }:
            return
        self.state = HostState.STOPPING
        try:
            await asyncio.wait_for(
                self._call(self.module.on_unload), timeout=self.settings.shutdown_timeout_seconds
            )
        except (Exception, asyncio.CancelledError):
            pass
        finally:
            self._unloaded = True

    @property
    def owned_task_count(self: Any) -> int:
        return len(self._tasks)
