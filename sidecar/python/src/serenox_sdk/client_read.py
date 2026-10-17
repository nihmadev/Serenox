"""Typed, bounded read-only Telegram client groups."""

from __future__ import annotations

import dataclasses
from collections.abc import Mapping, Sequence
from typing import Any

from .protocol import JSONValue, OperationKind, OperationV1


@dataclasses.dataclass(frozen=True, slots=True)
class Page:
    items: Sequence[Mapping[str, JSONValue]]
    cursor: str = ""
    has_more: bool = False
    total_hint: int = 0
    snapshot_unix_ms: int = 0


def _object(value: JSONValue) -> dict[str, JSONValue]:
    return dict(value) if isinstance(value, Mapping) else {}


class ReadClient:
    def __init__(self, sink: Any) -> None:
        self._sink = sink
        self.identity = IdentityReads(self)
        self.peers = PeerReads(self)
        self.users = UserReads(self)
        self.chats = ChatReads(self)
        self.messages = MessageReads(self)
        self.history = HistoryReads(self)
        self.search = SearchReads(self)
        self.dialogs = DialogReads(self)
        self.participants = ParticipantReads(self)
        self.media_metadata = MediaMetadataReads(self)

    async def _call(
        self, kind: OperationKind, target: Mapping[str, JSONValue], **parameters: JSONValue
    ) -> dict[str, JSONValue]:
        value = await self._sink.request(
            OperationV1(kind=kind, target=target, parameters=parameters)
        )
        return _object(value)

    @staticmethod
    def _page(result: Mapping[str, JSONValue], field: str) -> Page:
        raw = result.get("page")
        page = _object(raw)
        items = result.get(field, [])
        return Page(
            items=tuple(item for item in items if isinstance(item, Mapping))
            if isinstance(items, list)
            else (),
            cursor=str(page.get("cursor", "")),
            has_more=bool(page.get("has_more", False)),
            total_hint=int(page.get("total_hint", 0) or 0),
            snapshot_unix_ms=int(page.get("snapshot_unix_ms", 0) or 0),
        )


class IdentityReads:
    def __init__(self, client: ReadClient) -> None:
        self._client = client

    async def get_me(self) -> Mapping[str, JSONValue]:
        return await self._client._call(OperationKind.GET_ME, {})


class PeerReads:
    def __init__(self, client: ReadClient) -> None:
        self._client = client

    async def resolve(self, peer: int | str) -> Mapping[str, JSONValue]:
        target = {"peer": peer}
        return await self._client._call(OperationKind.RESOLVE_PEER, target)

    async def search_public_username(self, username: str) -> Mapping[str, JSONValue]:
        return await self._client._call(OperationKind.SEARCH_PUBLIC_USERNAME, {"peer": username})


class UserReads:
    def __init__(self, client: ReadClient) -> None:
        self._client = client

    async def get(self, user_id: int) -> Mapping[str, JSONValue]:
        return await self._client._call(OperationKind.GET_USER, {"peer_id": user_id})

    async def get_many(self, user_ids: Sequence[int]) -> Sequence[Mapping[str, JSONValue]]:
        return (
            await self._client._call(OperationKind.GET_USERS, {"user_ids": list(user_ids)})
        ).get("users", [])  # type: ignore[return-value]


class ChatReads:
    def __init__(self, client: ReadClient) -> None:
        self._client = client

    async def get(self, chat_id: int) -> Mapping[str, JSONValue]:
        return await self._client._call(OperationKind.GET_CHAT, {"peer_id": chat_id})

    async def get_many(self, chat_ids: Sequence[int]) -> Sequence[Mapping[str, JSONValue]]:
        return (
            await self._client._call(OperationKind.GET_CHATS, {"chat_ids": list(chat_ids)})
        ).get("chats", [])  # type: ignore[return-value]


