"""Typed, bounded Telegram moderation groups backed by host.moderation."""

from __future__ import annotations

import dataclasses
import time
from collections.abc import Mapping
from typing import Any, Protocol

from .protocol import JSONValue, OperationKind, OperationV1


class _Sink(Protocol):
    async def request(self, action: OperationV1) -> JSONValue: ...


def _clean(value: Mapping[str, Any]) -> dict[str, JSONValue]:
    result: dict[str, JSONValue] = {}
    for key, item in value.items():
        if item is None or item == "":
            continue
        if dataclasses.is_dataclass(item):
            result[key] = _clean(dataclasses.asdict(item))
        elif isinstance(item, Mapping):
            result[key] = _clean(item)
        elif isinstance(item, (list, tuple)):
            result[key] = [
                _clean(dataclasses.asdict(part)) if dataclasses.is_dataclass(part) else part
                for part in item
            ]
        else:
            result[key] = item
    return result


@dataclasses.dataclass(frozen=True, slots=True)
class ChatTarget:
    chat_id: int
    expected_kind: str = ""

    def to_wire(self) -> dict[str, JSONValue]:
        return _clean(dataclasses.asdict(self))


@dataclasses.dataclass(frozen=True, slots=True)
class ParticipantTarget:
    user_id: int = 0
    username: str = ""
    reply: Mapping[str, JSONValue] | None = None

    @classmethod
    def user(cls, user_id: int) -> ParticipantTarget:
        return cls(user_id=user_id)

    @classmethod
    def replied_message(cls, chat_id: int, message_id: int) -> ParticipantTarget:
        return cls(reply={"peer_id": chat_id, "message_id": message_id})

    def to_wire(self) -> dict[str, JSONValue]:
        return _clean(dataclasses.asdict(self))


@dataclasses.dataclass(frozen=True, slots=True)
class DeniedRights:
    send_messages: bool | None = None
    send_media: bool | None = None
    send_photos: bool | None = None
    send_videos: bool | None = None
    send_round_videos: bool | None = None
    send_voice: bool | None = None
    send_audios: bool | None = None
    send_documents: bool | None = None
    send_stickers: bool | None = None
    send_gifs: bool | None = None
    send_games: bool | None = None
    use_inline_bots: bool | None = None
    embed_links: bool | None = None
    send_polls: bool | None = None
    change_info: bool | None = None
    invite_users: bool | None = None
    pin_messages: bool | None = None
    manage_topics: bool | None = None
    send_plain: bool | None = None

    @classmethod
    def muted(cls) -> DeniedRights:
        return cls(
            send_messages=True,
            send_media=True,
            send_photos=True,
            send_videos=True,
            send_round_videos=True,
            send_voice=True,
            send_audios=True,
            send_documents=True,
            send_stickers=True,
            send_gifs=True,
            send_games=True,
            use_inline_bots=True,
            embed_links=True,
            send_polls=True,
            send_plain=True,
        )

    def to_wire(self) -> dict[str, JSONValue]:
        return _clean(dataclasses.asdict(self))


@dataclasses.dataclass(frozen=True, slots=True)
class Restriction:
    denied: DeniedRights
    until_unix_ms: int = 0
    permanent: bool = False

    def to_wire(self) -> dict[str, JSONValue]:
        return {
            "denied": self.denied.to_wire(),
            "until_unix_ms": self.until_unix_ms,
            "permanent": self.permanent,
        }


@dataclasses.dataclass(frozen=True, slots=True)
class AdminRights:
    change_info: bool | None = None
    post_messages: bool | None = None
    edit_messages: bool | None = None
    delete_messages: bool | None = None
    ban_users: bool | None = None
    invite_users: bool | None = None
    pin_messages: bool | None = None
    add_admins: bool | None = None
    anonymous: bool | None = None
    manage_call: bool | None = None
    manage_topics: bool | None = None
    post_stories: bool | None = None
    edit_stories: bool | None = None
    delete_stories: bool | None = None

    def to_wire(self) -> dict[str, JSONValue]:
        return _clean(dataclasses.asdict(self))


@dataclasses.dataclass(frozen=True, slots=True)
class InviteSettings:
    link: str = ""
    name: str | None = None
    expire_unix_ms: int | None = None
    usage_limit: int | None = None
    require_approval: bool | None = None

    def to_wire(self) -> dict[str, JSONValue]:
        return _clean(dataclasses.asdict(self))


@dataclasses.dataclass(frozen=True, slots=True)
class ModerationResult:
    raw: Mapping[str, JSONValue]

    @property
    def status(self) -> str:
        return str(self.raw.get("status", ""))

    @property
    def acknowledged(self) -> bool:
        return bool(self.raw.get("acknowledged", False))

    @property
    def member(self) -> Mapping[str, JSONValue] | None:
        value = self.raw.get("member")
        return value if isinstance(value, Mapping) else None


class Moderation:
    def __init__(self, sink: _Sink) -> None:
        from .moderation_groups import Invites, JoinRequests, Messages, Settings, Topics

        self._sink = sink
        self.members = Members(self)
        self.admins = Admins(self)
        self.messages = Messages(self)
        self.settings = Settings(self)
        self.join_requests = JoinRequests(self)
        self.invites = Invites(self)
        self.topics = Topics(self)

    async def _call(
        self,
        kind: OperationKind,
        chat: ChatTarget,
        *,
        participant: ParticipantTarget | None = None,
        idempotency_key: str = "",
        **parameters: JSONValue,
    ) -> ModerationResult:
        target: dict[str, JSONValue] = {"chat": chat.to_wire()}
        if participant is not None:
            target["participant"] = participant.to_wire()
        if idempotency_key:
            parameters["idempotency_key"] = idempotency_key
        value = await self._sink.request(
            OperationV1(kind=kind, target=target, parameters=parameters)
        )
        if not isinstance(value, Mapping):
            raise TypeError("moderation result must be an object")
        return ModerationResult(value)


