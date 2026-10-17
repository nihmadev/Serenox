from __future__ import annotations

import unittest
from typing import Any

from serenox_sdk import loader, utils, validators
from serenox_sdk.actions import (
    CallbackRegistry,
    ClientProxy,
    HTTPClient,
    InlineProxy,
    Message,
    RecordingActionSink,
)
from serenox_sdk.config import ConfigValue, ModuleConfig
from serenox_sdk.errors import ConfigurationError, OverloadedError, UnsupportedAPIError
from serenox_sdk.module import Module, ModuleRegistry
from serenox_sdk.protocol import HandlerKind, HTTPRequestV1, MessageV1, OperationKind
from serenox_sdk.storage import DatabaseProxy


class DecoratedModule(Module):
    strings = {"name": "Decorated"}  # noqa: RUF012

    @loader.owner
    @loader.command(aliases=("p",), name="ping")
    async def pingcmd(self, message: Message) -> None:
        await message.reply("pong")

    @loader.watcher("in", startswith="watch", only_media=True)
    async def incoming(self, message: Message) -> None:
        await message.reply("seen")

    @loader.inline_handler
    async def query(self, query: Any) -> None:
        del query


class SDKCompatibilityTests(unittest.IsolatedAsyncioTestCase):
    async def test_typed_http_request_and_json_limit(self) -> None:
        seen: list[HTTPRequestV1] = []

        async def callback(request: HTTPRequestV1) -> dict[str, Any]:
            seen.append(request)
            return {
                "status": 200,
                "final_url": "https://example.test",
                "body": "eyJvayI6dHJ1ZX0=",
                "elapsed_ms": 1,
            }

        response = await HTTPClient(callback, frozenset({"http"})).request(
            "GET", "https://example.test", timeout=1
        )
        self.assertEqual(response.json(max_bytes=64), {"ok": True})
        self.assertEqual(seen[0].method, "GET")
        with self.assertRaises(ValueError):
            response.json(max_bytes=1)

    async def test_message_reply_edit_delete_actions(self) -> None:
        sink = RecordingActionSink()
        message = Message(MessageV1(id=2, chat_id=1, text="hi"), sink)
        await message.reply("reply")
        await message.edit("edit")
        self.assertTrue(await message.delete())
        self.assertEqual(
            [action.kind for action in sink.actions],
            [OperationKind.REPLY, OperationKind.EDIT, OperationKind.DELETE],
        )

    async def test_client_read_and_unknown_apis_fail_actionably(self) -> None:
        sink = RecordingActionSink()
        client = ClientProxy(sink)
        self.assertEqual(await client.get_me(), {})
        with self.assertRaisesRegex(UnsupportedAPIError, "Client.download_media") as caught:
            await client.download_media("opaque")
        self.assertIn("send_message", str(caught.exception))
        message = Message(MessageV1(id=2, chat_id=1), sink)
        with self.assertRaisesRegex(UnsupportedAPIError, "Message.forward_to"):
            await message.forward_to(3)
        with self.assertRaisesRegex(UnsupportedAPIError, "utils.run_sync"):
            utils.run_sync()

    async def test_expanded_client_groups_emit_bounded_typed_operations(self) -> None:
        sink = RecordingActionSink()
        client = ClientProxy(sink)
        await client.history.list(42, limit=2)
        await client.search.by_sender(42, 7, query="mod", limit=2)
        await client.media_metadata.inspect("opaque-handle")
        self.assertEqual(
            [action.kind for action in sink.actions],
            [
                OperationKind.GET_HISTORY,
                OperationKind.SEARCH_MESSAGES_BY_SENDER,
                OperationKind.INSPECT_MEDIA,
            ],
        )

    async def test_native_unsupported_operations_fail_before_action_collection(self) -> None:
        sink = RecordingActionSink()
        client = ClientProxy(sink)
        with self.assertRaisesRegex(UnsupportedAPIError, "Client.join_chat"):
            await client.join_chat("@example")
        with self.assertRaisesRegex(UnsupportedAPIError, "Client.leave_chat"):
            await client.leave_chat("@example")
        with self.assertRaisesRegex(UnsupportedAPIError, "Client.send_reaction"):
            await client.send_reaction(1, 2, "👍")

        inline = InlineProxy(sink, callbacks=CallbackRegistry(), clock=lambda: 0.0)
        with self.assertRaisesRegex(UnsupportedAPIError, "Inline.answer"):
            await inline.answer("query", [])
        self.assertEqual(sink.actions, [])

    async def test_config_and_storage_are_typed_and_bounded(self) -> None:
        config = ModuleConfig(
            ConfigValue("enabled", True, validator=validators.Boolean()),
            ConfigValue("limit", 2, validator=validators.Integer(minimum=1, maximum=3)),
        )
        config.apply({"enabled": "false", "limit": "3"})
        self.assertFalse(config["enabled"])
        self.assertEqual(config["limit"], 3)
        with self.assertRaises(ConfigurationError):
            config.apply({"unknown": 1})

        sink = RecordingActionSink(capacity=1)
        database = DatabaseProxy(sink, max_entries=1, max_value_bytes=64)
        self.assertTrue(database.set("mod", "key", {"ok": True}))
        with self.assertRaises(OverloadedError):
            database.set("mod", "second", 2)
        self.assertEqual(database.get("mod", "key"), {"ok": True})
        with self.assertRaises(OverloadedError):
            database.set("mod", "key", "x" * 100)

    async def test_storage_rolls_back_if_action_queue_is_full(self) -> None:
        sink = RecordingActionSink(capacity=1)
        database = DatabaseProxy(sink, max_entries=2)
        database.set("mod", "first", 1)
        with self.assertRaises(OverloadedError):
            database.set("mod", "second", 2)
        self.assertIsNone(database.get("mod", "second"))

    async def test_decorators_publish_core_filters_and_owner_metadata(self) -> None:
        module = DecoratedModule()
        # The production host binds before registry construction; discovery itself
        # only needs translated strings for this fixture.
        registry = ModuleRegistry(module)
        command = registry.find(HandlerKind.COMMAND, "p")
        self.assertIsNotNone(command)
        assert command is not None
        self.assertTrue(getattr(command.callback, "__serenox_owner_only__", False))
        watcher = next(iter(registry.by_kind(HandlerKind.WATCHER)))
        self.assertEqual(watcher.spec.filters["in"], True)
        self.assertEqual(watcher.spec.filters["startswith"], "watch")

    async def test_manifest_capabilities_bound_sidecar_metadata(self) -> None:
        registry = ModuleRegistry(
            DecoratedModule(),
            allowed_capabilities=("commands", "watchers", "inline"),
        )
        self.assertEqual(registry.metadata.capabilities, ("commands", "inline", "watchers"))
        with self.assertRaisesRegex(ValueError, "undeclared capabilities"):
            ModuleRegistry(DecoratedModule(), allowed_capabilities=("commands",))


if __name__ == "__main__":
    unittest.main()
