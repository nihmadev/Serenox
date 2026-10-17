from __future__ import annotations

import re
from typing import ClassVar

from serenox_sdk import ConfigValue, Module, ModuleConfig, OutgoingMessage, loader, validators
from serenox_sdk.actions import Message


_MAX_TELEGRAM_TEXT = 4096
_STYLE_MARKERS = ("💖✨", "❤️💫", "💕🌟")
_STYLE_ENDINGS = ("✨💖", "💫❤️", "🌟💕")
_SPEECH_REPLACEMENTS = str.maketrans(
    {
        "r": "w",
        "R": "W",
        "l": "w",
        "L": "W",
        "р": "л",
        "Р": "Л",
    }
)
_VARIANTS = (
    ("💖✨💖✨ ", " ✨💖✨💖💖"),
    ("❤️💫❤️💫 ", " 💕🥺👉👈мррр~💫❤️"),
    ("💕🌟💕🌟 ", " 🌟💖💞💗💓💕🌟"),
)


def uwuify(text: str, *, variant: int = 0) -> str:
    """Turn a plain message into a small, deterministic meme-style UwU line."""

    clean = text.strip()
    if not clean:
        return clean
    if clean.startswith(_STYLE_MARKERS) and clean.endswith(_STYLE_ENDINGS):
        return clean

    clean = clean.translate(_SPEECH_REPLACEMENTS)
    clean = re.sub(r"!+", "!", clean)
    clean = re.sub(r"\?+", "?", clean)
    clean = re.sub(r"\.{3,}", "…", clean)
    if clean[-1] in "!?…~":
        clean += "~" if clean[-1] != "~" else ""
    else:
        clean += "~"

    prefix, suffix = _VARIANTS[variant % len(_VARIANTS)]
    # Leave room for the decorative prefix/suffix without exceeding Telegram's
    # plain-text message limit. Slicing is safe here because the SDK validates
    # the final action as UTF-8 later.
    available = _MAX_TELEGRAM_TEXT - len(prefix) - len(suffix) - 1
    return f"{prefix}{clean[:available]}{suffix}"


class UwU(Module):
    """A playful outgoing-message UwU formatter."""

    id = "example.uwu"
    version = "1.2.0"
    author = "Serenox"
    description = "Meme-style UwU formatter with an owner-controlled mode"
    strings: ClassVar[dict[str, str]] = {"name": "UwU formatter"}

    config = ModuleConfig(
        ConfigValue(
            "enabled",
            False,
            "Format outgoing text as cute UwU speech",
            validator=validators.Boolean(),
        ),
    )

    @loader.owner
    @loader.command(name="uwu", aliases=("nya",))
    async def uwu(self, message: Message) -> None:
        """Enable, disable, or inspect the formatter mode."""

        parts = message.text.strip().split()
        action = parts[1].casefold() if len(parts) > 1 else "status"
        if action in {"enable", "on", "вкл", "включить"}:
            self.config["enabled"] = True
            await message.reply("💖✨💖 UwU mode enabled~ мррр~ 💕🥺👉👈✨💖", parse_mode="plain")
            return
        if action in {"disable", "off", "выкл", "выключить"}:
            self.config["enabled"] = False
            await message.reply("❤️💫 UwU mode disabled~ 💫❤️", parse_mode="plain")
            return
        if action in {"status", "state", "статус"}:
            state = "enabled" if self.config["enabled"] else "disabled"
            await message.reply(
                f"💖✨ UwU mode: {state}. Use .uwu enable/disable~ 💕✨💖",
                parse_mode="plain",
            )
            return
        await message.reply(
            "💞✨ Usage: .uwu enable, .uwu disable, or .uwu status~ 🥺👉👈💖",
            parse_mode="plain",
        )

    @loader.pre_send(name="uwu")
    async def uwu_pre_send(self, message: OutgoingMessage) -> None:
        """Rewrite the send action before it reaches Telegram."""

        if not self.config["enabled"] or not message.text.strip():
            return
        message.text = uwuify(message.text, variant=message.chat_id % len(_VARIANTS))


__all__ = ["UwU", "uwuify"]
