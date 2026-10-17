package v1

import (
	"encoding/json"
)

type PluginMetadata struct {
	ID                 string              `json:"id"`
	Name               string              `json:"name"`
	Version            string              `json:"version"`
	Description        string              `json:"description,omitempty"`
	Author             string              `json:"author,omitempty"`
	ProtocolVersion    string              `json:"protocol_version"`
	Capabilities       []Capability        `json:"capabilities,omitempty"`
	Commands           []CommandSpec       `json:"commands,omitempty"`
	Watchers           []WatcherSpec       `json:"watchers,omitempty"`
	InlineHandlers     []InlineSpec        `json:"inline_handlers,omitempty"`
	CallbackHandlers   []CallbackSpec      `json:"callback_handlers,omitempty"`
	PreSendHandlers    []PreSendSpec       `json:"pre_send_handlers,omitempty"`
	ControlCommands    []ControlCommand    `json:"control_commands,omitempty"`
	Jobs               []JobHandlerSpec    `json:"jobs,omitempty"`
	EventPublications  []EventPublication  `json:"event_publications,omitempty"`
	EventSubscriptions []EventSubscription `json:"event_subscriptions,omitempty"`
	Config             []ConfigSpec        `json:"config,omitempty"`
}

// ControlCommand is an opt-in Bot API command declaration. Registration is
// validated before a plugin is activated; ordinary Commands stay user-session
// only and are never published through a bot by implication.
type ControlCommand struct {
	Name                  string              `json:"name"`
	Aliases               []string            `json:"aliases,omitempty"`
	Description           string              `json:"description,omitempty"`
	Surfaces              []ControlSurface    `json:"surfaces"`
	ActorPolicy           ControlActorPolicy  `json:"actor_policy,omitempty"`
	AllowedActorIDs       []int64             `json:"allowed_actor_ids,omitempty"`
	Mutating              bool                `json:"mutating,omitempty"`
	RequireConfirmation   bool                `json:"require_confirmation,omitempty"`
	ConfirmationTimeoutMS int64               `json:"confirmation_timeout_ms,omitempty"`
	AllowQueryExecution   bool                `json:"allow_query_execution,omitempty"`
	MaxArgumentBytes      int                 `json:"max_argument_bytes,omitempty"`
	MaxStateBytes         int                 `json:"max_state_bytes,omitempty"`
	ResponseMode          ControlResponseMode `json:"response_mode"`
	HandlerID             string              `json:"handler_id,omitempty"`
}

// PreSendSpec declares a synchronous transformation that runs immediately
// before a send action reaches the Telegram transport.
type PreSendSpec struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type CommandSpec struct {
	Name        string   `json:"name"`
	Aliases     []string `json:"aliases,omitempty"`
	Description string   `json:"description,omitempty"`
	OwnerOnly   bool     `json:"owner_only,omitempty"`
}

type WatcherSpec struct {
	ID          string        `json:"id"`
	Description string        `json:"description,omitempty"`
	Filter      WatcherFilter `json:"filter"`
}

// InlineSpec declares one bounded inline-query handler.
type InlineSpec struct {
	Name         string  `json:"name"`
	Description  string  `json:"description,omitempty"`
	QueryPrefix  string  `json:"query_prefix,omitempty"`
	SenderIDs    []int64 `json:"sender_ids,omitempty"`
	AllowedUsers []int64 `json:"allowed_users,omitempty"`
	OwnerOnly    bool    `json:"owner_only,omitempty"`
}

// CallbackSpec declares one bounded callback handler. Empty data filters
// intentionally match every callback admitted to the plugin.
type CallbackSpec struct {
	Name         string  `json:"name"`
	Description  string  `json:"description,omitempty"`
	Data         string  `json:"data,omitempty"`
	DataPrefix   string  `json:"data_prefix,omitempty"`
	ChatIDs      []int64 `json:"chat_ids,omitempty"`
	SenderIDs    []int64 `json:"sender_ids,omitempty"`
	AllowedUsers []int64 `json:"allowed_users,omitempty"`
	OwnerOnly    bool    `json:"owner_only,omitempty"`
	TTLMS        int64   `json:"ttl_ms,omitempty"`
}

// WatcherFilter is evaluated by the core before an update is queued to a
// plugin. Empty slices mean no restriction.
type WatcherFilter struct {
	Kinds      []UpdateKind `json:"kinds,omitempty"`
	ChatIDs    []int64      `json:"chat_ids,omitempty"`
	SenderIDs  []int64      `json:"sender_ids,omitempty"`
	Incoming   bool         `json:"incoming,omitempty"`
	Outgoing   bool         `json:"outgoing,omitempty"`
	TextPrefix string       `json:"text_prefix,omitempty"`
}

type ConfigSpec struct {
	Key         string          `json:"key"`
	Type        ConfigType      `json:"type"`
	Description string          `json:"description,omitempty"`
	Required    bool            `json:"required,omitempty"`
	Secret      bool            `json:"secret,omitempty"`
	Default     json.RawMessage `json:"default,omitempty"`
}

type ConfigValue struct {
	Key    string          `json:"key"`
	Type   ConfigType      `json:"type"`
	Value  json.RawMessage `json:"value"`
	Secret bool            `json:"secret,omitempty"`
}
