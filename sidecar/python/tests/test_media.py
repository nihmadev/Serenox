import io
import unittest

from serenox_sdk.actions import MediaClient
from serenox_sdk.protocol import MediaOperation, MediaRequestV1


class Sink:
    async def request(self, action):
        return {"operation": action.kind.value}

    def notify(self, action):
        raise AssertionError("not used")


class MediaTests(unittest.IsolatedAsyncioTestCase):
    async def test_protocol_and_stream_type_rejections(self):
        self.assertEqual(
            MediaRequestV1(MediaOperation.INSPECT, handle="m1_x").to_dict()["handle"], "m1_x"
        )
        client = MediaClient(Sink())
        with self.assertRaises(TypeError):
            await client.upload(b"unbounded", kind="document", size=9)
        with self.assertRaises(TypeError):
            await client.download("m1_x", "/tmp/escape", size=1)
        self.assertEqual((await client.inspect("m1_x"))["operation"], "media.inspect")
        self.assertTrue(hasattr(io.BytesIO(), "read"))


if __name__ == "__main__":
    unittest.main()
