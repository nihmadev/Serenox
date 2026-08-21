"""Compatibility import surface for ``from serenox_sdk import loader``."""

from .config import ConfigValue, LibraryConfig, ModuleConfig
from .decorators import (
    callback_handler,
    command,
    group_admin,
    group_member,
    group_owner,
    inline_handler,
    owner,
    pm,
    sudo,
    support,
    tds,
    unrestricted,
    watcher,
)
from .module import Module

__all__ = [
    "ConfigValue",
    "LibraryConfig",
    "Module",
    "ModuleConfig",
    "callback_handler",
    "command",
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
