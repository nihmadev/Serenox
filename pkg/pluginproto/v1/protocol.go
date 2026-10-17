// Package v1 defines the transport-neutral Serenox plugin protocol, version 1.
//
// The package intentionally contains no Telegram client library types. Public
// plugin processes can therefore remain independent of the Telegram adapter
// selected by the core.
package v1

import "errors"

const (
	// Protocol is written into every RPC envelope.
	Protocol = "serenox.plugin/1"
	// Version is the exact protocol version implemented by this package.
	Version = "1.0"

	DefaultMaxFrameSize            uint32 = 1 << 20 // 1 MiB
	MaxStringBytes                        = 64 << 10
	MaxCollectionItems                    = 1024
	MaxHandlers                           = 256
	MaxActions                            = 128
	MaxActionMetadata                     = 16
	MaxActionMetadataValueBytes           = 512
	MaxIdempotencyKeyBytes                = 128
	MaxWatcherTextBytes                   = 4096
	MaxInlineResults                      = 50
	MaxInlineRows                         = 16
	MaxButtonsPerRow                      = 8
	MaxCallbackDataBytes                  = 64
	MaxQueryIDBytes                       = 256
	MaxButtonTextBytes                    = 256
	MaxButtonURLBytes                     = 2048
	MaxControlCommands                    = 256
	MaxControlAliases                     = 1024
	MaxControlActorIDs                    = 128
	MaxControlQueryBytes                  = 4096
	MaxControlStateBytes                  = 16 << 10
	MaxControlFlowSteps                   = 16
	MaxControlSessionIDBytes              = 96
	MaxStorageBatchItems                  = 128
	MaxStorageValueBytes                  = 1 << 20
	MaxClientPageItems                    = 100
	MaxClientBatchItems                   = 100
	MaxClientCursorBytes                  = 512
	MaxClientQueryBytes                   = 4096
	MaxClientTextBytes                    = 64 << 10
	MaxClientEntities                     = 128
	MaxClientParticipants                 = 100
	MaxClientDialogs                      = 100
	MaxClientBackendCalls                 = 4
	MaxClientPages                        = 20
	MaxClientCursorLifetimeSeconds        = 15 * 60
	MaxModerationBatchItems               = 100
	MaxModerationPageItems                = 100
	MaxModerationWarnings                 = 16
	MaxModerationRankBytes                = 32
	MaxModerationInviteNameBytes          = 32
	MaxModerationReasonBytes              = 512
	MaxModerationDeadlineDays             = 366
	MaxModerationRequestSeconds           = 30
	MaxJobsPerPlugin                      = 256
	MaxJobListItems                       = 100
	MaxJobPayloadBytes                    = 64 << 10
	MaxJobDescriptionBytes                = 1024
	MaxJobHorizonDays                     = 366
	MaxEventPublications                  = 64
	MaxEventSubscriptions                 = 64
	MaxEventPayloadBytes                  = 64 << 10
	MaxEventPayloadDepth                  = 16
	MaxEventPayloadFields                 = 256
	MaxEventCollectionItems               = 256
	MaxEventStringBytes                   = 16 << 10
	MaxEventsPerResponse                  = 16
	MaxEventFanout                        = 32
	MaxEventAttempts                      = 10
	MaxEventHop                           = 16
	// Media transfers never use JSON frames. These limits are negotiated and
	// may only be reduced by a plugin/package resource policy.
	MediaMaxSizeBytes           int64 = 64 << 20
	MediaChunkSizeBytes               = 64 << 10
	MediaMaxTransferSeconds           = 300
	MediaIdleTimeoutSeconds           = 30
	MediaMaxConcurrentTransfers       = 2
	MediaPluginQuotaBytes       int64 = 256 << 20
)

var ErrInvalidDTO = errors.New("invalid plugin protocol DTO")

type EnvelopeKind string

const (
	KindRequest  EnvelopeKind = "request"
	KindResponse EnvelopeKind = "response"
)

