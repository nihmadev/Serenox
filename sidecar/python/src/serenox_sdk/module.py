"""Module lifecycle base class and deterministic handler discovery."""

from __future__ import annotations

import dataclasses
import inspect
import time
from collections.abc import Callable, Iterator, Mapping, Sequence
from typing import Any

from .actions import ActionSink, CallbackRegistry, ClientProxy, HTTPClient, InlineProxy, Message
from .config import ModuleConfig
from .decorators import HandlerMetadata, JobMetadata, get_handler_metadata
from .errors import UnsupportedAPIError
from .events import EventsProxy
from .jobs import JobsProxy
from .protocol import (
    HandlerKind,
    HandlerSpecV1,
    JSONValue,
    OperationKind,
    OperationV1,
)
from .storage import DatabaseProxy, ModuleStorage


class StringsProxy(Mapping[str, str]):
    """Mapping that also supports legacy ``self.strings("key")`` calls."""

    def __init__(self, values: Mapping[str, Any]) -> None:
        self._values = {str(key): str(value) for key, value in values.items()}

    def __getitem__(self, key: str) -> str:
        return self._values[key]

    def __iter__(self) -> Iterator[str]:
        return iter(self._values)

    def __len__(self) -> int:
        return len(self._values)

    def __call__(self, key: str, message: Any = None) -> str:
        del message
        return self._values.get(key, key)


@dataclasses.dataclass(slots=True)
class RuntimeContext:
    sink: ActionSink
    database: DatabaseProxy
    callbacks: CallbackRegistry
    http_request: Callable[..., Any] | None = None
    capabilities: frozenset[str] = frozenset()
    prefix: str = "."
    account_id: int = 0
    clock: Callable[[], float] = time.monotonic


class Module:
    """Small process-safe subset of the legacy Hikka Module API."""

    strings: Mapping[str, str] = {"name": "Unknown"}
    version = "0.0.0"
    author = ""
    description = ""

    async def client_ready(self) -> None:
        """Called once after validated configuration is applied."""

    async def on_unload(self) -> None:
        """Called once during a clean sidecar stop."""

    def config_complete(self) -> None:
        """Called after validated configuration is installed."""

    def _bind(self, context: RuntimeContext) -> None:
        self._runtime = context
        namespace = f"python.{type(self).__module__}.{type(self).__name__}"
        self._module_storage = ModuleStorage(context.database, namespace)
        self.db = context.database
        self._db = context.database
        self.client = ClientProxy(context.sink)
        self.moderation = self.client.moderation
        self._client = self.client
        self.http = (
            HTTPClient(context.http_request, context.capabilities)
            if context.http_request is not None
            else None
        )
        self.jobs = JobsProxy(context.sink)
        self.events = EventsProxy(context.sink)
        self.inline = InlineProxy(context.sink, context.callbacks, clock=context.clock)
        self.tg_id = context.account_id
        self._tg_id = context.account_id
        current_config = getattr(self, "config", None)
        if current_config is None:
            class_config = getattr(type(self), "config", None)
            current_config = (
                class_config.clone() if isinstance(class_config, ModuleConfig) else ModuleConfig()
            )
        elif isinstance(current_config, ModuleConfig):
            current_config = current_config.clone()
        else:
            raise TypeError("module.config must be a ModuleConfig")
        self.config = current_config
        current_strings = getattr(type(self), "strings", {"name": type(self).__name__})
        self.strings = StringsProxy(current_strings)

    def get(self, key: str, default: Any = None) -> Any:
        self._require_bound("Module.get")
        # A synchronous call cannot safely block the sidecar event loop while
        # waiting for a reverse RPC. Returning the startup cache here would
        # make a durable value look current after a restart, so fail clearly.
        del key, default
        raise UnsupportedAPIError("Module.get", "await Module.async_get for a durable read")

    def set(self, key: str, value: Any) -> bool:
        """Update the compatibility cache and enqueue an async action.

        This method does not certify a durable commit; use ``async_set`` when
        the caller needs the core's atomic commit guarantee.
        """
        self._require_bound("Module.set")
        return self._module_storage.set(key, value)

    async def async_get(self, key: str, default: Any = None) -> Any:
        self._require_bound("Module.async_get")
        return await self._module_storage.async_get(key, default)

    async def async_set(self, key: str, value: Any) -> bool:
        self._require_bound("Module.async_set")
        return await self._module_storage.async_set(key, value)

    async def async_delete(self, key: str) -> bool:
        self._require_bound("Module.async_delete")
        return await self._module_storage.async_delete(key)

    def get_prefix(self) -> str:
        self._require_bound("Module.get_prefix")
        return self._runtime.prefix

    async def invoke(
        self,
        command: str,
        args: str | None = None,
        peer: int | None = None,
        message: Message | None = None,
        edit: bool = False,
    ) -> Message:
        self._require_bound("Module.invoke")
        if peer is None and message is None:
            raise ValueError("invoke requires peer or message")
        target: dict[str, JSONValue] = {}
        if message is not None:
            target = {"chat_id": message.chat_id, "message_id": message.id, "edit": edit}
        elif peer is not None:
            target = {"chat_id": peer}
        value = await self._runtime.sink.request(
            OperationV1(
                kind=OperationKind.INVOKE_COMMAND,
                target=target,
                parameters={"command": command, "arguments": args or ""},
            )
        )
        if isinstance(value, Mapping):
            message_value = value.get("message", value)
            if isinstance(message_value, Mapping):
                return Message.from_dict(message_value, self._runtime.sink)
        if message is not None:
            return message
        return Message.from_dict(
            {"id": 0, "chat_id": peer, "text": f"{self.get_prefix()}{command} {args or ''}"},
            self._runtime.sink,
        )

    def lookup(self, name: str) -> Any:
        del name
        raise UnsupportedAPIError(
            "Module.lookup", "a declared RPC command/action instead of cross-module Python objects"
        )

    def pointer(self, *args: Any, **kwargs: Any) -> Any:
        del args, kwargs
        raise UnsupportedAPIError("Module.pointer", "Module.get/set with an immutable JSON value")

    def _require_bound(self, api: str) -> None:
        if not hasattr(self, "_runtime"):
            raise RuntimeError(f"{api} is unavailable before the sidecar binds the module")


