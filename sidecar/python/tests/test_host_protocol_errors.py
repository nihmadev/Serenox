from __future__ import annotations

import unittest

from serenox_sdk.errors import ActionError
from serenox_sidecar.host_protocol import HostProtocolMixin


class _ProtocolHarness(HostProtocolMixin):
    def _ensure_response_size(self, response: object) -> None:
        del response
        return None


class HostProtocolErrorTests(unittest.TestCase):
    def test_action_error_keeps_moderation_code_and_details(self) -> None:
        response = _ProtocolHarness()._exception_response(
            {
                "request_id": 7,
                "deadline_unix_ms": 99,
            },
            ActionError(
                "Telegram denied channels.editBanned",
                action_code="moderation_right_required",
                operation="members.restrict",
                details={
                    "telegram_stage": "channels.editBanned",
                    "required_right": "ban_users",
                },
            ),
        )

        error = response.get("error")
        self.assertIsInstance(error, dict)
        assert isinstance(error, dict)
        self.assertEqual(error.get("code"), "moderation_right_required")
        self.assertEqual(error.get("operation"), "members.restrict")
        details = error.get("details")
        self.assertIsInstance(details, dict)
        assert isinstance(details, dict)
        self.assertEqual(details.get("required_right"), "ban_users")


if __name__ == "__main__":
    unittest.main()
