from __future__ import annotations

import asyncio
import importlib.util
import unittest
from pathlib import Path

from serenox_sdk.actions import CallbackRegistry, Message, RecordingActionSink
from serenox_sdk.module import ModuleRegistry, RuntimeContext
from serenox_sdk.protocol import MessageV1, OperationKind
from serenox_sdk.storage import DatabaseProxy


def _load_reference() -> type:
    repository = Path(__file__).resolve().parents[3]
    filename = repository / "examples" / "plugins" / "uwu" / "plugin.py"
    spec = importlib.util.spec_from_file_location("example_uwu", filename)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"cannot load {filename}")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module.UwU


class MigrationReferenceTests(unittest.TestCase):
    def _bound_module(self):
        module_class = _load_reference()
        module = module_class()
        sink = RecordingActionSink()
        module._bind(
            RuntimeContext(
                sink=sink,
                database=DatabaseProxy(sink),
                callbacks=CallbackRegistry(),
                capabilities=frozenset({"commands", "watchers"}),
            )
        )
        return module, sink

    def test_reference_metadata_declares_command_and_pre_send_hook(self) -> None:
        module_class = _load_reference()
        registry = ModuleRegistry(
            module_class(),
            allowed_capabilities=("commands", "pre_send"),
        )
        self.assertEqual(registry.metadata.id, "example.uwu")
        self.assertEqual(registry.metadata.capabilities, ("commands", "pre_send"))
        self.assertEqual(len(registry.metadata.handlers), 2)

    def test_uwuify_adds_cute_speech_and_is_idempotent(self) -> None:
        module_path = Path(__file__).resolve().parents[3] / "examples/plugins/uwu/plugin.py"
        spec = importlib.util.spec_from_file_location("example_uwu_helpers", module_path)
        assert spec is not None and spec.loader is not None
        helper_module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(helper_module)

        styled = helper_module.uwuify("Really lovely!", variant=1)
        self.assertIn("Weawwy wovewy!~", styled)
        self.assertIn("❤️", styled)
        self.assertIn("💕", styled)
        self.assertIn("мррр~", styled)
        self.assertEqual(helper_module.uwuify(styled, variant=2), styled)

    def test_enable_formats_send_actions_and_disable_stops_it(self) -> None:
        module, sink = self._bound_module()
        command = Message(MessageV1(id=1, chat_id=42, text=".uwu enable", sender_id=7), sink)
        asyncio.run(module.uwu(command))
        self.assertTrue(module.config["enabled"])
        self.assertEqual(sink.actions[-1].kind, OperationKind.REPLY)
        self.assertEqual(sink.actions[-1].parameters["parse_mode"], "plain")

        outgoing = type("Send", (), {"text": "Really lovely!", "chat_id": 42})()
        asyncio.run(module.uwu_pre_send(outgoing))
        self.assertIn("Weawwy wovewy", outgoing.text)

        disable = Message(MessageV1(id=3, chat_id=42, text=".uwu disable", sender_id=7), sink)
        asyncio.run(module.uwu(disable))
        outgoing.text = "Really lovely!"
        asyncio.run(module.uwu_pre_send(outgoing))
        self.assertFalse(module.config["enabled"])
        self.assertEqual(outgoing.text, "Really lovely!")


if __name__ == "__main__":
    unittest.main()