class Members:
    def __init__(self, client: Moderation) -> None:
        self._client = client

    async def get(
        self, chat: ChatTarget, target: ParticipantTarget
    ) -> Mapping[str, JSONValue] | None:
        return (
            await self._client._call(OperationKind.MOD_MEMBERS_GET, chat, participant=target)
        ).member

    async def list(
        self,
        chat: ChatTarget,
        *,
        filter: str = "recent",
        query: str = "",
        limit: int = 100,
        cursor: str = "",
    ) -> ModerationResult:
        return await self._client._call(
            OperationKind.MOD_MEMBERS_LIST,
            chat,
            page={"filter": filter, "query": query, "limit": limit, "cursor": cursor},
        )

    async def restrict(
        self,
        chat: ChatTarget,
        target: ParticipantTarget,
        restriction: Restriction,
        *,
        expected_state: str = "",
        idempotency_key: str = "",
    ) -> ModerationResult:
        return await self._client._call(
            OperationKind.MOD_MEMBERS_RESTRICT,
            chat,
            participant=target,
            restriction=restriction.to_wire(),
            expected_state=expected_state,
            idempotency_key=idempotency_key,
        )

    async def mute(
        self,
        chat: ChatTarget,
        target: ParticipantTarget,
        *,
        duration_seconds: float | None = None,
        idempotency_key: str = "",
    ) -> ModerationResult:
        restriction = Restriction(denied=DeniedRights.muted(), permanent=duration_seconds is None)
        if duration_seconds is not None:
            restriction = Restriction(
                denied=restriction.denied,
                until_unix_ms=int((time.time() + duration_seconds) * 1000),
            )
        return await self.restrict(chat, target, restriction, idempotency_key=idempotency_key)

    async def _simple(
        self, kind: OperationKind, chat: ChatTarget, target: ParticipantTarget, key: str
    ) -> ModerationResult:
        return await self._client._call(kind, chat, participant=target, idempotency_key=key)

    async def unmute(
        self,
        chat: ChatTarget,
        target: ParticipantTarget,
        *,
        expected_state: str = "",
        idempotency_key: str = "",
    ) -> ModerationResult:
        if expected_state:
            return await self._client._call(
                OperationKind.MOD_MEMBERS_UNRESTRICT,
                chat,
                participant=target,
                expected_state=expected_state,
                idempotency_key=idempotency_key,
            )
        return await self._simple(
            OperationKind.MOD_MEMBERS_UNRESTRICT, chat, target, idempotency_key
        )

    async def ban(
        self, chat: ChatTarget, target: ParticipantTarget, *, idempotency_key: str = ""
    ) -> ModerationResult:
        return await self._simple(OperationKind.MOD_MEMBERS_BAN, chat, target, idempotency_key)

    async def unban(
        self, chat: ChatTarget, target: ParticipantTarget, *, idempotency_key: str = ""
    ) -> ModerationResult:
        return await self._simple(OperationKind.MOD_MEMBERS_UNBAN, chat, target, idempotency_key)

    async def kick(
        self, chat: ChatTarget, target: ParticipantTarget, *, idempotency_key: str = ""
    ) -> ModerationResult:
        return await self._simple(OperationKind.MOD_MEMBERS_KICK, chat, target, idempotency_key)


class Admins:
    def __init__(self, client: Moderation) -> None:
        self._client = client

    async def acting_rights(self, chat: ChatTarget) -> Mapping[str, JSONValue] | None:
        return (await self._client._call(OperationKind.MOD_ADMINS_GET_ACTING, chat)).member

    async def get(
        self, chat: ChatTarget, target: ParticipantTarget
    ) -> Mapping[str, JSONValue] | None:
        return (
            await self._client._call(OperationKind.MOD_ADMINS_GET, chat, participant=target)
        ).member

    async def list(
        self, chat: ChatTarget, *, limit: int = 100, cursor: str = ""
    ) -> ModerationResult:
        return await self._client._call(
            OperationKind.MOD_ADMINS_LIST, chat, page={"limit": limit, "cursor": cursor}
        )

    async def set(
        self,
        chat: ChatTarget,
        target: ParticipantTarget,
        rights: AdminRights,
        *,
        rank: str | None = None,
        idempotency_key: str = "",
    ) -> ModerationResult:
        return await self._client._call(
            OperationKind.MOD_ADMINS_SET,
            chat,
            participant=target,
            admin={"rights": rights.to_wire(), "rank": rank},
            idempotency_key=idempotency_key,
        )

    async def demote(
        self, chat: ChatTarget, target: ParticipantTarget, *, idempotency_key: str = ""
    ) -> ModerationResult:
        return await self._client._call(
            OperationKind.MOD_ADMINS_DEMOTE,
            chat,
            participant=target,
            idempotency_key=idempotency_key,
        )


__all__ = [
    "AdminRights",
    "ChatTarget",
    "DeniedRights",
    "InviteSettings",
    "Moderation",
    "ModerationResult",
    "ParticipantTarget",
    "Restriction",
]
