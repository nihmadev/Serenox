"""Stable Python plugin compatibility SDK for Serenox protocol v1."""

from . import loader, utils, validators
from .actions import ClientProxy, InlineProxy, Message, RecordingActionSink
from .config import ConfigValue, LibraryConfig, ModuleConfig
from .decorators import callback_handler, command, inline_handler, tds, watcher
from .errors import (
    ActionError,
    ConfigurationError,
    ConnectionClosedError,
    OverloadedError,
    ProtocolError,
    SerenoxError,
    UnsupportedAPIError,
)
from .module import Module
from .protocol import PROTOCOL_VERSION, SDK_VERSION

__all__ = [
    "PROTOCOL_VERSION",
    "SDK_VERSION",
    "ActionError",
    "ClientProxy",
    "ConfigValue",
    "ConfigurationError",
    "ConnectionClosedError",
    "InlineProxy",
    "LibraryConfig",
    "Message",
    "Module",
    "ModuleConfig",
    "OverloadedError",
    "ProtocolError",
    "RecordingActionSink",
    "SerenoxError",
    "UnsupportedAPIError",
    "callback_handler",
    "command",
    "inline_handler",
    "loader",
    "tds",
    "utils",
    "validators",
    "watcher",
]
