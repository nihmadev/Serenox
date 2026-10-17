# profile.nowplaying

Shows the track playing on this Linux desktop in the Telegram last name, e.g.
`Ivan 🎧 Pantheon — Macarena`. Players are read over MPRIS (D-Bus session bus)
and the profile is written only when the track or play state changes; the
original last name comes back when playback stops, the player closes, or the
plugin is disabled/unloaded.

```text
/np enable
/np status
/np disable
```

Build and package:

```bash
mkdir -p dist/now-playing/bin
CGO_ENABLED=0 go build -o dist/now-playing/bin/now-playing ./examples/plugins/now-playing
cp examples/plugins/now-playing/manifest.json dist/now-playing/
go run ./cmd/srnx-plugin pack -source dist/now-playing -output dist/profile.nowplaying-1.0.0.srnx
```

The daemon must run as the desktop user on the same machine as the player.
Configuration, signal flow, and failure handling are described in
`website/content/docs/now-playing.md`.

Live check against the local players (read-only):

```bash
env -i PATH="$PATH" HOME="$HOME" NOWPLAYING_LIVE=1 go test -run Live -v ./examples/plugins/now-playing/
```
