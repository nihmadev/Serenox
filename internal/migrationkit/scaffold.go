package migrationkit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const (
	uwuManifest = `{
  "format": "serenox.plugin.package/v1",
  "id": "example.uwu",
	"name": "UwU formatter",
	"version": "1.1.0",
	"description": "Meme-style UwU formatter with an owner-controlled mode",
  "category": "python-sidecar",
  "protocol": {"min": "1.0", "max_exclusive": "2.0"},
  "core": {"min": "0.1.0", "max_exclusive": "1.0.0"},
  "sdk": {"min": "0.1.0", "max_exclusive": "1.0.0"},
  "sidecar": {"min": "0.1.0", "max_exclusive": "1.0.0"},
  "entrypoints": [{"os": "any", "arch": "any", "path": "plugin.py", "module": "plugin.py", "class": "UwU"}],
  "capabilities": ["commands", "pre_send"],
	"permissions": ["telegram.reply"],
  "files": []
}
`
	uwuPlugin = `from __future__ import annotations

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
    available = _MAX_TELEGRAM_TEXT - len(prefix) - len(suffix) - 1
    return f"{prefix}{clean[:available]}{suffix}"


class UwU(Module):
    """A playful outgoing-message UwU formatter."""

    id = "example.uwu"
    version = "1.1.0"
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
        if not self.config["enabled"] or not message.text.strip():
            return
        message.text = uwuify(message.text, variant=message.chat_id % len(_VARIANTS))


__all__ = ["UwU", "uwuify"]
`
)

// Templates returns the names of built-in migration starters.
func Templates() []string { return []string{"uwu"} }

// Scaffold creates a new plugin directory without overwriting existing files.
func Scaffold(directory, template string) error {
	if template != "uwu" {
		return fmt.Errorf("unknown migration template %q; available templates: uwu", template)
	}
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return fmt.Errorf("resolve output directory: %w", err)
	}
	if info, statErr := os.Stat(absolute); statErr == nil {
		if !info.IsDir() {
			return fmt.Errorf("output path is not a directory: %s", absolute)
		}
		entries, readErr := os.ReadDir(absolute)
		if readErr != nil {
			return fmt.Errorf("inspect output directory: %w", readErr)
		}
		if len(entries) != 0 {
			return fmt.Errorf("refusing to initialize non-empty directory: %s", absolute)
		}
	} else if !os.IsNotExist(statErr) {
		return fmt.Errorf("inspect output directory: %w", statErr)
	} else if err := os.MkdirAll(absolute, 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	files := map[string][]byte{
		"manifest.json": []byte(uwuManifest),
		"plugin.py":     []byte(uwuPlugin),
		"README.md":     []byte(uwuReadme),
	}
	for name, data := range files {
		if name == "manifest.json" {
			var value any
			if err := json.Unmarshal(data, &value); err != nil {
				return fmt.Errorf("validate built-in manifest: %w", err)
			}
		}
		if err := os.WriteFile(filepath.Join(absolute, name), data, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", name, err)
		}
	}
	return nil
}

const uwuReadme = `# UwU meme formatter starter

This migration-kit example formats outgoing text as a cute meme-style UwU
message. The owner controls the mode with .uwu enable, .uwu disable, and
.uwu status. It uses only the Serenox Python SDK and telegram.edit/reply.

Try it locally:

    make python-sync
    serenox plugin preflight --source .
    serenox plugin pack --source . --output ../dist/example.uwu-1.1.0.srnx
    serenox plugin verify ../dist/example.uwu-1.1.0.srnx

The pre-send hook rewrites every send action before it reaches Telegram. Run
serenox plugin preflight before publishing.
`
