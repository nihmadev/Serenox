from serenox_sdk import EventPublication, EventSubscription, Module, event_handler, event_success


class NoteConsumer(Module):
    id = "example.note.consumer"
    version = "1.0.0"
    event_publications = (EventPublication("example.note.processed", max_payload_bytes=4096),)
    event_subscriptions = (EventSubscription("example.note.created", "process-note", source_policy="exact_plugin", source_plugin="example.note.producer"),)

    @event_handler("process-note")
    async def process_note(self, envelope):
        await self.events.publish("example.note.processed", {"source_event_id": envelope["id"]}, causation_id=envelope["id"], trace_id=envelope["trace_id"])
        return event_success()