class MessageReads:
    def __init__(self, client: ReadClient) -> None:
        self._client = client

    async def get(self, peer_id: int, message_id: int) -> Mapping[str, JSONValue] | None:
        result = await self._client._call(
            OperationKind.GET_MESSAGE, {"peer_id": peer_id, "message_id": message_id}
        )
        value = result.get("message")
        return value if isinstance(value, Mapping) else None

    async def get_many(
        self, peer_id: int, message_ids: Sequence[int]
    ) -> Sequence[Mapping[str, JSONValue]]:
        return (
            await self._client._call(
                OperationKind.GET_MESSAGES, {"peer_id": peer_id, "message_ids": list(message_ids)}
            )
        ).get("messages", [])  # type: ignore[return-value]


class HistoryReads:
    def __init__(self, client: ReadClient) -> None:
        self._client = client

    async def list(self, peer_id: int, *, limit: int = 100, cursor: str = "") -> Page:
        return self._client._page(
            await self._client._call(
                OperationKind.GET_HISTORY,
                {"peer_id": peer_id},
                limit=limit,
                cursor=cursor,
                direction="older",
            ),
            "messages",
        )

    async def around(
        self, peer_id: int, message_id: int, *, before: int = 20, after: int = 20
    ) -> Page:
        return self._client._page(
            await self._client._call(
                OperationKind.GET_HISTORY_AROUND,
                {"peer_id": peer_id, "message_id": message_id},
                before=before,
                after=after,
            ),
            "messages",
        )


class SearchReads:
    def __init__(self, client: ReadClient) -> None:
        self._client = client

    async def messages(
        self, peer_id: int, query: str, *, limit: int = 100, cursor: str = ""
    ) -> Page:
        return self._client._page(
            await self._client._call(
                OperationKind.SEARCH_MESSAGES,
                {"peer_id": peer_id},
                query=query,
                limit=limit,
                cursor=cursor,
            ),
            "messages",
        )

    async def by_sender(
        self, peer_id: int, sender_id: int, *, query: str = "", limit: int = 100, cursor: str = ""
    ) -> Page:
        return self._client._page(
            await self._client._call(
                OperationKind.SEARCH_MESSAGES_BY_SENDER,
                {"peer_id": peer_id, "sender_id": sender_id},
                query=query,
                limit=limit,
                cursor=cursor,
            ),
            "messages",
        )

    async def by_media(
        self, peer_id: int, media_kind: str, *, limit: int = 100, cursor: str = ""
    ) -> Page:
        return self._client._page(
            await self._client._call(
                OperationKind.SEARCH_MESSAGES_BY_MEDIA,
                {"peer_id": peer_id},
                media_kind=media_kind,
                limit=limit,
                cursor=cursor,
            ),
            "messages",
        )


class DialogReads:
    def __init__(self, client: ReadClient) -> None:
        self._client = client

    async def list(self, *, folder_id: int = 0, limit: int = 100, cursor: str = "") -> Page:
        return self._client._page(
            await self._client._call(
                OperationKind.LIST_DIALOGS, {}, folder_id=folder_id, limit=limit, cursor=cursor
            ),
            "dialogs",
        )


class ParticipantReads:
    def __init__(self, client: ReadClient) -> None:
        self._client = client

    async def list(self, chat_id: int, *, limit: int = 100, cursor: str = "") -> Page:
        return self._client._page(
            await self._client._call(
                OperationKind.LIST_PARTICIPANTS, {"peer_id": chat_id}, limit=limit, cursor=cursor
            ),
            "participants",
        )

    async def search(self, chat_id: int, query: str, *, limit: int = 100, cursor: str = "") -> Page:
        return self._client._page(
            await self._client._call(
                OperationKind.SEARCH_PARTICIPANTS,
                {"peer_id": chat_id},
                query=query,
                limit=limit,
                cursor=cursor,
            ),
            "participants",
        )


class MediaMetadataReads:
    def __init__(self, client: ReadClient) -> None:
        self._client = client

    async def inspect(self, handle: str) -> Mapping[str, JSONValue]:
        return await self._client._call(OperationKind.INSPECT_MEDIA, {"handle": handle})


__all__ = ["Page", "ReadClient"]
