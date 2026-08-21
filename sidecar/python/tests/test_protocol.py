from __future__ import annotations

import unittest

from serenox_sdk.errors import ProtocolError
from serenox_sdk.protocol import (
    ActionKind,
    ActionV1,
    EnvelopeV1,
    MediaV1,
    MessageV1,
    Method,
    RequestV1,
    ResponseV1,
    WatcherFilterV1,
)


class ProtocolContractTests(unittest.TestCase):
    """Golden field names mirror pkg/pluginproto/v1/types.go."""

    def test_request_envelope_exact_fields(self) -> None:
        request = RequestV1(
            request_id=7,
            deadline_unix_ms=1_700_000_000_000,
            method=Method.HEALTH,
            payload={"detailed": True},
        )
        self.assertEqual(
            request.to_dict(),
            {
                "protocol": "serenox.plugin/1",
                "request_id": 7,
                "kind": "request",
                "deadline_unix_ms": 1_700_000_000_000,
                "method": "health",
                "payload": {"detailed": True},
            },
        )

    def test_response_error_uses_no_result(self) -> None:
        response = ResponseV1.from_dict(
            {
                "protocol": "serenox.plugin/1",
                "request_id": 8,
                "kind": "response",
                "deadline_unix_ms": 1_700_000_000_000,
                "error": {"code": "overloaded", "message": "full", "retryable": True},
            }
        )
        self.assertIsNone(response.result)
        self.assertEqual(response.error.code if response.error else None, "overloaded")
        with self.assertRaisesRegex(ProtocolError, "result must be absent"):
            ResponseV1.from_dict(
                {
                    "protocol": "serenox.plugin/1",
                    "request_id": 8,
                    "kind": "response",
                    "deadline_unix_ms": 1_700_000_000_000,
                    "result": {},
                    "error": {"code": "overloaded", "message": "full"},
                }
            )

    def test_message_exact_go_field_names_and_omitempty(self) -> None:
        self.assertEqual(
            MessageV1(id=1, chat_id=2).to_dict(),
            {"id": 1, "chat_id": 2, "date_unix_ms": 0},
        )
        message = MessageV1.from_dict(
            {
                "id": 11,
                "chat_id": -10012,
                "sender_id": 13,
                "date_unix_ms": 14,
                "edit_date_unix_ms": 15,
                "text": "hello",
                "outgoing": True,
                "mentioned": True,
                "chat_kind": "supergroup",
                "via_bot": True,
                "forwarded": True,
                "reply_to_message_id": 10,
                "entities": [{"type": "bold", "offset": 0, "length": 5}],
                "media": {
                    "kind": "document",
                    "file_id": "opaque",
                    "file_name": "a.txt",
                    "mime_type": "text/plain",
                    "size": 3,
                },
            }
        )
        encoded = message.to_dict()
        self.assertEqual(encoded["reply_to_message_id"], 10)
        self.assertNotIn("reply_to_id", encoded)
        self.assertEqual(
            encoded["media"],
            {
                "kind": "document",
                "file_id": "opaque",
                "file_name": "a.txt",
                "mime_type": "text/plain",
                "size": 3,
            },
        )

    def test_wire_action_and_watcher_filter_names(self) -> None:
        action = ActionV1(
            id="a1",
            kind=ActionKind.REPLY,
            chat_id=9,
            reply_to_message_id=8,
            text="ok",
        )
        self.assertEqual(
            action.to_dict(),
            {
                "id": "a1",
                "kind": "reply",
                "chat_id": 9,
                "reply_to_message_id": 8,
                "text": "ok",
            },
        )
        watcher_filter = WatcherFilterV1(
            kinds=("message",), incoming=True, text_prefix="."
        ).to_dict()
        self.assertEqual(
            watcher_filter,
            {"kinds": ["message"], "incoming": True, "text_prefix": "."},
        )

    def test_malformed_envelopes_are_rejected(self) -> None:
        with self.assertRaisesRegex(ProtocolError, "protocol must be"):
            EnvelopeV1.from_dict(
                {
                    "protocol": "other",
                    "request_id": 1,
                    "kind": "request",
                    "deadline_unix_ms": 1,
                    "method": "health",
                    "payload": {},
                }
            )
        with self.assertRaisesRegex(ProtocolError, "unknown request method"):
            RequestV1.from_dict(
                {
                    "protocol": "serenox.plugin/1",
                    "request_id": 1,
                    "kind": "request",
                    "deadline_unix_ms": 1,
                    "method": "unknown",
                    "payload": {},
                }
            )

    def test_media_minimal_matches_go_omitempty(self) -> None:
        self.assertEqual(MediaV1(kind="photo").to_dict(), {"kind": "photo"})


if __name__ == "__main__":
    unittest.main()