@dataclasses.dataclass(frozen=True, slots=True)
class RegisteredHandler:
    spec: HandlerSpecV1
    callback: Callable[..., Any]
    metadata: HandlerMetadata | None


@dataclasses.dataclass(frozen=True, slots=True)
class RegistryMetadata:
    """Sidecar-local metadata used to build the exact wire metadata DTO."""

    id: str
    name: str
    version: str
    description: str
    author: str
    handlers: tuple[HandlerSpecV1, ...]
    capabilities: tuple[str, ...]
    jobs: tuple[Mapping[str, JSONValue], ...] = ()
    event_publications: tuple[Mapping[str, JSONValue], ...] = ()
    event_subscriptions: tuple[Mapping[str, JSONValue], ...] = ()


class ModuleRegistry:
    """Immutable registry built once before the socket handshake."""

    MAX_HANDLERS = 256

    def __init__(
        self,
        module: Module,
        *,
        instance_id: str = "",
        allowed_capabilities: Sequence[str] | None = None,
    ) -> None:
        del instance_id  # Runtime instance identity is not stable plugin metadata.
        self.module = module
        self.allowed_capabilities = (
            None if allowed_capabilities is None else frozenset(allowed_capabilities)
        )
        self._handlers: dict[tuple[HandlerKind, str], RegisteredHandler] = {}
        self.job_handlers: dict[str, tuple[Callable[..., Any], JobMetadata]] = {}
        self.event_handlers: dict[str, Callable[..., Any]] = {}
        self._discover()
        if len(self._handlers) > self.MAX_HANDLERS:
            raise ValueError(f"module declares more than {self.MAX_HANDLERS} handlers")
        name = (
            module.strings("name")
            if isinstance(module.strings, StringsProxy)
            else str(module.strings.get("name", type(module).__name__))
        )
        plugin_id = str(
            getattr(module, "id", "") or f"python.{type(module).__module__}.{type(module).__name__}"
        ).lower()
        plugin_id = "".join(
            character
            if character.isascii() and (character.isalnum() or character in "_.-")
            else "-"
            for character in plugin_id
        )[:128]
        jobs = tuple(
            self._job_wire(name, meta) for name, (_, meta) in sorted(self.job_handlers.items())
        )
        self.metadata = RegistryMetadata(
            id=plugin_id,
            name=name,
            version=str(getattr(module, "version", "0.0.0")),
            description=str(
                getattr(module, "description", "") or inspect.getdoc(type(module)) or ""
            ),
            author=str(getattr(module, "author", "")),
            handlers=tuple(handler.spec for handler in self._handlers.values()),
            capabilities=self._capabilities(),
            jobs=jobs,
            event_publications=tuple(
                item.to_dict() for item in getattr(module, "event_publications", ())
            ),
            event_subscriptions=tuple(
                item.to_dict() for item in getattr(module, "event_subscriptions", ())
            ),
        )

    def _discover(self) -> None:
        for method_name, callback in inspect.getmembers(self.module, predicate=callable):
            if method_name.startswith("_"):
                continue
            metadata = get_handler_metadata(callback)
            job_metadata = getattr(callback, "__serenox_job__", None)
            event_name = getattr(callback, "__serenox_event_handler__", None)
            if event_name is not None:
                if event_name in self.event_handlers:
                    raise ValueError(f"duplicate event handler {event_name!r}")
                self.event_handlers[event_name] = callback
            if isinstance(job_metadata, JobMetadata):
                name = (job_metadata.name or method_name).lower()
                if name in self.job_handlers:
                    raise ValueError(f"duplicate job handler {name!r}")
                self.job_handlers[name] = (callback, job_metadata)
            kind: HandlerKind | None = metadata.kind if metadata else None
            handler_name = metadata.name if metadata else None
            if kind is None:
                if method_name.endswith("cmd") and len(method_name) > 3:
                    kind = HandlerKind.COMMAND
                    handler_name = method_name[:-3]
                elif method_name.endswith("_inline_handler"):
                    kind = HandlerKind.INLINE
                    handler_name = method_name.removesuffix("_inline_handler")
                elif method_name.endswith("_callback_handler"):
                    kind = HandlerKind.CALLBACK
                    handler_name = method_name.removesuffix("_callback_handler")
                elif method_name == "watcher":
                    kind = HandlerKind.WATCHER
                    handler_name = "watcher"
            if kind is None:
                continue
            if not handler_name:
                handler_name = (
                    method_name[:-3]
                    if kind is HandlerKind.COMMAND and method_name.endswith("cmd")
                    else method_name
                )
            normalized_name = handler_name.lower()
            if (
                not normalized_name
                or len(normalized_name) > 128
                or not all(
                    character.isascii() and (character.isalnum() or character in "_.-")
                    for character in normalized_name
                )
            ):
                raise ValueError(f"invalid handler name {handler_name!r}")
            key = (kind, normalized_name)
            if key in self._handlers:
                raise ValueError(f"duplicate {kind.value} handler {normalized_name!r}")
            filters = metadata.filters if metadata else {}
            aliases = metadata.aliases if metadata else ()
            timeout_ms = metadata.timeout_ms if metadata else 10_000
            spec = HandlerSpecV1(
                kind=kind,
                name=normalized_name,
                method=method_name,
                filters=filters,
                aliases=aliases,
                timeout_ms=timeout_ms,
            )
            self._handlers[key] = RegisteredHandler(spec, callback, metadata)

    def find(self, kind: HandlerKind, name: str) -> RegisteredHandler | None:
        normalized = name.lower()
        direct = self._handlers.get((kind, normalized))
        if direct is not None:
            return direct
        if kind is HandlerKind.COMMAND:
            for handler in self._handlers.values():
                if handler.spec.kind is kind and normalized in handler.spec.aliases:
                    return handler
        return None

    def by_kind(self, kind: HandlerKind) -> tuple[RegisteredHandler, ...]:
        return tuple(handler for handler in self._handlers.values() if handler.spec.kind is kind)

    def _capabilities(self) -> tuple[str, ...]:
        required: set[str] = set()
        if self.job_handlers:
            required.add("jobs")
        if getattr(self.module, "event_publications", ()):
            required.add("events.publish")
        if getattr(self.module, "event_subscriptions", ()):
            required.add("events.subscribe")
        for handler in self._handlers.values():
            if handler.spec.kind is HandlerKind.COMMAND:
                required.add("commands")
            elif handler.spec.kind is HandlerKind.WATCHER:
                required.add("watchers")
            elif handler.spec.kind is HandlerKind.PRE_SEND:
                required.add("pre_send")
            else:
                required.add("inline")
        if self.allowed_capabilities is None:
            return tuple(sorted(required | {"storage", "client_operations"}))
        missing = required - self.allowed_capabilities
        if missing:
            raise ValueError(
                "module handlers require undeclared capabilities: " + ", ".join(sorted(missing))
            )
        return tuple(sorted(self.allowed_capabilities))

    @staticmethod
    def _job_wire(name: str, metadata: JobMetadata) -> Mapping[str, JSONValue]:
        return {
            "name": name,
            "description": metadata.description,
            "persistent": metadata.persistent,
            "max_runtime_ms": metadata.max_runtime_ms,
            "overlap": metadata.overlap,
        }


__all__ = [
    "Module",
    "ModuleRegistry",
    "RegisteredHandler",
    "RegistryMetadata",
    "RuntimeContext",
    "StringsProxy",
]
