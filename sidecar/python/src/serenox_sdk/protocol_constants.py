"""Internal protocol component: wire constants and enum definitions."""

from __future__ import annotations

import enum
from typing import TypeAlias, Union

PROTOCOL_NAME = "serenox.plugin/1"


PROTOCOL_VERSION = "1.0"


SDK_VERSION = "0.1.0"


DEFAULT_MAX_FRAME_BYTES = 1 << 20


MAX_STRING_BYTES = 64 << 10


MAX_COLLECTION_ITEMS = 1_024


MAX_HANDLERS = 256


MAX_ACTIONS = 128


MAX_STORAGE_VALUE_BYTES = 1 << 20


MEDIA_MAX_SIZE_BYTES = 64 << 20


MEDIA_CHUNK_SIZE_BYTES = 64 << 10


MEDIA_MAX_TRANSFER_SECONDS = 300


MEDIA_IDLE_TIMEOUT_SECONDS = 30


MEDIA_MAX_CONCURRENT_TRANSFERS = 2


MEDIA_PLUGIN_QUOTA_BYTES = 256 << 20


JSONValue: TypeAlias = Union[  # noqa: UP007 - runtime-safe recursive alias on Python 3.10
    bool, int, float, str, list["JSONValue"], dict[str, "JSONValue"], None
]


class EnvelopeKind(str, enum.Enum):
    REQUEST = "request"
    RESPONSE = "response"


class Method(str, enum.Enum):
    HANDSHAKE = "handshake"
    CONFIGURE = "lifecycle.configure"
    START = "lifecycle.start"
    STOP = "lifecycle.stop"
    HEALTH = "health"
    DISPATCH = "dispatch"
    HOST_ACTIONS = "host.actions"
    HOST_STORAGE = "host.storage"
    HOST_CLIENT = "host.client"
    HOST_MEDIA = "host.media"
    HOST_HTTP = "host.http"
    HOST_JOBS = "host.jobs"
    HOST_MODERATION = "host.moderation"
    HOST_EVENTS = "host.events"


class Capability(str, enum.Enum):
    COMMANDS = "commands"
    WATCHERS = "watchers"
    INLINE = "inline"
    STORAGE = "storage"
    CLIENT = "client_operations"
    CLIENT_IDENTITY = "client.identity.read"
    CLIENT_PEER = "client.peer.read"
    CLIENT_USER = "client.user.read"
    CLIENT_CHAT = "client.chat.read"
    CLIENT_MESSAGE = "client.message.read"
    CLIENT_HISTORY = "client.history.read"
    CLIENT_SEARCH = "client.search.read"
    CLIENT_DIALOGS = "client.dialogs.read"
    CLIENT_PARTICIPANTS = "client.participants.read"
    CLIENT_MEDIA_METADATA = "client.media.metadata"
    MEDIA_READ = "media_read"
    MEDIA_WRITE = "media_write"
    MEDIA_ATTACH = "media_attach"
    HTTP = "http"
    PRE_SEND = "pre_send"
    EVENTS_PUBLISH = "events.publish"
    EVENTS_SUBSCRIBE = "events.subscribe"
    CONTROL_BOT = "control_bot"
    CONTROL_BOT_INLINE = "control_bot.inline"
    CONTROL_BOT_CALLBACKS = "control_bot.callbacks"
    JOBS = "jobs"
    MODERATION = "telegram.moderation"
    PROFILE = "telegram.profile"


class JobOperation(str, enum.Enum):
    CREATE = "create"
    UPDATE = "update"
    CANCEL = "cancel"
    PAUSE = "pause"
    RESUME = "resume"
    TRIGGER = "trigger"
    GET = "get"
    LIST = "list"
    INSPECT = "inspect"
    CLEAR = "clear"
    DELETE = "delete"


class UpdateKind(str, enum.Enum):
    MESSAGE = "message"
    EDITED_MESSAGE = "edited_message"
    DELETED = "deleted_message"
    CALLBACK = "callback"
    INLINE_QUERY = "inline_query"
    CONTROL_BOT_MESSAGE = "control_bot_message"
    CONTROL_BOT_CALLBACK = "control_bot_callback"
    CONTROL_BOT_INLINE_QUERY = "control_bot_inline_query"


