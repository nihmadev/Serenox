"""Finite compatibility helpers with no Telegram-library dependency."""

from __future__ import annotations

import html
import shlex
from typing import Any

from .actions import Message
from .errors import UnsupportedAPIError


async def answer(message: Message, response: str, **kwargs: Any) -> Message:
    """Edit an outgoing message or reply to an incoming message."""

    allowed = {"parse_mode", "reply_markup"}
    unsupported = set(kwargs).difference(allowed)
    if unsupported:
        name = sorted(unsupported)[0]
        raise UnsupportedAPIError(f"utils.answer(..., {name}=...)")
    return await message.answer(response, **kwargs)


def get_args_raw(message: Message) -> str:
    text = message.text.strip()
    if not text:
        return ""
    parts = text.split(maxsplit=1)
    return parts[1] if len(parts) == 2 else ""


def get_args(message: Message) -> list[str]:
    raw = get_args_raw(message)
    if not raw:
        return []
    try:
        return shlex.split(raw)
    except ValueError:
        return raw.split()


def get_chat_id(message: Message) -> int:
    return message.chat_id


def escape_html(value: Any) -> str:
    return html.escape(str(value), quote=False)


def __getattr__(name: str) -> Any:
    raise UnsupportedAPIError(
        f"utils.{name}", "utils.answer/get_args/get_args_raw/get_chat_id/escape_html"
    )


__all__ = ["answer", "escape_html", "get_args", "get_args_raw", "get_chat_id"]
