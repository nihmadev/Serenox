from __future__ import annotations

import unittest
from typing import Any

from serenox_sdk import loader, utils, validators
from serenox_sdk.actions import ClientProxy, Message, RecordingActionSink
from serenox_sdk.config import ConfigValue, ModuleConfig
from serenox_sdk.errors import ConfigurationError, OverloadedError, UnsupportedAPIError
from serenox_sdk.module import Module, ModuleRegistry
from serenox_sdk.protocol import HandlerKind, MessageV1, OperationKind
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


if __name__ == "__main__":
    unittest.main()
