---
title: profile.nowplaying
description: Show the desktop's current track in the profile last name over MPRIS.
---

# Now playing in the profile name

`profile.nowplaying` (`examples/plugins/now-playing`) watches MPRIS media
players on the Linux D-Bus session bus and writes the current track into the
account's **last name**, leaving the first name untouched. It uses the same
lease-owned profile contract as [profile.clock](./profile-clock.md).

## Event flow

The plugin implements `BackgroundHandler` and subscribes to two signals rather
than polling:

- `org.freedesktop.DBus.Properties.PropertiesChanged` on
  `/org/mpris/MediaPlayer2` for `org.mpris.MediaPlayer2.Player`, reacting only
  to `Metadata` and `PlaybackStatus` (position, volume and rate are ignored);
- `org.freedesktop.DBus.NameOwnerChanged` in the `org.mpris.MediaPlayer2`
  namespace, for players appearing and closing.

MPRIS has no separate "track ended" signal. A new track arrives as a
`Metadata` change, the end of a queue as `PlaybackStatus = Stopped`, and a
closed player as its bus name losing its owner. Signal bursts are coalesced
for `debounce_ms` (1500 ms by default), then the players are re-read. A
playing player beats a paused one, and the selected player stays selected
while it remains in the same state. The profile is written only when the
rendered text differs from what is shown, so seeks and repeated metadata cost
no Telegram calls.

When nothing is playing, or the player stops or closes, the original last name
is restored. Paused playback keeps the track unless `clear_on_pause` is set.

## Configuration

| Key | Default | Meaning |
| --- | --- | --- |
| `format` | `🎧 {artist} — {title}` | Template; `{artist}`, `{title}`, `{album}` |
| `format_no_artist` | `🎧 {title}` | Used when the player reports no artist |
| `players` | `[]` | Allow-list of substrings of the bus name or `Identity` |
| `ignore_players` | `["TelegramDesktop"]` | Players never used (Telegram voice messages) |
| `clear_on_pause` | `false` | Restore the original last name while paused |
| `debounce_ms` | `1500` | Signal coalescing window |
| `bus_address` | `""` | Session bus; defaults to `$XDG_RUNTIME_DIR/bus` |

The core starts plugins with only `PATH` in the environment, so the plugin
derives the session bus address from the systemd default
`/run/user/<uid>/bus`. The daemon must therefore run as the desktop user on the
same machine as the player.

Names are bounded at 64 **bytes**; Cyrillic takes two bytes per letter. Text is
normalised (control characters removed, whitespace collapsed) and truncated on
a rune boundary with `…`.

## Failure handling

- Flood wait: the next write is deferred for the reported duration, then the
  latest track is applied.
- Manual rename: the core reports a stale lease; the plugin re-acquires and
  treats the new name as the original.
- Crash without restore: the plugin persists its original name and the last
  value it wrote. On restart, if the profile still shows that value, it writes
  the original back before re-acquiring, so the core never captures a track
  title as the "original".
- Unload and daemon shutdown restore the original name through the core.

## Operate

```text
/np enable
/np status
/np disable
```

Only one plugin can own the profile name, so disable `profile.clock` first.
