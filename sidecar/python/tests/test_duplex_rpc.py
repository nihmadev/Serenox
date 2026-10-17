from __future__ import annotations

import unittest

from serenox_sdk.actions import ClientProxy
from serenox_sdk.errors import UnsupportedAPIError
from serenox_sdk.jobs import JobsProxy
from serenox_sdk.protocol import OperationKind, OperationV1
from serenox_sidecar.host_actions import HostActionSink


class DuplexSDKTests(unittest.IsolatedAsyncioTestCase):
    async def test_typed_storage_and_client_reads_use_duplex_sender(self) -> None:
        calls: list[OperationV1] = []

        async def respond(action: OperationV1):
            calls.append(action)
            if action.kind is OperationKind.STORAGE_GET:
                return {"stored": 7}
            if action.kind is OperationKind.STORAGE_SET:
                return True
            if action.kind is OperationKind.GET_ME:
                return {"id": 9, "kind": "user"}
            return {"message": {"id": 3, "chat_id": 4, "text": "hello"}}

        sink = HostActionSink(8)
        sink.set_duplex_sender(respond)
        self.assertEqual(
            await sink.request(
                OperationV1(OperationKind.STORAGE_GET, {"namespace": "n", "key": "k"})
            ),
            {"stored": 7},
        )
        client = ClientProxy(sink)
        self.assertEqual((await client.get_me())["id"], 9)
        message = await client.get_message(4, 3)
        assert message is not None
        self.assertEqual(message.text, "hello")
        self.assertEqual(
            [item.kind for item in calls],
            [OperationKind.STORAGE_GET, OperationKind.GET_ME, OperationKind.GET_MESSAGE],
        )

    async def test_read_without_negotiated_duplex_is_actionable(self) -> None:
        sink = HostActionSink(1)
        with self.assertRaisesRegex(UnsupportedAPIError, "future duplex host.client RPC"):
            await ClientProxy(sink).get_me()

    async def test_jobs_use_duplex_host_rpc_not_client_actions(self) -> None:
        calls: list[OperationV1] = []

        async def respond(action: OperationV1):
            calls.append(action)
            return {"status": {"identity": {"id": "demo"}, "state": "enabled"}}

        sink = HostActionSink(8)
        sink.set_duplex_sender(respond)
        result = await JobsProxy(sink).create(
            {
                "identity": {"id": "demo"},
                "handler": "micro_timer",
                "schedule": {"kind": "fixed_rate", "interval": {"interval_ms": 1000}},
            }
        )
        self.assertEqual(result["status"]["state"], "enabled")
        self.assertEqual([item.kind for item in calls], [OperationKind.JOB_CREATE])


if __name__ == "__main__":
    unittest.main()
