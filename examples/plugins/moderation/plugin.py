from __future__ import annotations

import re
from typing import ClassVar

from serenox_sdk import ActionError, ChatTarget, Module, ParticipantTarget, loader
from serenox_sdk.actions import Message

_DURATION = re.compile(
    r"^(?P<count>[1-9][0-9]{0,3})(?P<unit>[smhd]|[смчд])$", re.IGNORECASE
)
_MAX_SECONDS = 366 * 24 * 60 * 60


def parse_duration(token: str) -> int:
    match = _DURATION.fullmatch(token.casefold())
    if match is None:
        raise ValueError("duration must look like 30s, 15m, 1h, 2d, 15м, or 1ч")
    factors = {
        "s": 1,
        "с": 1,
        "m": 60,
        "м": 60,
        "h": 3600,
        "ч": 3600,
        "d": 86400,
        "д": 86400,
    }
    value = int(match.group("count")) * factors[match.group("unit").casefold()]
    if value < 30 or value > _MAX_SECONDS:
        raise ValueError("duration must be between 30 seconds and 366 days")
    return value


class ModerationExample(Module):
    id = "example.moderation"
    version = "1.0.0"
    description = "Safe owner-only reply moderation and outgoing no-prefix examples"
    strings: ClassVar[dict[str, str]] = {"name": "Moderation example"}

    def _targets(self, message: Message) -> tuple[ChatTarget, ParticipantTarget]:
        if message.reply_to_message_id is None:
            raise ValueError("reply to a member message")
        # Forward metadata is not a participant identity and this example does
        # not moderate from forwarded owner triggers.
        if message.forwarded:
            raise ValueError("forwarded moderation triggers are rejected")
        if message.chat_kind not in {"group", "supergroup", "megagroup"}:
            raise ValueError("moderation commands require a group")
        kind = (
            "megagroup"
            if message.chat_kind in {"supergroup", "megagroup"}
            else "basic_group"
        )
        return (
            ChatTarget(message.chat_id, expected_kind=kind),
            ParticipantTarget.replied_message(
                message.chat_id, message.reply_to_message_id
            ),
        )

    async def _notice(self, message: Message, status: str) -> None:
        try:
            await message.edit(status, parse_mode="plain")
        except ActionError as exc:
            raise ActionError(
                "moderation succeeded but editing the owner trigger failed",
                action_code="notification_failed",
                details={"moderation_status": status, "edit_code": exc.action_code},
            ) from exc

    async def _mute(self, message: Message, seconds: int) -> None:
        chat, target = self._targets(message)
        result = await self.moderation.members.mute(
            chat,
            target,
            duration_seconds=seconds,
            idempotency_key=f"mute-{message.chat_id}-{message.id}",
        )
        await self._notice(message, f"mute: {result.status}")

    async def _simple(self, message: Message, operation: str) -> None:
        chat, target = self._targets(message)
        method = {
            "unmute": self.moderation.members.unmute,
            "ban": self.moderation.members.ban,
            "kick": self.moderation.members.kick,
        }[operation]
        result = await method(
            chat,
            target,
            idempotency_key=f"{operation}-{message.chat_id}-{message.id}",
        )
        await self._notice(message, f"{operation}: {result.status}")

    @loader.owner
    @loader.command(name="mute")
    async def mute_command(self, message: Message) -> None:
        parts = message.text.strip().split()
        if len(parts) != 2:
            raise ValueError("usage: reply with .mute 1h")
        await self._mute(message, parse_duration(parts[1]))

    @loader.owner
    @loader.command(name="unmute")
    async def unmute_command(self, message: Message) -> None:
        if len(message.text.strip().split()) != 1:
            raise ValueError("usage: reply with .unmute")
        await self._simple(message, "unmute")

    @loader.owner
    @loader.command(name="ban")
    async def ban_command(self, message: Message) -> None:
        parts = message.text.strip().split()
        if len(parts) > 2 or (len(parts) == 2 and len(parts[1].encode("utf-8")) > 32):
            raise ValueError("usage: reply with .ban [bounded-reason]")
        await self._simple(message, "ban")

    @loader.owner
    @loader.command(name="kick")
    async def kick_command(self, message: Message) -> None:
        if len(message.text.strip().split()) != 1:
            raise ValueError("usage: reply with .kick")
        await self._simple(message, "kick")

    @loader.watcher("out", "only_reply", "only_groups", name="moderation_no_prefix")
    async def outgoing_no_prefix(self, message: Message) -> None:
        # Core routing admits only outgoing messages; this exact parser is a
        # second guard and intentionally reserves no words in Serenox itself.
        tokens = message.text.strip().casefold().split()
        if len(tokens) == 2 and tokens[0] in {"mute", "мут"}:
            await self._mute(message, parse_duration(tokens[1]))
        elif len(tokens) == 1 and tokens[0] in {"unmute", "размут"}:
            await self._simple(message, "unmute")
        elif (
            len(tokens) in {1, 2}
            and tokens[0] in {"ban", "бан"}
            and (len(tokens) == 1 or len(tokens[1].encode("utf-8")) <= 32)
        ):
            await self._simple(message, "ban")
        elif len(tokens) == 1 and tokens[0] in {"kick", "кик"}:
            await self._simple(message, "kick")


__all__ = ["ModerationExample", "parse_duration"]
