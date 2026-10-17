from __future__ import annotations

import asyncio
import contextlib
import time
import unittest

from serenox_sdk import loader
from serenox_sdk.actions import Message
from serenox_sdk.config import ConfigValue, ModuleConfig
from serenox_sdk.errors import ProtocolError
from serenox_sdk.module import Module
from serenox_sidecar.framing import FramedJSONStream
from serenox_sidecar.host import HostSettings, SidecarHost


def _request(request_id: int, method: str, payload: dict[str, object]) -> dict[str, object]:
    return {
        "protocol": "serenox.plugin/1",
        "request_id": request_id,
        "kind": "request",
        "deadline_unix_ms": int(time.time() * 1000) + 10_000,
        "method": method,
        "payload": payload,
    }


def _message(text: str, *, outgoing: bool = False) -> dict[str, object]:
    return {
        "id": 10,
        "chat_id": 20,
        "sender_id": 30,
        "date_unix_ms": 1,
        "text": text,
        "outgoing": outgoing,
        "chat_kind": "private",
    }


class FixtureModule(Module):
    id = "fixture-python"
    strings = {"name": "Fixture"}  # noqa: RUF012
    version = "1.2.3"
    config = ModuleConfig(ConfigValue("greeting", "hello"))

    def __init__(self) -> None:
        self.lifecycle: list[str] = []
        self.watch_count = 0

    def config_complete(self) -> None:
        self.lifecycle.append(f"configure:{self.config['greeting']}")

    async def client_ready(self) -> None:
        self.lifecycle.append("start")

    async def on_unload(self) -> None:
        self.lifecycle.append("stop")

    @loader.owner
    @loader.command(name="ping", aliases=("p",))
    async def pingcmd(self, message: Message) -> None:
        await message.reply("reply")
        await message.edit("edit")
        await message.delete()
        self.set("last", message.text)

    @loader.command(name="read")
    async def readcmd(self, message: Message) -> None:
        del message
        await self.client.get_me()

    @loader.watcher("in", startswith="watch")
    async def incoming(self, message: Message) -> None:
        self.watch_count += 1
        await message.reply("watched")


class SlowModule(Module):
    id = "slow-python"
    strings = {"name": "Slow"}  # noqa: RUF012

    def __init__(self) -> None:
        self.entered = asyncio.Event()
        self.release = asyncio.Event()

    @loader.command(name="slow")
    async def slowcmd(self, message: Message) -> None:
        del message
        self.entered.set()
        await self.release.wait()


class LargeActionModule(Module):
    id = "large-action-python"
    strings = {"name": "Large action"}  # noqa: RUF012

    @loader.command(name="large")
    async def largecmd(self, message: Message) -> None:
        await message.reply("x" * 300)


class _PipeWriter:
    def __init__(self, peer: asyncio.StreamReader) -> None:
        self.peer = peer
        self.closed = False

    def write(self, data: bytes) -> None:
        if self.closed:
            raise ConnectionResetError("in-memory pipe is closed")
        self.peer.feed_data(data)

    async def drain(self) -> None:
        await asyncio.sleep(0)

    def close(self) -> None:
        if not self.closed:
            self.closed = True
            self.peer.feed_eof()

    async def wait_closed(self) -> None:
        await asyncio.sleep(0)


class _Harness:
    def __init__(self, host: SidecarHost) -> None:
        self.host = host
        self.task: asyncio.Task[None] | None = None
        self.stream: FramedJSONStream | None = None

    async def start(self) -> None:
        plugin_reader = asyncio.StreamReader()
        core_reader = asyncio.StreamReader()
        plugin_writer = _PipeWriter(core_reader)
        core_writer = _PipeWriter(plugin_reader)
        self.task = asyncio.create_task(
            self.host.serve(plugin_reader, plugin_writer)  # type: ignore[arg-type]
        )
        self.stream = FramedJSONStream(
            core_reader,
            core_writer,  # type: ignore[arg-type]
        )

    async def send(self, request: dict[str, object]) -> None:
        assert self.stream is not None
        await self.stream.write(request)

    async def receive(self) -> dict[str, object]:
        assert self.stream is not None
        response = await asyncio.wait_for(self.stream.read(), timeout=2)
        return dict(response)

    async def call(self, request: dict[str, object]) -> dict[str, object]:
        await self.send(request)
        return await self.receive()

    async def close(self) -> None:
        if self.stream is not None:
            with contextlib.suppress(Exception):
                await self.stream.close()
        if self.task is not None and not self.task.done():
            self.task.cancel()
            await asyncio.gather(self.task, return_exceptions=True)