class ActionKind(str, enum.Enum):
    """The exact action-kind values declared by the Go v1 protocol."""

    REPLY = "reply"
    EDIT = "edit"
    DELETE = "delete"
    SEND = "send"
    INLINE_ANSWER = "inline_answer"
    CLIENT = "client_operation"
    BOT_ANSWER_INLINE = "bot_answer_inline_query"
    BOT_ANSWER_CALLBACK = "bot_answer_callback_query"
    BOT_SEND = "bot_send_message"
    BOT_EDIT = "bot_edit_message"
    BOT_DELETE = "bot_delete_message"


class ResponseChannel(str, enum.Enum):
    USER_SESSION = "user_session"
    CONTROL_BOT = "control_bot"


class ControlSurface(str, enum.Enum):
    INLINE = "inline"
    PRIVATE = "private"


class ControlActorPolicy(str, enum.Enum):
    OWNER_ONLY = "owner_only"
    ALLOW_LIST = "allow_list"


class ControlResponseMode(str, enum.Enum):
    INLINE_RESULT = "inline_result"
    BOT_MESSAGE = "bot_message"
    CALLBACK_FLOW = "callback_flow"


class ConfigType(str, enum.Enum):
    STRING = "string"
    INTEGER = "integer"
    NUMBER = "number"
    BOOLEAN = "boolean"
    STRINGS = "strings"
    JSON = "json"


class ErrorCode(str, enum.Enum):
    INVALID_REQUEST = "invalid_request"
    INVALID_RESPONSE = "invalid_response"
    INCOMPATIBLE_PROTOCOL = "incompatible_protocol"
    AUTHENTICATION = "authentication_failed"
    INVALID_STATE = "invalid_state"
    UNSUPPORTED = "unsupported"
    OVERLOADED = "overloaded"
    DEADLINE_EXCEEDED = "deadline_exceeded"
    PLUGIN = "plugin_error"
    INTERNAL = "internal"
    MEDIA_INVALID_HANDLE = "media_invalid_handle"
    MEDIA_CHECKSUM = "media_checksum_mismatch"
    MEDIA_SEQUENCE = "media_sequence_error"
    MEDIA_QUOTA = "media_quota_exceeded"
    MEDIA_INCOMPLETE = "media_incomplete"
    MEDIA_UNSUPPORTED = "media_unsupported"
    MEDIA_TRANSFER = "media_transfer_failed"
    HTTP_INVALID_URL = "http_invalid_url"
    HTTP_FORBIDDEN_HOST = "http_forbidden_host"
    HTTP_DNS_FAILURE = "http_dns_failure"
    HTTP_CONNECTION = "http_connection_failure"
    HTTP_TLS = "http_tls_failure"
    HTTP_TIMEOUT = "http_timeout"
    HTTP_CANCELLATION = "http_cancellation"
    HTTP_REDIRECT = "http_redirect_rejected"
    HTTP_REQUEST_SIZE = "http_request_size_limit"
    HTTP_RESPONSE_SIZE = "http_response_size_limit"
    HTTP_RATE_LIMIT = "http_rate_limit"
    HTTP_OVERLOAD = "http_overload"
    HTTP_UPSTREAM = "http_upstream_status"
    HTTP_SSRF = "http_ssrf_rejected"
    HTTP_HEADERS = "http_header_limit"
    HTTP_UNSUPPORTED = "http_unsupported"
    FLOOD_WAIT = "flood_wait"
    MESSAGE_NOT_FOUND = "message_not_found"
    PEER_NOT_FOUND = "peer_not_found"
    CURSOR_INVALID = "cursor_invalid"
    CURSOR_EXPIRED = "cursor_expired"
    DISCONNECTED = "disconnected"
    DELETED = "deleted"
    RESPONSE_TRUNCATED = "response_truncated"
    MEDIA_FAILURE = "media_failure"
    OUTCOME_UNKNOWN = "outcome_unknown"
    PROFILE_LEASE_CONFLICT = "profile_lease_conflict"
    PROFILE_STALE_LEASE = "profile_stale_lease"
    PROFILE_INVALID_NAME = "profile_invalid_name"


