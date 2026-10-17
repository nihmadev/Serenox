from __future__ import annotations

import importlib.util
import unittest
from pathlib import Path
from typing import Any

from serenox_sdk import ChatTarget, ParticipantTarget
from serenox_sdk.actions import Message
from serenox_sdk.moderation import Moderation
from serenox_sdk.module import ModuleRegistry
from serenox_sdk.protocol import HandlerKind, MessageV1, OperationV1
from serenox_sidecar.host_utils import matches_filters, wire_watcher_filter


class Sink:
    def __init__(self) -> None:
        self.actions: list[OperationV1] = []

    async def request(self, action: OperationV1) -> Any:
        self.actions.append(action)
        if action.kind.value == "client.get_message":
            return {"message": {"id": 7, "chat_id": -42, "sender_id": 9, "date_unix_ms": 1}}
        return {
            "operation": action.kind.value.removeprefix("moderation."),
            "request_id": "py-1",
            "status": "success",
            "chat_id": -42,
            "acknowledged": True,
        }

    def notify(self, action: OperationV1) -> None:
        self.actions.append(action)


class ModerationSDKTests(unittest.IsolatedAsyncioTestCase):
    async def test_mute_lowers_to_explicit_presence_aware_restriction(self) -> None:
        sink = Sink()
        client = Moderation(sink)
        result = await client.members.mute(
            ChatTarget(-42, "megagroup"),
            ParticipantTarget.replied_message(-42, 7),
            duration_seconds=3600,
            idempotency_key="mute-7",
        )
        self.assertEqual("success", result.status)
        action = sink.actions[-1]
        self.assertEqual("moderation.members.restrict", action.kind.value)
        restriction = action.parameters["restriction"]
        assert isinstance(restriction, dict)
        denied = restriction["denied"]
        until = restriction["until_unix_ms"]
        assert isinstance(denied, dict)
        assert isinstance(until, int)
        self.assertTrue(denied["send_messages"])
        self.assertGreater(until, 0)

    async def test_message_reply_property_and_typed_fetch(self) -> None:
        sink = Sink()
        message = Message(MessageV1(id=8, chat_id=-42, reply_to_message_id=7, date_unix_ms=1), sink)
        self.assertEqual(7, message.reply_to_message_id)
        replied = await message.get_reply_message()
        self.assertIsNotNone(replied)
        self.assertEqual(9, replied.sender_id)
        self.assertEqual(
            {"reply": {"peer_id": -42, "message_id": 7}},
            message.moderation_reply_target(),
        )

    async def test_example_no_prefix_watcher_rejects_incoming_messages(self) -> None:
        path = Path(__file__).parents[3] / "examples/plugins/moderation/plugin.py"
        spec = importlib.util.spec_from_file_location("moderation_example_test", path)
        assert spec is not None and spec.loader is not None
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        registry = ModuleRegistry(module.ModerationExample())
        handler = registry.find(HandlerKind.WATCHER, "moderation_no_prefix")
        self.assertIsNotNone(handler)
        assert handler is not None
        self.assertTrue(wire_watcher_filter(handler.spec.filters)["outgoing"])
        incoming = Message(
            MessageV1(
                id=9,
                chat_id=-42,
                sender_id=77,
                date_unix_ms=1,
                text="мут 1ч",
                outgoing=False,
                chat_kind="supergroup",
                reply_to_message_id=7,
            ),
            Sink(),
        )
        self.assertFalse(matches_filters(handler, incoming, is_command=False))


if __name__ == "__main__":
    unittest.main()
