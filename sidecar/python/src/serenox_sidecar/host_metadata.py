"""Finite plugin metadata projection for the sidecar handshake."""

from __future__ import annotations

import inspect
from collections.abc import Mapping
from typing import Any

from serenox_sdk.errors import ProtocolError
from serenox_sdk.protocol import PROTOCOL_VERSION, HandlerKind, JSONValue, ensure_json_value

from .host_utils import int_list, wire_watcher_filter


def build_wire_metadata(host: Any) -> dict[str, JSONValue]:
    commands: list[JSONValue] = []
    control_commands: list[JSONValue] = []
    watchers: list[JSONValue] = []
    inline_handlers: list[JSONValue] = []
    callback_handlers: list[JSONValue] = []
    pre_send_handlers: list[JSONValue] = []
    for handler in host.registry.metadata.handlers:
        callback = getattr(host.module, handler.method)
        if handler.kind is HandlerKind.COMMAND:
            control = handler.filters.get("control_command")
            if isinstance(control, Mapping):
                declaration: dict[str, JSONValue] = dict(control)
                declaration["name"] = handler.name
                declaration["handler_id"] = handler.name
                if handler.aliases:
                    declaration["aliases"] = ensure_json_value(
                        list(handler.aliases), field="control.aliases"
                    )
                description = inspect.getdoc(callback) or ""
                if description:
                    declaration["description"] = description
                control_commands.append(declaration)
                continue
            command: dict[str, JSONValue] = {"name": handler.name}
            if handler.aliases:
                command["aliases"] = ensure_json_value(
                    list(handler.aliases), field="command.aliases"
                )
            description = inspect.getdoc(callback) or ""
            if description:
                command["description"] = description
            if getattr(callback, "__serenox_owner_only__", False):
                command["owner_only"] = True
            commands.append(command)
        elif handler.kind is HandlerKind.WATCHER:
            watcher: dict[str, JSONValue] = {
                "id": handler.name,
                "filter": wire_watcher_filter(handler.filters),
            }
            description = inspect.getdoc(callback) or ""
            if description:
                watcher["description"] = description
            watchers.append(watcher)
        elif handler.kind is HandlerKind.INLINE:
            inline: dict[str, JSONValue] = {"name": handler.name}
            query_prefix = handler.filters.get("query_prefix")
            if isinstance(query_prefix, str) and query_prefix:
                inline["query_prefix"] = query_prefix
            sender_ids = [value for value in int_list(handler.filters.get("from_id"))]
            if sender_ids:
                inline["sender_ids"] = sender_ids
            allowed_users = [value for value in int_list(handler.filters.get("allowed_users"))]
            if allowed_users:
                inline["allowed_users"] = allowed_users
            if getattr(callback, "__serenox_owner_only__", False):
                inline["owner_only"] = True
            description = inspect.getdoc(callback) or ""
            if description:
                inline["description"] = description
            inline_handlers.append(inline)
        elif handler.kind is HandlerKind.CALLBACK:
            callback_spec: dict[str, JSONValue] = {"name": handler.name}
            for key in ("data", "data_prefix"):
                value = handler.filters.get(key)
                if isinstance(value, str) and value:
                    callback_spec[key] = value
            chat_ids = [value for value in int_list(handler.filters.get("chat_id"))]
            sender_ids = [value for value in int_list(handler.filters.get("from_id"))]
            if chat_ids:
                callback_spec["chat_ids"] = chat_ids
            if sender_ids:
                callback_spec["sender_ids"] = sender_ids
            allowed_users = [value for value in int_list(handler.filters.get("allowed_users"))]
            if allowed_users:
                callback_spec["allowed_users"] = allowed_users
            if getattr(callback, "__serenox_owner_only__", False):
                callback_spec["owner_only"] = True
            description = inspect.getdoc(callback) or ""
            if description:
                callback_spec["description"] = description
            callback_handlers.append(callback_spec)
        elif handler.kind is HandlerKind.PRE_SEND:
            pre_send: dict[str, JSONValue] = {"name": handler.name}
            description = inspect.getdoc(callback) or ""
            if description:
                pre_send["description"] = description
            pre_send_handlers.append(pre_send)
    config: list[JSONValue] = []
    for item in host.module.config.schema():
        config_item: dict[str, JSONValue] = {
            "key": item.key,
            "type": item.type.value if hasattr(item.type, "value") else str(item.type),
            "default": item.default,
        }
        if item.description:
            config_item["description"] = item.description
        if item.required:
            config_item["required"] = True
        if item.secret:
            config_item["secret"] = True
        config.append(config_item)
    metadata: dict[str, JSONValue] = {
        "id": host.registry.metadata.id,
        "name": host.registry.metadata.name,
        "version": host.registry.metadata.version,
        "description": host.registry.metadata.description,
        "author": host.registry.metadata.author,
        "protocol_version": PROTOCOL_VERSION,
        "capabilities": ensure_json_value(
            list(host.registry.metadata.capabilities), field="metadata.capabilities"
        ),
        "commands": commands,
        "control_commands": control_commands,
        "watchers": watchers,
        "inline_handlers": inline_handlers,
        "callback_handlers": callback_handlers,
        "pre_send_handlers": pre_send_handlers,
        "config": config,
        "jobs": list(host.registry.metadata.jobs),
        "event_publications": list(host.registry.metadata.event_publications),
        "event_subscriptions": list(host.registry.metadata.event_subscriptions),
    }
    for field in (
        "description",
        "author",
        "capabilities",
        "commands",
        "control_commands",
        "watchers",
        "inline_handlers",
        "callback_handlers",
        "pre_send_handlers",
        "config",
        "jobs",
    ):
        if metadata.get(field) in (None, "", [], {}):
            metadata.pop(field, None)
    checked = ensure_json_value(metadata, field="metadata")
    if not isinstance(checked, dict):
        raise ProtocolError("metadata must be a JSON object")
    return checked
