# UwU meme formatter plugin

This example turns your outgoing text into a cute meme-style UwU message. It
changes `r/l` (and `р/л`) into a softer pronunciation and aggressively decorates
the result with Telegram-rendered emoji: `❤️ 💖 💕 ✨ 🌟 💫 🥺 👉👈` plus `~`,
`мррр`, and `uwu/nya`.

The mode is owner-only and disabled by default:

```text
.uwu enable
.uwu disable
.uwu status
```

The plugin uses the SDK's `pre_send` hook, so send actions are rewritten before
they reach the Telegram transport. This is a real hook, not a post-send edit.

Run it from a source checkout:

```sh
make python-sync
serenox plugin preflight --source examples/plugins/uwu
serenox plugin pack \
  --source examples/plugins/uwu \
  --output dist/example.uwu-1.2.0.srnx
serenox plugin verify dist/example.uwu-1.2.0.srnx
```

The formatter is deterministic and idempotent, so an edited update cannot add
another layer of decorations. Rerun preflight before publishing.
