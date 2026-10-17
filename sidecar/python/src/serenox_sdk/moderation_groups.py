"""Typed moderation message, settings, join, invite, and topic groups."""

from __future__ import annotations

from collections.abc import Mapping, Sequence

from .moderation import (
    ChatTarget,
    DeniedRights,
    InviteSettings,
    Moderation,
    ModerationResult,
    ParticipantTarget,
)
from .protocol import JSONValue, OperationKind


class Messages:
    def __init__(self, client: Moderation) -> None:
        self._client = client

    async def delete(
        self, chat: ChatTarget, message_ids: Sequence[int], *, idempotency_key: str = ""
    ) -> ModerationResult:
        return await self._client._call(
            OperationKind.MOD_MESSAGES_DELETE,
            chat,
            messages=[{"chat_id": chat.chat_id, "message_id": value} for value in message_ids],
            idempotency_key=idempotency_key,
        )

    async def pin(
        self,
        chat: ChatTarget,
        message_id: int,
        *,
        silent: bool = False,
        one_side: bool = False,
        idempotency_key: str = "",
    ) -> ModerationResult:
        return await self._client._call(
            OperationKind.MOD_MESSAGES_PIN,
            chat,
            messages=[{"chat_id": chat.chat_id, "message_id": message_id}],
            silent=silent,
            one_side=one_side,
            idempotency_key=idempotency_key,
        )

    async def unpin(
        self, chat: ChatTarget, message_id: int, *, idempotency_key: str = ""
    ) -> ModerationResult:
        return await self._client._call(
            OperationKind.MOD_MESSAGES_UNPIN,
            chat,
            messages=[{"chat_id": chat.chat_id, "message_id": message_id}],
            idempotency_key=idempotency_key,
        )

    async def unpin_all(self, chat: ChatTarget, *, idempotency_key: str = "") -> ModerationResult:
        return await self._client._call(
            OperationKind.MOD_MESSAGES_UNPIN_ALL, chat, idempotency_key=idempotency_key
        )


class Settings:
    def __init__(self, client: Moderation) -> None:
        self._client = client

    async def get(self, chat: ChatTarget) -> Mapping[str, JSONValue]:
        return (await self._client._call(OperationKind.MOD_SETTINGS_GET, chat)).raw

    async def set_default_rights(
        self, chat: ChatTarget, denied: DeniedRights, *, idempotency_key: str = ""
    ) -> ModerationResult:
        return await self._client._call(
            OperationKind.MOD_SETTINGS_DEFAULT,
            chat,
            settings={"default_denied": denied.to_wire()},
            idempotency_key=idempotency_key,
        )

    async def set_slow_mode(
        self, chat: ChatTarget, seconds: int, *, idempotency_key: str = ""
    ) -> ModerationResult:
        return await self._client._call(
            OperationKind.MOD_SETTINGS_SLOW,
            chat,
            settings={"slow_mode_seconds": seconds},
            idempotency_key=idempotency_key,
        )

    async def set_protected_content(
        self, chat: ChatTarget, enabled: bool, *, idempotency_key: str = ""
    ) -> ModerationResult:
        return await self._client._call(
            OperationKind.MOD_SETTINGS_PROTECTED,
            chat,
            settings={"protected_content": enabled},
            idempotency_key=idempotency_key,
        )


class JoinRequests:
    def __init__(self, client: Moderation) -> None:
        self._client = client

    async def list(
        self, chat: ChatTarget, *, query: str = "", limit: int = 100, cursor: str = ""
    ) -> ModerationResult:
        return await self._client._call(
            OperationKind.MOD_JOIN_LIST,
            chat,
            page={"query": query, "limit": limit, "cursor": cursor},
        )

    async def decide(
        self,
        chat: ChatTarget,
        target: ParticipantTarget,
        approve: bool,
        *,
        idempotency_key: str = "",
    ) -> ModerationResult:
        return await self._client._call(
            OperationKind.MOD_JOIN_APPROVE if approve else OperationKind.MOD_JOIN_DECLINE,
            chat,
            participant=target,
            idempotency_key=idempotency_key,
        )


class Invites:
    def __init__(self, client: Moderation) -> None:
        self._client = client

    async def create(
        self, chat: ChatTarget, settings: InviteSettings, *, idempotency_key: str = ""
    ) -> ModerationResult:
        return await self._client._call(
            OperationKind.MOD_INVITES_CREATE,
            chat,
            invite=settings.to_wire(),
            idempotency_key=idempotency_key,
        )

    async def list(
        self, chat: ChatTarget, *, limit: int = 100, cursor: str = ""
    ) -> ModerationResult:
        return await self._client._call(
            OperationKind.MOD_INVITES_LIST, chat, page={"limit": limit, "cursor": cursor}
        )

    async def edit(
        self, chat: ChatTarget, settings: InviteSettings, *, idempotency_key: str = ""
    ) -> ModerationResult:
        return await self._client._call(
            OperationKind.MOD_INVITES_EDIT,
            chat,
            invite=settings.to_wire(),
            idempotency_key=idempotency_key,
        )

    async def revoke(
        self, chat: ChatTarget, link: str, *, idempotency_key: str = ""
    ) -> ModerationResult:
        return await self._client._call(
            OperationKind.MOD_INVITES_REVOKE,
            chat,
            invite=InviteSettings(link=link).to_wire(),
            idempotency_key=idempotency_key,
        )


class Topics:
    def __init__(self, client: Moderation) -> None:
        self._client = client

    async def get(self, chat: ChatTarget, topic_id: int) -> Mapping[str, JSONValue]:
        return (
            await self._client._call(
                OperationKind.MOD_TOPICS_GET, chat, topic={"topic_id": topic_id}
            )
        ).raw

    async def set_closed(
        self, chat: ChatTarget, topic_id: int, closed: bool, *, idempotency_key: str = ""
    ) -> ModerationResult:
        return await self._client._call(
            OperationKind.MOD_TOPICS_CLOSE if closed else OperationKind.MOD_TOPICS_REOPEN,
            chat,
            topic={"topic_id": topic_id},
            idempotency_key=idempotency_key,
        )

    async def set_general_hidden(
        self, chat: ChatTarget, hidden: bool, *, idempotency_key: str = ""
    ) -> ModerationResult:
        return await self._client._call(
            OperationKind.MOD_TOPICS_HIDE if hidden else OperationKind.MOD_TOPICS_UNHIDE,
            chat,
            idempotency_key=idempotency_key,
        )

    async def delete(
        self, chat: ChatTarget, topic_id: int, *, idempotency_key: str = ""
    ) -> ModerationResult:
        return await self._client._call(
            OperationKind.MOD_TOPICS_DELETE,
            chat,
            topic={"topic_id": topic_id},
            idempotency_key=idempotency_key,
        )
