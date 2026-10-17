# Event Fabric composition fixture

The Go producer publishes `example.note.created/v1` with the update ID as its publisher-scoped dedupe key. The Python consumer receives the immutable envelope through stable handler `process-note`, emits `example.note.processed/v1`, and acknowledges only after durable acceptance. The credential-free application and Event Fabric tests exercise dedupe, retry, dead-letter, replay, hop protection, unload, and recovery semantics without Telegram network access.
