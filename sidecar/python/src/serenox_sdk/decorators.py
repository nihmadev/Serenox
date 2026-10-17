"""Hikka-style decorators backed by explicit protocol handler metadata."""

from __future__ import annotations

import dataclasses
from collections.abc import Callable, Mapping
from typing import Any, TypeVar, overload

from .errors import ProtocolError
from .protocol import HandlerKind, JSONValue, ensure_json_value

F = TypeVar("F", bound=Callable[..., Any])


@dataclasses.dataclass(frozen=True, slots=True)
class JobMetadata:
    name: str | None
    description: str
    persistent: bool
    max_runtime_ms: int
    overlap: str


def job(
    *,
    name: str | None = None,
    description: str = "",
    persistent: bool = True,
    max_runtime_ms: int = 30_000,
    overlap: str = "skip_if_running",
) -> Callable[[F], F]:
    if max_runtime_ms < 1 or max_runtime_ms > 86_400_000:
        raise ValueError("max_runtime_ms is outside bounds")
    if overlap not in {
        "skip_if_running",
        "coalesce_one_pending",
        "queue_one_bounded",
        "reject_when_plugin_queue_full",
    }:
        raise ValueError("unknown job overlap policy")

    def apply(function: F) -> F:
        function.__dict__["__serenox_job__"] = JobMetadata(
            name, description, persistent, max_runtime_ms, overlap
        )
        return function

    return apply


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


def control_command(
    *,
    name: str | None = None,
    aliases: tuple[str, ...] | list[str] = (),
    surfaces: tuple[str, ...] | list[str] = ("inline",),
    response_mode: str = "inline_result",
    actor_policy: str = "owner_only",
    allowed_actor_ids: tuple[int, ...] | list[int] = (),
    mutating: bool = False,
    require_confirmation: bool = False,
    confirmation_timeout_ms: int = 0,
    allow_query_execution: bool = False,
    max_argument_bytes: int = 0,
    max_state_bytes: int = 0,
    timeout_ms: int = 10_000,
) -> Callable[[F], F]:
    """Declare an opt-in control-bot command, never a userbot command.

    The declaration is emitted during sidecar metadata handshake and is checked
    by the core against the package's ``control_bot`` capability.
    """
    if mutating and allow_query_execution:
        raise ValueError("mutating control commands cannot execute while an inline query is typed")
    declaration: dict[str, JSONValue] = {
        "surfaces": ensure_json_value(list(surfaces), field="control.surfaces"),
        "response_mode": response_mode,
        "actor_policy": actor_policy,
        "allowed_actor_ids": ensure_json_value(
            list(allowed_actor_ids), field="control.allowed_actor_ids"
        ),
        "mutating": mutating,
        "require_confirmation": require_confirmation,
        "confirmation_timeout_ms": confirmation_timeout_ms,
        "allow_query_execution": allow_query_execution,
        "max_argument_bytes": max_argument_bytes,
        "max_state_bytes": max_state_bytes,
    }
    return _decorate(
        HandlerKind.COMMAND,
        name=name,
        aliases=aliases,
        timeout_ms=timeout_ms,
        control_command=declaration,
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


def pre_send(
    function: F | None = None,
    *,
    name: str | None = None,
    timeout_ms: int = 10_000,
) -> F | Callable[[F], F]:
    """Register a synchronous transformation for outbound send actions."""

    decorator = _decorate(HandlerKind.PRE_SEND, name=name, timeout_ms=timeout_ms)
    if function is not None:
        return decorator(function)
    return decorator


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
    "control_command",
    "get_handler_metadata",
    "group_admin",
    "group_member",
    "group_owner",
    "inline_handler",
    "owner",
    "pm",
    "pre_send",
    "sudo",
    "support",
    "tds",
    "unrestricted",
    "watcher",
]