class LifecyclePhase(str, enum.Enum):
    CONFIGURE = "configure"
    START = "start"
    STOP = "stop"


class StorageOperation(str, enum.Enum):
    GET = "get"
    SET = "set"
    DELETE = "delete"
    LIST = "list"
    BATCH = "batch"


class HandlerKind(str, enum.Enum):
    """Sidecar-local handler categories; these never cross the v1 wire."""

    COMMAND = "command"
    WATCHER = "watcher"
    INLINE = "inline"
    CALLBACK = "callback"
    PRE_SEND = "pre_send"


class OperationKind(str, enum.Enum):
    """SDK operation surface lowered to one or more exact wire actions."""

    REPLY = "message.reply"
    EDIT = "message.edit"
    DELETE = "message.delete"
    SEND = "client.send_message"
    GET_MESSAGE = "client.get_message"
    GET_ME = "client.get_me"
    RESOLVE_PEER = "client.resolve_peer"
    GET_USER = "client.get_user"
    GET_USERS = "client.get_users"
    GET_CHAT = "client.get_chat"
    GET_CHATS = "client.get_chats"
    SEARCH_PUBLIC_USERNAME = "client.search_public_username"
    GET_MESSAGES = "client.get_messages"
    GET_MESSAGE_AROUND = "client.get_message_around"
    GET_REPLY = "client.get_reply"
    GET_REPLIES = "client.get_replies"
    GET_PINNED_MESSAGE = "client.get_pinned_message"
    GET_ALBUM = "client.get_album"
    GET_MESSAGE_REACTIONS = "client.get_message_reactions"
    GET_POLL = "client.get_poll"
    GET_HISTORY = "client.get_history"
    GET_HISTORY_AROUND = "client.get_history_around"
    SEARCH_MESSAGES = "client.search_messages"
    SEARCH_MESSAGES_BY_SENDER = "client.search_messages_by_sender"
    SEARCH_MESSAGES_BY_MEDIA = "client.search_messages_by_media"
    SEARCH_GLOBAL_MESSAGES = "client.search_global_messages"
    LIST_DIALOGS = "client.list_dialogs"
    GET_DIALOG = "client.get_dialog"
    LIST_PINNED_DIALOGS = "client.list_pinned_dialogs"
    GET_COMMON_CHATS = "client.get_common_chats"
    GET_SAVED_MESSAGES_CONTEXT = "client.get_saved_messages_context"
    LIST_PARTICIPANTS = "client.list_participants"
    SEARCH_PARTICIPANTS = "client.search_participants"
    GET_PARTICIPANT = "client.get_participant"
    GET_RECENT_SENDERS = "client.get_recent_senders"
    INSPECT_MEDIA = "client.inspect_media"
    GET_MEDIA_ATTRIBUTES = "client.get_media_attributes"
    GET_MESSAGE_MEDIA_GROUP = "client.get_message_media_group"
    JOIN_CHAT = "client.join_chat"
    LEAVE_CHAT = "client.leave_chat"
    REACT = "client.react"
    INLINE_ANSWER = "inline.answer"
    INLINE_FORM = "inline.form"
    INLINE_EDIT = "inline.edit"
    INLINE_DELETE = "inline.delete"
    STORAGE_GET = "storage.get"
    STORAGE_SET = "storage.set"
    STORAGE_DELETE = "storage.delete"
    MEDIA_INSPECT = "media.inspect"
    MEDIA_DOWNLOAD = "media.download"
    MEDIA_UPLOAD = "media.upload"
    MEDIA_ATTACH = "media.attach"
    MEDIA_DELETE = "media.delete"
    INVOKE_COMMAND = "command.invoke"
    JOB_CREATE = "job.create"
    JOB_UPDATE = "job.update"
    JOB_CANCEL = "job.cancel"
    JOB_PAUSE = "job.pause"
    JOB_RESUME = "job.resume"
    JOB_TRIGGER = "job.trigger"
    JOB_GET = "job.get"
    JOB_LIST = "job.list"
    JOB_INSPECT = "job.inspect"
    JOB_CLEAR = "job.clear"
    JOB_DELETE = "job.delete"
    EVENT_PUBLISH = "event.publish"
    PROFILE = "profile.update_name"
    MOD_MEMBERS_GET = "moderation.members.get"
    MOD_MEMBERS_LIST = "moderation.members.list"
    MOD_MEMBERS_RESTRICT = "moderation.members.restrict"
    MOD_MEMBERS_UNRESTRICT = "moderation.members.unrestrict"
    MOD_MEMBERS_BAN = "moderation.members.ban"
    MOD_MEMBERS_UNBAN = "moderation.members.unban"
    MOD_MEMBERS_KICK = "moderation.members.kick"
    MOD_ADMINS_GET_ACTING = "moderation.admins.get_acting_rights"
    MOD_ADMINS_GET = "moderation.admins.get"
    MOD_ADMINS_LIST = "moderation.admins.list"
    MOD_ADMINS_SET = "moderation.admins.set"
    MOD_ADMINS_DEMOTE = "moderation.admins.demote"
    MOD_MESSAGES_DELETE = "moderation.messages.delete"
    MOD_MESSAGES_PIN = "moderation.messages.pin"
    MOD_MESSAGES_UNPIN = "moderation.messages.unpin"
    MOD_MESSAGES_UNPIN_ALL = "moderation.messages.unpin_all"
    MOD_SETTINGS_GET = "moderation.settings.get"
    MOD_SETTINGS_DEFAULT = "moderation.settings.set_default_rights"
    MOD_SETTINGS_SLOW = "moderation.settings.set_slow_mode"
    MOD_SETTINGS_PROTECTED = "moderation.settings.set_protected_content"
    MOD_JOIN_LIST = "moderation.join_requests.list"
    MOD_JOIN_APPROVE = "moderation.join_requests.approve"
    MOD_JOIN_DECLINE = "moderation.join_requests.decline"
    MOD_INVITES_CREATE = "moderation.invites.create"
    MOD_INVITES_LIST = "moderation.invites.list"
    MOD_INVITES_EDIT = "moderation.invites.edit"
    MOD_INVITES_REVOKE = "moderation.invites.revoke"
    MOD_TOPICS_GET = "moderation.topics.get"
    MOD_TOPICS_CLOSE = "moderation.topics.close"
    MOD_TOPICS_REOPEN = "moderation.topics.reopen"
    MOD_TOPICS_HIDE = "moderation.topics.hide_general"
    MOD_TOPICS_UNHIDE = "moderation.topics.unhide_general"
    MOD_TOPICS_DELETE = "moderation.topics.delete"


__all__ = [
    "DEFAULT_MAX_FRAME_BYTES",
    "MAX_ACTIONS",
    "MAX_COLLECTION_ITEMS",
    "MAX_HANDLERS",
    "MAX_STORAGE_VALUE_BYTES",
    "MAX_STRING_BYTES",
    "MEDIA_CHUNK_SIZE_BYTES",
    "MEDIA_IDLE_TIMEOUT_SECONDS",
    "MEDIA_MAX_CONCURRENT_TRANSFERS",
    "MEDIA_MAX_SIZE_BYTES",
    "MEDIA_MAX_TRANSFER_SECONDS",
    "MEDIA_PLUGIN_QUOTA_BYTES",
    "PROTOCOL_NAME",
    "PROTOCOL_VERSION",
    "SDK_VERSION",
    "ActionKind",
    "Capability",
    "ConfigType",
    "EnvelopeKind",
    "ErrorCode",
    "HandlerKind",
    "JSONValue",
    "LifecyclePhase",
    "Method",
    "OperationKind",
    "StorageOperation",
    "UpdateKind",
]
