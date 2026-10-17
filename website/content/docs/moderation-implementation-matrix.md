---
title: Moderation implementation matrix
description: Check protocol, adapter, recorded, and SDK support by operation.
---

# Moderation implementation matrix

`verified` means deterministic protocol/core/SDK tests; `recorded-only` is not
evidence of Telegram support; `live-gated` is a source-audited gotd mapping
requiring disposable-resource verification; `partial` names a bounded gap;
`unsupported` means the adapter returns a structured error before mutation.

All rows have explicit protocol DTOs and validation, core routing and
permission admission, recorded fixtures, Go/Python SDK methods, tests, and
documentation. The status columns below call out the adapter-specific delta.

| Operation | Protocol/core | gotd v0.145.1 mapping | Recorded | Go/Python SDK | Verification |
|---|---|---|---|---|---|
| members.get | verified | `channels.getParticipant` | verified | verified | live-gated |
| members.list/search admins | verified | `channels.getParticipants` + `channelParticipantsAdmins/Search` | verified | verified | live-gated |
| members.list restricted/banned | verified | `channelParticipantsBanned/Kicked` | verified | verified | live-gated |
| members.restrict/update | verified | read current + `channels.editBanned` | verified | verified | live-gated megagroups; unsupported broadcast/basic group |
| members.unrestrict | verified | `channels.editBanned` with empty rights | verified | verified | live-gated |
| members.ban/unban | verified | `channels.editBanned` view ban/empty rights | verified | verified | live-gated |
| members.kick | verified | deliberately no ban-then-unban transaction | verified | verified | recorded-only; live unsupported |
| admins.get acting/target | verified | acting rights from `channels.getFullChannel` chat entity; target via `channels.getParticipant` | verified | verified | live-gated |
| admins.list | verified | `channels.getParticipants` admins | verified | verified | live-gated |
| admins.promote/update/title | verified | `channels.editAdmin` direct rights/rank | verified | verified | live-gated |
| admins.demote | verified | `channels.editAdmin` empty rights/rank | verified | verified | live-gated |
| messages.delete batch | verified | `channels.deleteMessages` or `messages.deleteMessages(revoke)` | verified | verified | live-gated; private unsupported |
| messages.pin/unpin | verified | `messages.updatePinnedMessage` | verified | verified | live-gated |
| messages.unpin_all | verified | `messages.unpinAllMessages` | verified | verified | live-gated, separate destructive permission |
| messages.get_pinned | verified DTO | existing typed read is not live-mapped | recorded | verified read SDK | unsupported in moderation service |
| settings.get | verified | `channels.getFullChannel` / `messages.getFullChat` | verified | verified | live-gated |
| settings.set_default_rights | verified | bounded read + `messages.editChatDefaultBannedRights` | verified | verified | live-gated |
| settings.set_slow_mode | verified | `channels.toggleSlowMode` finite interval | verified | verified | live-gated megagroups |
| settings.set_protected_content | verified | `messages.toggleNoForwards` | verified | verified | live-gated |
| settings join mode/reactions | DTO reserved fields only | no mutation route published | recorded state only | no mutating SDK method | unsupported |
| join_requests.list | verified | `messages.getChatInviteImporters(requested)` | verified | verified | partial: first bounded live page only |
| join_requests.approve/decline | verified | `messages.hideChatJoinRequest` | verified | verified | live-gated |
| invites.create | verified | `messages.exportChatInvite` | verified, link redacted | verified | live-gated |
| invites.list | verified | `messages.getExportedChatInvites(InputUserSelf)` | verified, link redacted | verified | partial: first bounded live page only |
| invites.edit/revoke | verified | `messages.editExportedChatInvite` | verified | verified | live-gated |
| topics.get | verified | `messages.getForumTopicsByID` | verified | verified | live-gated, cached forum precondition |
| topics.close/reopen | verified | `messages.editForumTopic(closed)` | verified | verified | live-gated |
| topics.hide/unhide general | verified | `messages.editForumTopic(topic=1, hidden)` | verified | verified | live-gated |
| topics.delete | verified | `messages.deleteTopicHistory` | verified | verified | live-gated, separate destructive permission |
| report spam/content | no DTO or permission | no mapping | none | none | unsupported |

Basic-group participant/admin role management, batch join decisions, reaction
settings, join-request enablement, and live kick remain unsupported rather than
being inferred from enums or recorded fixtures. No live moderation suite was
executed without explicitly supplied disposable resources.
