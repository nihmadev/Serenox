---
title: Telegram client implementation matrix
description: Check verified and partial read-only Telegram operations.
---

# Read-only Telegram client implementation matrix

This matrix records support by operation. A protocol type, SDK method, or
recorded fixture is not evidence of live Telegram support.

| Operation group | Operation | Status | Evidence / boundary |
|---|---|---|---|
| Identity | `get_me` | verified | Live gotd identity and recorded DTO tests |
| Profile | `profile_update_name` | partial | Typed lease boundary and gotd `account.updateProfile` mapping; live account mutation remains separately gated |
| Peers | `resolve_peer` | verified | Live username/entity cache path and protocol tests |
| Users | `get_user`, `get_users` | partial | DTO and bounded lookup contract; live batch mapping is adapter-dependent |
| Chats | `get_chat`, `get_chats` | partial | DTO and bounded lookup contract; live entity refresh is adapter-dependent |
| Messages | `get_message` | verified | Live bounded history lookup and recorded tests |
| Messages | `get_messages` | partial | Bounded one-peer contract; no hidden scan |
| Messages | replies, albums, reactions, polls, service metadata | partial | DTOs are bounded; live population is only claimed when gotd conversion supplies it |
| History | `get_history`, `get_history_around` | partial | Explicit page limits/cursors; live mapping uses one bounded history call |
| Search | `search_messages`, sender/media filters | partial | Live gotd mapping performs one bounded search call; global/public search remains planned |
| Dialogs | dialog listing and pinned dialogs | planned | Typed contract only until a bounded gotd mapping is verified |
| Participants | participant listing/search/status | planned | Typed contract only until a bounded gotd mapping is verified |
| Media metadata | inspect/attributes/group | partial | Opaque metadata contract; no implicit download or filesystem access |
| Arbitrary Telegram methods | any raw method dispatch | unsupported | No raw TL, access hash, session, or library object crosses the boundary |

Recorded transport is credential-free and deterministic. It validates the
same DTO and request limits but never upgrades a live operation to verified.

Administrative operations are catalogued separately in the
[moderation implementation matrix](moderation-implementation-matrix.md). An
enum or SDK method alone never upgrades a recorded or partial row to live
support.
