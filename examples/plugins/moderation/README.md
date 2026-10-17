# Moderation example

This deliberately small plugin demonstrates `.mute 1h`, `.unmute`, `.ban
spam`, and `.kick` owner commands plus exact outgoing reply-only `mute 1h`,
`мут 1ч`, `размут`, `бан`, and `кик` watchers. Incoming messages never pass
the watcher's `out` filter. The mutation result is complete before the plugin
edits the owner's trigger, so an edit failure never rolls back or disguises a
successful moderation operation.

The live adapter intentionally reports `unsupported` for `kick`: Telegram's
usual ban-then-unban sequence is not atomic and the current result contract
does not yet represent its partial outcome precisely. Recorded transport can
exercise the ergonomic helper without making a live-support claim.
