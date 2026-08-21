"""Hikka-style decorators backed by explicit protocol handler metadata."""

from __future__ import annotations

import dataclasses
from collections.abc import Callable, Mapping
from typing import Any, TypeVar, overload

from .errors import ProtocolError
from .protocol import HandlerKind, JSONValue, ensure_json_value

F = TypeVar("F", bound=Callable[..., Any])


@dataclasses.dataclass(frozen=True, slots=True)
class HandlerMetadata:
    kind: HandlerKind
    name: str | None
    filters: Mapping[str, JSONValue]
    aliases: tuple[str, ...]
    timeout_ms: int


def _decorate(
    kind: HandlerKind,
    *tags: str,
    name: str | None = None,
    aliases: tuple[str, ...] | list[str] = (),
    timeout_ms: int = 10_000,
    **filters: Any,
) -> Callable[[F], F]:
    if timeout_ms < 1 or timeout_ms > 300_000:
        raise ValueError("timeout_ms must be between 1 and 300000")
    normalized: dict[str, JSONValue] = {}
    for tag in tags:
        if not isinstance(tag, str) or not tag:
            raise TypeError("handler decorator tags must be non-empty strings")
        normalized[tag] = True
    for key, value in filters.items():
        normalized[key] = ensure_json_value(value, field=f"filter.{key}")
    normalized_aliases = tuple(str(alias).lower() for alias in aliases)
    for alias in normalized_aliases:
        if (
            not alias
            or len(alias) > 128
            or not all(
                character.isascii() and (character.isalnum() or character in "_.-")
                for character in alias
            )
        ):
            raise ValueError(f"invalid command alias {alias!r}")

    def apply(function: F) -> F:
        metadata = HandlerMetadata(
            kind=kind,
            name=name,
            filters=normalized,
            aliases=normalized_aliases,
            timeout_ms=timeout_ms,
        )
        function.__dict__["__serenox_handler__"] = metadata
        setattr(
            function,
            f"is_{kind.value}_handler" if kind is HandlerKind.INLINE else f"is_{kind.value}",
            True,
        )
        for key, value in normalized.items():
            setattr(function, key, value)
        return function

    return apply


@overload
def command(function: F, /) -> F: ...


@overload
def command(
    *tags: str,
    name: str | None = None,
    aliases: tuple[str, ...] | list[str] = (),
    timeout_ms: int = 10_000,
    **filters: Any,
) -> Callable[[F], F]: ...


def command(
    *tags: str | F,
    name: str | None = None,
    aliases: tuple[str, ...] | list[str] = (),
    timeout_ms: int = 10_000,
    **filters: Any,
) -> F | Callable[[F], F]:
    """Mark a method as a command; usable as ``@command`` or ``@command()``."""

    if len(tags) == 1 and callable(tags[0]) and name is None and not filters:
        return _decorate(HandlerKind.COMMAND)(tags[0])
    if any(not isinstance(tag, str) for tag in tags):
        raise TypeError("command positional arguments must be filter tag strings")
    return _decorate(
        HandlerKind.COMMAND,
        *(str(tag) for tag in tags),
        name=name,
        aliases=aliases,
        timeout_ms=timeout_ms,
        **filters,
    )


def watcher(
    *tags: str | F,
    name: str | None = None,
    timeout_ms: int = 10_000,
    **filters: Any,
) -> F | Callable[[F], F]:
    """Mark a method as a watcher and publish core-evaluated filters."""

    if len(tags) == 1 and callable(tags[0]) and name is None and not filters:
        return _decorate(HandlerKind.WATCHER)(tags[0])
    if any(not isinstance(tag, str) for tag in tags):
        raise TypeError("watcher positional arguments must be filter tag strings")
    return _decorate(
        HandlerKind.WATCHER,
        *(str(tag) for tag in tags),
        name=name,
        timeout_ms=timeout_ms,
        **filters,
    )


def inline_handler(
    *tags: str | F,
    name: str | None = None,
    aliases: tuple[str, ...] | list[str] = (),
    timeout_ms: int = 10_000,
    **filters: Any,
) -> F | Callable[[F], F]:
    """Mark a method as an inline-query handler."""

    if len(tags) == 1 and callable(tags[0]) and name is None and not filters:
        return _decorate(HandlerKind.INLINE)(tags[0])
    if any(not isinstance(tag, str) for tag in tags):
        raise TypeError("inline_handler positional arguments must be filter tag strings")
    return _decorate(
        HandlerKind.INLINE,
        *(str(tag) for tag in tags),
        name=name,
        aliases=aliases,
        timeout_ms=timeout_ms,
        **filters,
    )


def callback_handler(
    *tags: str | F,
    name: str | None = None,
    timeout_ms: int = 10_000,
    **filters: Any,
) -> F | Callable[[F], F]:
    """Mark a method as an inline callback handler."""

    if len(tags) == 1 and callable(tags[0]) and name is None and not filters:
        return _decorate(HandlerKind.CALLBACK)(tags[0])
    if any(not isinstance(tag, str) for tag in tags):
        raise TypeError("callback_handler positional arguments must be filter tag strings")
    return _decorate(
        HandlerKind.CALLBACK,
        *(str(tag) for tag in tags),
        name=name,
        timeout_ms=timeout_ms,
        **filters,
    )


def tds(cls: type[Any]) -> type[Any]:
    """Compatibility no-op for the legacy translation-docstring decorator."""

    return cls


def identity_decorator(
    function: F | None = None, *args: Any, **kwargs: Any
) -> F | Callable[[F], F]:
    """Preserve legacy security decorators; authorization stays in the core."""

    del args, kwargs
    if function is not None and callable(function):
        return function

    def apply(inner: F) -> F:
        return inner

    return apply


def owner(function: F | None = None, *args: Any, **kwargs: Any) -> F | Callable[[F], F]:
    """Mark a command as core-enforced owner-only authorization."""

    del args, kwargs

    def apply(inner: F) -> F:
        inner.__dict__["__serenox_owner_only__"] = True
        return inner

    return apply(function) if function is not None and callable(function) else apply


sudo = identity_decorator
support = identity_decorator
unrestricted = identity_decorator
group_owner = identity_decorator
group_admin = identity_decorator
group_member = identity_decorator
pm = identity_decorator


def get_handler_metadata(function: Callable[..., Any]) -> HandlerMetadata | None:
    value = getattr(function, "__serenox_handler__", None)
    if value is None:
        return None
    if not isinstance(value, HandlerMetadata):
        raise ProtocolError("invalid __serenox_handler__ metadata attached to method")
    return value


__all__ = [
    "HandlerMetadata",
    "callback_handler",
    "command",
    "get_handler_metadata",
    "group_admin",
    "group_member",
    "group_owner",
    "inline_handler",
    "owner",
    "pm",
    "sudo",
    "support",
    "tds",
    "unrestricted",
    "watcher",
]
