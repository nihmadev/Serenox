from __future__ import annotations

import unittest

from serenox_sdk.events import (
    EventPublication,
    EventsProxy,
    EventSubscription,
    event_handler,
    event_retry,
)
from serenox_sdk.module import Module, ModuleRegistry
from serenox_sdk.protocol import JSONValue, OperationKind, OperationV1


class Sink:
    def __init__(self) -> None:
        self.action: OperationV1 | None = None

    async def request(self, action: OperationV1) -> JSONValue:
        self.action = action
        return {"event_id": "evt_1", "accepted": True}

    def notify(self, action: OperationV1) -> None:
        raise AssertionError(action)


class Consumer(Module):
    id = "example.consumer"
    event_publications = (EventPublication("example.note.processed"),)
    event_subscriptions = (EventSubscription("example.note.created", "notes"),)

    @event_handler("notes")
    async def notes(self, envelope):
        return event_retry("again")


class EventSDKTests(unittest.IsolatedAsyncioTestCase):
    async def test_publish_uses_typed_reverse_rpc(self) -> None:
        sink = Sink()
        proxy = EventsProxy(sink)
        result = await proxy.publish("example.note.created", {"text": "hello"}, dedupe_key="note-1")
        self.assertTrue(result["accepted"])
        assert sink.action is not None
        self.assertEqual(sink.action.kind, OperationKind.EVENT_PUBLISH)
        self.assertEqual(sink.action.parameters["dedupe_key"], "note-1")

    async def test_registry_declares_and_registers_stable_handler(self) -> None:
        module = Consumer()
        registry = ModuleRegistry(
            module, allowed_capabilities=("events.publish", "events.subscribe")
        )
        self.assertIn("notes", registry.event_handlers)
        self.assertEqual(registry.metadata.event_subscriptions[0]["type"], "example.note.created")
        self.assertEqual(
            set(registry.metadata.capabilities), {"events.publish", "events.subscribe"}
        )


if __name__ == "__main__":
    unittest.main()
