---
title: Action results, retries, and workflows
description: Interpret action outcomes and design retry-safe plugin workflows.
---

# Action results, retries, and workflows

Serenox actions are sequential Telegram mutations, not transactions. Every
result identifies its `action_id` and `operation` and has one of these states:

- `success`: Telegram returned enough information to establish acceptance;
- `failure`: the operation was rejected before acceptance;
- `outcome_unknown`: a disconnect, deadline, or other ambiguous transport
  failure occurred after the request could have been accepted;
- `skipped`: a bounded batch stopped before this action was attempted.

Successful send/reply/media results include a server message ID and a bounded
`Message` DTO when gotd returned enough fields. Edit and delete results include
the affected message ID and peer where those are known. No message body, ID,
peer, or media handle is fabricated. An unknown outcome is not retried by the
core; a plugin should perform a typed read or an application-specific recovery
check before deciding what to do.

## Idempotency

Mutating actions may carry `idempotency_key` (at most 128 UTF-8 bytes). The core
scopes it by verified plugin ID, Telegram account, and operation. The default
in-memory journal retains at most 4,096 completed/pending entries for 10
minutes. A duplicate returns the original result, including an
`outcome_unknown` result, without issuing another Telegram mutation. Entries
are evicted by age and then oldest completed entry when full; pending entries
are never evicted. State is intentionally not persisted: restart, unload,
uninstall, account shutdown, and daemon shutdown clear the journal. Therefore a
caller that needs recovery across restart must use a read-before-send policy or
an application-level durable record.

Actions without a caller key are at-most-once from Serenox's perspective. The
core does not derive keys for Telegram sends because payload-derived keys can
silently collapse unrelated user intent.

## Batches and workflows

`ActionBatchRequest` executes in input order. It returns one bounded result for
each attempted action. Failures do not stop later actions by default; setting
`stop_on_error` stops at the first non-success result and marks the response
`partial`/`stopped`. Telegram actions are never advertised as atomic. Host
storage batches remain atomic at the storage layer, but a Telegram action and a
later callback registration can still leave a partial external state. Dynamic
callback routes are published only after their sending action succeeds and are
removed when registration fails or the plugin unloads.

The Go SDK exposes `ServicesFromContext(ctx).Actions` with `Send`, `Reply`,
`Edit`, `Delete`, and bounded `Batch` helpers. Python's async message/client
helpers return the typed message when duplex action RPC is negotiated and raise
`ActionError` with the stable protocol code for failure or unknown outcome.
The optional workflow shape is a finite action batch: at most 128 steps, one
deadline/cancellation context, bounded intermediate results, and no detached
goroutines or hidden retries. Plugins may also compose their own typed calls.

Example Go demotivator step:

```go
services, _ := plugin.ServicesFromContext(ctx)
sent, err := services.Actions.Send(ctx, "demotivator-send", chatID, caption)
if err != nil || sent.Status != protocol.ActionResultSuccess { return err }
_, err = services.Actions.AttachMedia(ctx, "demotivator-media", protocol.Action{
    Kind: protocol.ActionSend, ChatID: chatID,
    Media: &protocol.MediaAttachment{Handle: handle, Kind: "photo", Caption: caption},
})
return err
```

Example Python synthesizer step:

```python
message = await client.send_message(chat_id, "rendering…")
await message.edit(rendered_caption)
await media.attach(handle, chat_id, caption=rendered_caption)
```

The supported live Telegram subset is plain/HTML send, reply, edit, delete,
and bounded media attachment. Inline answers, reactions, pin/forward,
membership changes, arbitrary client operations, and unsupported parse/media
options fail explicitly with `unsupported`; they are not smuggled through
`ClientOperation` or arbitrary JSON.

Result text, captions, usernames, peer details, media handles, callback data,
action IDs, and message IDs are bounded DTO fields and are redacted from
metrics/log labels by default. Temporary media files are created with private
permissions and removed after each adapter call, including error paths.

## Control-bot response channel

Control-bot UI operations use separate action kinds (`bot_send_message`,
`bot_edit_message`, `bot_delete_message`, `bot_answer_inline_query`, and
`bot_answer_callback_query`) and must set `response_channel` to `control_bot`.
They require `telegram.bot.write_ui`; user-session send/reply/edit/delete still
require their existing permissions and cannot be redirected to the bot. A Bot
API deadline after a possible mutation is returned as `outcome_unknown`; the
core does not retry it. Tokens, Bot API URLs, raw updates, or client objects are
never included in actions or results.

Moderation reuses the stable result-state vocabulary and adds explicit `noop`
and `partial` states. Mutation timeouts are `outcome_unknown`, are retained by
bounded idempotency, and are never automatically retried. A successful
moderation call and a later command edit are two sequential results, not one
transaction. See [moderation-api.md](moderation-api.md).