class SidecarHostTests(unittest.IsolatedAsyncioTestCase):
    async def asyncSetUp(self) -> None:
        self.harnesses: list[_Harness] = []

    async def asyncTearDown(self) -> None:
        for harness in reversed(self.harnesses):
            await harness.close()

    async def make_harness(
        self, module: Module, *, settings: HostSettings | None = None
    ) -> _Harness:
        harness = _Harness(
            SidecarHost(
                module,
                auth_token="secret",
                instance_id="fixture-1",
                settings=settings,
            )
        )
        self.harnesses.append(harness)
        await harness.start()
        return harness

    async def negotiate(self, harness: _Harness, *, start: bool = True) -> None:
        handshake = await harness.call(
            _request(
                1,
                "handshake",
                {
                    "supported_versions": ["1.0"],
                    "auth_token": "secret",
                    "instance_id": "fixture-1",
                    "host_version": "test",
                    "nonce": "nonce",
                    "max_frame_bytes": 1 << 20,
                },
            )
        )
        result = handshake["result"]
        assert isinstance(result, dict)
        self.assertEqual(result["selected_version"], "1.0")
        metadata = result["metadata"]
        assert isinstance(metadata, dict)
        self.assertEqual(metadata["protocol_version"], "1.0")

        config = []
        if isinstance(harness.host.module, FixtureModule):
            config = [{"key": "greeting", "type": "string", "value": "hola"}]
        configured = await harness.call(
            _request(2, "lifecycle.configure", {"phase": "configure", "config": config})
        )
        self.assertEqual(configured["result"], {"state": "configured"})
        if start:
            started = await harness.call(_request(3, "lifecycle.start", {"phase": "start"}))
            self.assertEqual(started["result"], {"state": "ready"})

    async def test_authenticated_lifecycle_dispatch_actions_and_health(self) -> None:
        module = FixtureModule()
        harness = await self.make_harness(module)
        await self.negotiate(harness)
        response = await harness.call(
            _request(
                4,
                "dispatch",
                {
                    "update": {
                        "id": "u1",
                        "kind": "message",
                        "occurred_unix_ms": 1,
                        "message": _message(".ping"),
                    },
                    "command": {
                        "name": "ping",
                        "prefix": ".",
                        "raw_args": "",
                        "args": [],
                        "message": _message(".ping"),
                    },
                },
            )
        )
        result = response["result"]
        assert isinstance(result, dict)
        actions = result["actions"]
        assert isinstance(actions, list)
        self.assertEqual(
            [action["kind"] for action in actions if isinstance(action, dict)],
            ["reply", "edit", "delete", "client_operation"],
        )
        self.assertEqual(actions[0]["reply_to_message_id"], 10)  # type: ignore[index]
        self.assertEqual(actions[1]["message_id"], 10)  # type: ignore[index]

        health = await harness.call(_request(5, "health", {"detailed": True}))
        health_result = health["result"]
        assert isinstance(health_result, dict)
        self.assertTrue(health_result["ready"])
        self.assertIn("metrics", health_result)

        stopped = await harness.call(_request(6, "lifecycle.stop", {"phase": "stop"}))
        self.assertEqual(stopped["result"], {"state": "stopped"})
        assert harness.task is not None
        await asyncio.wait_for(harness.task, timeout=2)
        self.assertEqual(module.lifecycle, ["configure:hola", "start", "stop"])
        self.assertEqual(harness.host.owned_task_count, 0)

    async def test_watcher_metadata_and_filter_defense(self) -> None:
        module = FixtureModule()
        harness = await self.make_harness(module)
        await self.negotiate(harness)
        metadata = harness.host._wire_metadata()
        watchers = metadata["watchers"]
        assert isinstance(watchers, list)
        self.assertEqual(
            watchers[0]["filter"],  # type: ignore[index]
            {
                "incoming": True,
                "text_prefix": "watch",
                "kinds": ["message", "edited_message"],
            },
        )
        unmatched = await harness.call(
            _request(
                4,
                "dispatch",
                {
                    "update": {
                        "id": "u2",
                        "kind": "message",
                        "occurred_unix_ms": 1,
                        "message": _message("ignore"),
                    }
                },
            )
        )
        self.assertEqual(unmatched["result"], {})
        self.assertEqual(module.watch_count, 0)
        matched = await harness.call(
            _request(
                5,
                "dispatch",
                {
                    "update": {
                        "id": "u3",
                        "kind": "message",
                        "occurred_unix_ms": 1,
                        "message": _message("watch me"),
                    }
                },
            )
        )
        matched_result = matched["result"]
        assert isinstance(matched_result, dict)
        self.assertEqual(matched_result["actions"][0]["kind"], "reply")  # type: ignore[index]
        self.assertEqual(module.watch_count, 1)

    async def test_queue_overload_is_bounded_and_retryable(self) -> None:
        module = SlowModule()
        harness = await self.make_harness(
            module,
            settings=HostSettings(queue_capacity=1, worker_count=1, response_capacity=4),
        )
        await self.negotiate(harness)

        def dispatch(request_id: int) -> dict[str, object]:
            return _request(
                request_id,
                "dispatch",
                {
                    "update": {
                        "id": f"u{request_id}",
                        "kind": "message",
                        "occurred_unix_ms": 1,
                        "message": _message(".slow"),
                    },
                    "command": {
                        "name": "slow",
                        "prefix": ".",
                        "message": _message(".slow"),
                    },
                },
            )

        await harness.send(dispatch(4))
        await asyncio.wait_for(module.entered.wait(), timeout=2)
        await harness.send(dispatch(5))
        await harness.send(dispatch(6))
        overloaded = await harness.receive()
        self.assertEqual(overloaded["request_id"], 6)
        error = overloaded["error"]
        assert isinstance(error, dict)
        self.assertEqual(error["code"], "overloaded")
        self.assertTrue(error["retryable"])
        module.release.set()
        completed: set[int] = set()
        for _ in range(2):
            request_id = (await harness.receive())["request_id"]
            assert isinstance(request_id, int)
            completed.add(request_id)
        self.assertEqual(completed, {4, 5})

    async def test_action_size_is_bounded(self) -> None:
        harness = await self.make_harness(
            LargeActionModule(),
            settings=HostSettings(
                max_frame_bytes=1_024,
                max_action_bytes=256,
                queue_capacity=1,
                worker_count=1,
            ),
        )
        await self.negotiate(harness)
        response = await harness.call(
            _request(
                4,
                "dispatch",
                {
                    "update": {
                        "id": "large",
                        "kind": "message",
                        "occurred_unix_ms": 1,
                        "message": _message(".large"),
                    },
                    "command": {
                        "name": "large",
                        "prefix": ".",
                        "message": _message(".large"),
                    },
                },
            )
        )
        error = response["error"]
        assert isinstance(error, dict)
        self.assertEqual(error["code"], "overloaded")
        self.assertIn("configured limit is 256", error["message"])

    async def test_authentication_failure_closes_before_workers_start(self) -> None:
        harness = await self.make_harness(FixtureModule())
        response = await harness.call(
            _request(
                1,
                "handshake",
                {
                    "supported_versions": ["1.0"],
                    "auth_token": "wrong",
                    "instance_id": "fixture-1",
                    "nonce": "nonce",
                    "max_frame_bytes": 1 << 20,
                },
            )
        )
        error = response["error"]
        assert isinstance(error, dict)
        self.assertEqual(error["code"], "authentication_failed")
        assert harness.task is not None
        with self.assertRaises(ProtocolError):
            await harness.task
        self.assertEqual(harness.host.owned_task_count, 0)

    async def test_malformed_envelope_returns_error_and_connection_recovers(self) -> None:
        harness = await self.make_harness(FixtureModule())
        await self.negotiate(harness)
        malformed = _request(4, "health", {})
        del malformed["payload"]
        response = await harness.call(malformed)
        error = response["error"]
        assert isinstance(error, dict)
        self.assertEqual(error["code"], "invalid_request")
        self.assertIn("payload is required", error["message"])
        recovered = await harness.call(_request(5, "health", {"detailed": False}))
        recovered_result = recovered["result"]
        assert isinstance(recovered_result, dict)
        self.assertTrue(recovered_result["ready"])

    async def test_unsupported_api_is_explicit_protocol_error(self) -> None:
        harness = await self.make_harness(FixtureModule())
        await self.negotiate(harness)
        response = await harness.call(
            _request(
                4,
                "dispatch",
                {
                    "update": {
                        "id": "u-read",
                        "kind": "message",
                        "occurred_unix_ms": 1,
                        "message": _message(".read"),
                    },
                    "command": {
                        "name": "read",
                        "prefix": ".",
                        "message": _message(".read"),
                    },
                },
            )
        )
        error = response["error"]
        assert isinstance(error, dict)
        self.assertEqual(error["code"], "unsupported")
        self.assertIn("future duplex host.client RPC", error["message"])
        self.assertEqual(error["details"]["api"], "client.get_me")  # type: ignore[index]

    async def test_disconnect_cancels_workers_and_runs_unload(self) -> None:
        module = FixtureModule()
        harness = await self.make_harness(module)
        await self.negotiate(harness)
        assert harness.task is not None
        harness.task.cancel()
        with self.assertRaises(asyncio.CancelledError):
            await harness.task
        self.assertEqual(module.lifecycle, ["configure:hola", "start", "stop"])
        self.assertEqual(harness.host.owned_task_count, 0)


if __name__ == "__main__":
    unittest.main()