type Method string

const (
	MethodHandshake Method = "handshake"
	MethodConfigure Method = "lifecycle.configure"
	MethodStart     Method = "lifecycle.start"
	MethodStop      Method = "lifecycle.stop"
	MethodHealth    Method = "health"
	MethodDispatch  Method = "dispatch"
	// The host.* methods are reserved for plugin-initiated reverse RPC. Peers
	// that do not negotiate reverse RPC must return ErrorUnsupported.
	MethodHostActions    Method = "host.actions"
	MethodHostStorage    Method = "host.storage"
	MethodHostClient     Method = "host.client"
	MethodHostMedia      Method = "host.media"
	MethodHostHTTP       Method = "host.http"
	MethodHostJobs       Method = "host.jobs"
	MethodHostModeration Method = "host.moderation"
	MethodHostEvents     Method = "host.events"
)

type Capability string

const (
	CapabilityCommands            Capability = "commands"
	CapabilityWatchers            Capability = "watchers"
	CapabilityInline              Capability = "inline"
	CapabilityStorage             Capability = "storage"
	CapabilityClient              Capability = "client_operations"
	CapabilityClientIdentity      Capability = "client.identity.read"
	CapabilityClientPeer          Capability = "client.peer.read"
	CapabilityClientUser          Capability = "client.user.read"
	CapabilityClientChat          Capability = "client.chat.read"
	CapabilityClientMessage       Capability = "client.message.read"
	CapabilityClientHistory       Capability = "client.history.read"
	CapabilityClientSearch        Capability = "client.search.read"
	CapabilityClientDialogs       Capability = "client.dialogs.read"
	CapabilityClientParticipants  Capability = "client.participants.read"
	CapabilityClientMediaMetadata Capability = "client.media.metadata"
	CapabilityMediaRead           Capability = "media_read"
	CapabilityMediaWrite          Capability = "media_write"
	CapabilityMediaAttach         Capability = "media_attach"
	CapabilityHTTP                Capability = "http"
	CapabilityPreSend             Capability = "pre_send"
	// Control-bot capabilities are deliberately separate from the user-session
	// client capability. They never grant access to a Bot API token or client.
	CapabilityControlBot          Capability = "control_bot"
	CapabilityControlBotInline    Capability = "control_bot.inline"
	CapabilityControlBotCallbacks Capability = "control_bot.callbacks"
	CapabilityJobs                Capability = "jobs"
	CapabilityModeration          Capability = "telegram.moderation"
	CapabilityProfile             Capability = "telegram.profile"
	CapabilityEventsPublish       Capability = "events.publish"
	CapabilityEventsSubscribe     Capability = "events.subscribe"
)

type UpdateKind string

const (
	UpdateMessage            UpdateKind = "message"
	UpdateEditedMessage      UpdateKind = "edited_message"
	UpdateDeleted            UpdateKind = "deleted_message"
	UpdateCallback           UpdateKind = "callback"
	UpdateInlineQuery        UpdateKind = "inline_query"
	UpdateControlMessage     UpdateKind = "control_bot_message"
	UpdateControlCallback    UpdateKind = "control_bot_callback"
	UpdateControlInlineQuery UpdateKind = "control_bot_inline_query"
)

type ActionKind string

const (
	ActionReply             ActionKind = "reply"
	ActionEdit              ActionKind = "edit"
	ActionDelete            ActionKind = "delete"
	ActionSend              ActionKind = "send"
	ActionInlineAnswer      ActionKind = "inline_answer"
	ActionClient            ActionKind = "client_operation"
	ActionBotAnswerInline   ActionKind = "bot_answer_inline_query"
	ActionBotAnswerCallback ActionKind = "bot_answer_callback_query"
	ActionBotSend           ActionKind = "bot_send_message"
	ActionBotEdit           ActionKind = "bot_edit_message"
	ActionBotDelete         ActionKind = "bot_delete_message"
	ActionProfileUpdateName ActionKind = "profile_update_name"
)

type ConfigType string

const (
	ConfigString  ConfigType = "string"
	ConfigInteger ConfigType = "integer"
	ConfigNumber  ConfigType = "number"
	ConfigBoolean ConfigType = "boolean"
	ConfigStrings ConfigType = "strings"
	ConfigJSON    ConfigType = "json"
)

type ErrorCode string

const (
	ErrorInvalidRequest              ErrorCode = "invalid_request"
	ErrorInvalidResponse             ErrorCode = "invalid_response"
	ErrorIncompatibleProtocol        ErrorCode = "incompatible_protocol"
	ErrorAuthentication              ErrorCode = "authentication_failed"
	ErrorInvalidState                ErrorCode = "invalid_state"
	ErrorUnsupported                 ErrorCode = "unsupported"
	ErrorOverloaded                  ErrorCode = "overloaded"
	ErrorDeadlineExceeded            ErrorCode = "deadline_exceeded"
	ErrorPlugin                      ErrorCode = "plugin_error"
	ErrorInternal                    ErrorCode = "internal"
	ErrorPermissionDenied            ErrorCode = "permission_denied"
	ErrorNotFound                    ErrorCode = "not_found"
	ErrorRateLimited                 ErrorCode = "rate_limited"
	ErrorStorageCommit               ErrorCode = "storage_commit_failed"
	ErrorMediaInvalidHandle          ErrorCode = "media_invalid_handle"
	ErrorMediaChecksum               ErrorCode = "media_checksum_mismatch"
	ErrorMediaSequence               ErrorCode = "media_sequence_error"
	ErrorMediaQuota                  ErrorCode = "media_quota_exceeded"
	ErrorMediaIncomplete             ErrorCode = "media_incomplete"
	ErrorMediaUnsupported            ErrorCode = "media_unsupported"
	ErrorMediaTransfer               ErrorCode = "media_transfer_failed"
	ErrorHTTPInvalidURL              ErrorCode = "http_invalid_url"
	ErrorHTTPForbiddenHost           ErrorCode = "http_forbidden_host"
	ErrorHTTPDNSFailure              ErrorCode = "http_dns_failure"
	ErrorHTTPConnection              ErrorCode = "http_connection_failure"
	ErrorHTTPTLSFailure              ErrorCode = "http_tls_failure"
	ErrorHTTPTimeout                 ErrorCode = "http_timeout"
	ErrorHTTPCancellation            ErrorCode = "http_cancellation"
	ErrorHTTPRedirect                ErrorCode = "http_redirect_rejected"
	ErrorHTTPRequestSize             ErrorCode = "http_request_size_limit"
	ErrorHTTPResponseSize            ErrorCode = "http_response_size_limit"
	ErrorHTTPRateLimit               ErrorCode = "http_rate_limit"
	ErrorHTTPOverload                ErrorCode = "http_overload"
	ErrorHTTPUpstream                ErrorCode = "http_upstream_status"
	ErrorHTTPSSRF                    ErrorCode = "http_ssrf_rejected"
	ErrorHTTPHeaders                 ErrorCode = "http_header_limit"
	ErrorHTTPUnsupported             ErrorCode = "http_unsupported"
	ErrorFloodWait                   ErrorCode = "flood_wait"
	ErrorMessageNotFound             ErrorCode = "message_not_found"
	ErrorPeerNotFound                ErrorCode = "peer_not_found"
	ErrorUserNotFound                ErrorCode = "user_not_found"
	ErrorChatNotFound                ErrorCode = "chat_not_found"
	ErrorDeleted                     ErrorCode = "deleted"
	ErrorDisconnected                ErrorCode = "disconnected"
	ErrorCursorInvalid               ErrorCode = "cursor_invalid"
	ErrorCursorExpired               ErrorCode = "cursor_expired"
	ErrorResponseTruncated           ErrorCode = "response_truncated"
	ErrorConversion                  ErrorCode = "conversion_failed"
	ErrorMediaFailure                ErrorCode = "media_failure"
	ErrorOutcomeUnknown              ErrorCode = "outcome_unknown"
	ErrorJobInvalid                  ErrorCode = "job_invalid"
	ErrorJobDuplicate                ErrorCode = "job_duplicate"
	ErrorJobNotFound                 ErrorCode = "job_not_found"
	ErrorJobStaleRevision            ErrorCode = "job_stale_revision"
	ErrorJobOverloaded               ErrorCode = "job_overloaded"
	ErrorJobHandlerUnavailable       ErrorCode = "job_handler_unavailable"
	ErrorJobInterrupted              ErrorCode = "job_interrupted"
	ErrorBotUnauthorized             ErrorCode = "bot_unauthorized"
	ErrorBotConflict                 ErrorCode = "bot_conflict"
	ErrorBotMalformedResponse        ErrorCode = "bot_malformed_response"
	ErrorBotNetwork                  ErrorCode = "bot_network_failure"
	ErrorBotTimeout                  ErrorCode = "bot_timeout"
	ErrorBotUnsupported              ErrorCode = "bot_unsupported"
	ErrorModerationTargetUnavailable ErrorCode = "moderation_target_unavailable"
	ErrorModerationAnonymousSender   ErrorCode = "moderation_anonymous_sender"
	ErrorModerationChannelSender     ErrorCode = "moderation_channel_sender"
	ErrorModerationUnsupportedChat   ErrorCode = "moderation_unsupported_chat"
	ErrorModerationAdminRequired     ErrorCode = "moderation_admin_required"
	ErrorModerationRightRequired     ErrorCode = "moderation_right_required"
	ErrorModerationProtectedTarget   ErrorCode = "moderation_protected_target"
	ErrorModerationStateConflict     ErrorCode = "moderation_state_conflict"
	ErrorModerationInvalidRight      ErrorCode = "moderation_invalid_right"
	ErrorModerationInvalidDeadline   ErrorCode = "moderation_invalid_deadline"
	ErrorModerationJoinNotFound      ErrorCode = "moderation_join_request_not_found"
	ErrorModerationInviteNotFound    ErrorCode = "moderation_invite_not_found"
	ErrorModerationTopicNotFound     ErrorCode = "moderation_topic_not_found"
	ErrorModerationNotForum          ErrorCode = "moderation_not_forum"
	ErrorProfileLeaseConflict        ErrorCode = "profile_lease_conflict"
	ErrorProfileStaleLease           ErrorCode = "profile_stale_lease"
	ErrorProfileInvalidName          ErrorCode = "profile_invalid_name"
)

// ResponseChannel makes a transport boundary explicit. Existing actions use
// the zero value (user_session) for wire compatibility; bot actions must use
// control_bot and cannot be sent through the user-session transport.
type ResponseChannel string

const (
	ResponseChannelUserSession ResponseChannel = "user_session"
	ResponseChannelControlBot  ResponseChannel = "control_bot"
)

type ControlSurface string

const (
	ControlSurfaceInline  ControlSurface = "inline"
	ControlSurfacePrivate ControlSurface = "private"
)

type ControlActorPolicy string

const (
	ControlActorOwnerOnly ControlActorPolicy = "owner_only"
	ControlActorAllowList ControlActorPolicy = "allow_list"
)

type ControlResponseMode string

const (
	ControlResponseInlineResult ControlResponseMode = "inline_result"
	ControlResponseBotMessage   ControlResponseMode = "bot_message"
	ControlResponseCallbackFlow ControlResponseMode = "callback_flow"
)

type LifecyclePhase string

const (
	LifecycleConfigure LifecyclePhase = "configure"
	LifecycleStart     LifecyclePhase = "start"
	LifecycleStop      LifecyclePhase = "stop"
)
