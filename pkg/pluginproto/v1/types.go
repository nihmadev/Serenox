// Package v1 defines the transport-neutral Serenox plugin protocol, version 1.
//
// The package intentionally contains no Telegram client library types. Public
// plugin processes can therefore remain independent of the Telegram adapter
// selected by the core.
package v1

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

const (
	// Protocol is written into every RPC envelope.
	Protocol = "serenox.plugin/1"
	// Version is the exact protocol version implemented by this package.
	Version = "1.0"

	DefaultMaxFrameSize uint32 = 1 << 20 // 1 MiB
	MaxStringBytes             = 64 << 10
	MaxCollectionItems         = 1024
	MaxHandlers                = 256
	MaxActions                 = 128
	MaxWatcherTextBytes        = 4096
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
	MethodHostActions Method = "host.actions"
	MethodHostStorage Method = "host.storage"
	MethodHostClient  Method = "host.client"
)

type Capability string

const (
	CapabilityCommands Capability = "commands"
	CapabilityWatchers Capability = "watchers"
	CapabilityInline   Capability = "inline"
	CapabilityStorage  Capability = "storage"
	CapabilityClient   Capability = "client_operations"
)

type UpdateKind string

const (
	UpdateMessage       UpdateKind = "message"
	UpdateEditedMessage UpdateKind = "edited_message"
	UpdateDeleted       UpdateKind = "deleted_message"
	UpdateCallback      UpdateKind = "callback"
	UpdateInlineQuery   UpdateKind = "inline_query"
)

type ActionKind string

const (
	ActionReply        ActionKind = "reply"
	ActionEdit         ActionKind = "edit"
	ActionDelete       ActionKind = "delete"
	ActionSend         ActionKind = "send"
	ActionInlineAnswer ActionKind = "inline_answer"
	ActionClient       ActionKind = "client_operation"
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
	ErrorInvalidRequest       ErrorCode = "invalid_request"
	ErrorInvalidResponse      ErrorCode = "invalid_response"
	ErrorIncompatibleProtocol ErrorCode = "incompatible_protocol"
	ErrorAuthentication       ErrorCode = "authentication_failed"
	ErrorInvalidState         ErrorCode = "invalid_state"
	ErrorUnsupported          ErrorCode = "unsupported"
	ErrorOverloaded           ErrorCode = "overloaded"
	ErrorDeadlineExceeded     ErrorCode = "deadline_exceeded"
	ErrorPlugin               ErrorCode = "plugin_error"
	ErrorInternal             ErrorCode = "internal"
)

type LifecyclePhase string

const (
	LifecycleConfigure LifecyclePhase = "configure"
	LifecycleStart     LifecyclePhase = "start"
	LifecycleStop      LifecyclePhase = "stop"
)

// Request is a host-to-plugin RPC envelope.
type Request struct {
	Protocol       string          `json:"protocol"`
	RequestID      uint64          `json:"request_id"`
	Kind           EnvelopeKind    `json:"kind"`
	DeadlineUnixMS int64           `json:"deadline_unix_ms"`
	Method         Method          `json:"method"`
	Payload        json.RawMessage `json:"payload,omitempty"`
}

// Response is a plugin-to-host RPC envelope.
type Response struct {
	Protocol       string          `json:"protocol"`
	RequestID      uint64          `json:"request_id"`
	Kind           EnvelopeKind    `json:"kind"`
	DeadlineUnixMS int64           `json:"deadline_unix_ms"`
	Result         json.RawMessage `json:"result,omitempty"`
	Error          *Error          `json:"error,omitempty"`
}

// Error is the stable wire error representation.
type Error struct {
	Code      ErrorCode         `json:"code"`
	Message   string            `json:"message"`
	Retryable bool              `json:"retryable,omitempty"`
	Field     string            `json:"field,omitempty"`
	Details   map[string]string `json:"details,omitempty"`
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Field != "" {
		return fmt.Sprintf("%s: %s (%s)", e.Code, e.Message, e.Field)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

type PluginMetadata struct {
	ID              string        `json:"id"`
	Name            string        `json:"name"`
	Version         string        `json:"version"`
	Description     string        `json:"description,omitempty"`
	Author          string        `json:"author,omitempty"`
	ProtocolVersion string        `json:"protocol_version"`
	Capabilities    []Capability  `json:"capabilities,omitempty"`
	Commands        []CommandSpec `json:"commands,omitempty"`
	Watchers        []WatcherSpec `json:"watchers,omitempty"`
	Config          []ConfigSpec  `json:"config,omitempty"`
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

type Entity struct {
	Type     string `json:"type"`
	Offset   int    `json:"offset"`
	Length   int    `json:"length"`
	URL      string `json:"url,omitempty"`
	UserID   int64  `json:"user_id,omitempty"`
	Language string `json:"language,omitempty"`
}

// Media is the library-independent attachment descriptor. FileID is an opaque
// core handle, not a Telegram-library object; plugins request downloads through
// an explicit client operation.
type Media struct {
	Kind     string `json:"kind"`
	FileID   string `json:"file_id,omitempty"`
	FileName string `json:"file_name,omitempty"`
	MIMEType string `json:"mime_type,omitempty"`
	Size     int64  `json:"size,omitempty"`
}

type Message struct {
	ID               int64    `json:"id"`
	ChatID           int64    `json:"chat_id"`
	SenderID         int64    `json:"sender_id,omitempty"`
	DateUnixMS       int64    `json:"date_unix_ms"`
	EditDateUnixMS   int64    `json:"edit_date_unix_ms,omitempty"`
	Text             string   `json:"text,omitempty"`
	Outgoing         bool     `json:"outgoing,omitempty"`
	Mentioned        bool     `json:"mentioned,omitempty"`
	ChatKind         string   `json:"chat_kind,omitempty"`
	ViaBot           bool     `json:"via_bot,omitempty"`
	Forwarded        bool     `json:"forwarded,omitempty"`
	ReplyToMessageID int64    `json:"reply_to_message_id,omitempty"`
	Entities         []Entity `json:"entities,omitempty"`
	Media            *Media   `json:"media,omitempty"`
}

type Callback struct {
	ID        string   `json:"id"`
	SenderID  int64    `json:"sender_id"`
	ChatID    int64    `json:"chat_id,omitempty"`
	MessageID int64    `json:"message_id,omitempty"`
	Data      string   `json:"data,omitempty"`
	Message   *Message `json:"message,omitempty"`
}

type InlineQuery struct {
	ID       string `json:"id"`
	SenderID int64  `json:"sender_id"`
	Query    string `json:"query,omitempty"`
	Offset   string `json:"offset,omitempty"`
}

type Update struct {
	ID             string       `json:"id"`
	Kind           UpdateKind   `json:"kind"`
	OccurredUnixMS int64        `json:"occurred_unix_ms"`
	Message        *Message     `json:"message,omitempty"`
	DeletedIDs     []int64      `json:"deleted_ids,omitempty"`
	Callback       *Callback    `json:"callback,omitempty"`
	InlineQuery    *InlineQuery `json:"inline_query,omitempty"`
}

type Command struct {
	Name    string   `json:"name"`
	Prefix  string   `json:"prefix"`
	RawArgs string   `json:"raw_args,omitempty"`
	Args    []string `json:"args,omitempty"`
	Message Message  `json:"message"`
}

type InlineButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data,omitempty"`
	URL          string `json:"url,omitempty"`
	SwitchQuery  string `json:"switch_query,omitempty"`
}

type InlineResult struct {
	ID          string           `json:"id"`
	Type        string           `json:"type"`
	Title       string           `json:"title,omitempty"`
	Description string           `json:"description,omitempty"`
	Text        string           `json:"text,omitempty"`
	Buttons     [][]InlineButton `json:"buttons,omitempty"`
}

type ClientOperation struct {
	Name      string `json:"name"`
	PeerID    int64  `json:"peer_id,omitempty"`
	MessageID int64  `json:"message_id,omitempty"`
	UserID    int64  `json:"user_id,omitempty"`
	Value     string `json:"value,omitempty"`
}

type Action struct {
	ID               string           `json:"id"`
	Kind             ActionKind       `json:"kind"`
	ChatID           int64            `json:"chat_id,omitempty"`
	MessageID        int64            `json:"message_id,omitempty"`
	ReplyToMessageID int64            `json:"reply_to_message_id,omitempty"`
	Text             string           `json:"text,omitempty"`
	ParseMode        string           `json:"parse_mode,omitempty"`
	CallbackID       string           `json:"callback_id,omitempty"`
	InlineQueryID    string           `json:"inline_query_id,omitempty"`
	Buttons          [][]InlineButton `json:"buttons,omitempty"`
	InlineResults    []InlineResult   `json:"inline_results,omitempty"`
	ClientOperation  *ClientOperation `json:"client_operation,omitempty"`
}

type ActionResult struct {
	ActionID string   `json:"action_id"`
	Success  bool     `json:"success"`
	Message  *Message `json:"message,omitempty"`
	Error    *Error   `json:"error,omitempty"`
}

type HandshakeRequest struct {
	SupportedVersions []string `json:"supported_versions"`
	AuthToken         string   `json:"auth_token"`
	InstanceID        string   `json:"instance_id"`
	HostVersion       string   `json:"host_version,omitempty"`
	Nonce             string   `json:"nonce"`
	MaxFrameBytes     uint32   `json:"max_frame_bytes"`
}

type HandshakeResponse struct {
	SelectedVersion string         `json:"selected_version"`
	Nonce           string         `json:"nonce"`
	MaxFrameBytes   uint32         `json:"max_frame_bytes"`
	Metadata        PluginMetadata `json:"metadata"`
}

type LifecycleRequest struct {
	Phase  LifecyclePhase `json:"phase"`
	Config []ConfigValue  `json:"config,omitempty"`
}

type LifecycleResponse struct {
	State   string   `json:"state"`
	Actions []Action `json:"actions,omitempty"`
}

type DispatchRequest struct {
	Update  Update   `json:"update"`
	Command *Command `json:"command,omitempty"`
}

type DispatchResponse struct {
	Actions []Action `json:"actions,omitempty"`
}

type HealthRequest struct {
	Detailed bool `json:"detailed,omitempty"`
}

type HealthResponse struct {
	Ready    bool              `json:"ready"`
	Status   string            `json:"status"`
	Metrics  map[string]uint64 `json:"metrics,omitempty"`
	UptimeMS int64             `json:"uptime_ms,omitempty"`
}

type StorageOperation string

const (
	StorageGet    StorageOperation = "get"
	StorageSet    StorageOperation = "set"
	StorageDelete StorageOperation = "delete"
)

type StorageRequest struct {
	Operation StorageOperation `json:"operation"`
	Namespace string           `json:"namespace"`
	Key       string           `json:"key"`
	Value     json.RawMessage  `json:"value,omitempty"`
}

type StorageResult struct {
	Found bool            `json:"found"`
	Value json.RawMessage `json:"value,omitempty"`
}

type ActionBatchRequest struct {
	Actions []Action `json:"actions"`
}

type ActionBatchResponse struct {
	Results []ActionResult `json:"results"`
}

type ClientRequest struct {
	Operation ClientOperation `json:"operation"`
}

type ClientResult struct {
	Message *Message        `json:"message,omitempty"`
	Value   json.RawMessage `json:"value,omitempty"`
}

func MarshalPayload(value any) (json.RawMessage, error) {
	if value == nil {
		return nil, nil
	}
	b, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal payload: %w", err)
	}
	return b, nil
}

func DecodePayload(payload json.RawMessage, dst any) error {
	if dst == nil {
		return nil
	}
	if len(payload) == 0 {
		return fmt.Errorf("%w: missing payload", ErrInvalidDTO)
	}
	dec := json.NewDecoder(bytes.NewReader(payload))
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("%w: decode payload: %v", ErrInvalidDTO, err)
	}
	var trailing any
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("%w: trailing payload data", ErrInvalidDTO)
		}
		return fmt.Errorf("%w: trailing payload data: %v", ErrInvalidDTO, err)
	}
	return nil
}

func ValidateRequest(r Request) error {
	if r.Protocol != Protocol {
		return invalid("protocol", "must be %q", Protocol)
	}
	if r.RequestID == 0 {
		return invalid("request_id", "must be non-zero")
	}
	if r.Kind != KindRequest {
		return invalid("kind", "must be %q", KindRequest)
	}
	if r.DeadlineUnixMS <= 0 {
		return invalid("deadline_unix_ms", "must be positive")
	}
	switch r.Method {
	case MethodHandshake, MethodConfigure, MethodStart, MethodStop, MethodHealth, MethodDispatch,
		MethodHostActions, MethodHostStorage, MethodHostClient:
	default:
		return invalid("method", "unknown method %q", r.Method)
	}
	if len(r.Payload) > int(DefaultMaxFrameSize) {
		return invalid("payload", "exceeds protocol payload limit")
	}
	return nil
}

func ValidateResponse(r Response) error {
	if r.Protocol != Protocol {
		return invalid("protocol", "must be %q", Protocol)
	}
	if r.RequestID == 0 {
		return invalid("request_id", "must be non-zero")
	}
	if r.Kind != KindResponse {
		return invalid("kind", "must be %q", KindResponse)
	}
	if r.DeadlineUnixMS <= 0 {
		return invalid("deadline_unix_ms", "must be positive")
	}
	if r.Error != nil && len(r.Result) != 0 {
		return invalid("result", "must be empty when error is present")
	}
	if r.Error != nil {
		return r.Error.Validate()
	}
	return nil
}

func (e Error) Validate() error {
	if e.Code == "" {
		return invalid("error.code", "is required")
	}
	if err := validString("error.message", e.Message, true); err != nil {
		return err
	}
	if len(e.Details) > MaxCollectionItems {
		return invalid("error.details", "has too many entries")
	}
	for key, value := range e.Details {
		if err := validString("error.details key", key, true); err != nil {
			return err
		}
		if err := validString("error.details value", value, false); err != nil {
			return err
		}
	}
	return nil
}

func (m PluginMetadata) Validate() error {
	requiredStrings := []struct {
		field string
		value string
	}{
		{field: "metadata.id", value: m.ID},
		{field: "metadata.name", value: m.Name},
		{field: "metadata.version", value: m.Version},
		{field: "metadata.protocol_version", value: m.ProtocolVersion},
	}
	for _, field := range requiredStrings {
		if err := validString(field.field, field.value, true); err != nil {
			return err
		}
	}
	for field, value := range map[string]string{
		"metadata.description": m.Description,
		"metadata.author":      m.Author,
	} {
		if err := validString(field, value, false); err != nil {
			return err
		}
	}
	if m.ProtocolVersion != Version {
		return invalid("metadata.protocol_version", "must be %q", Version)
	}
	if len(m.Commands)+len(m.Watchers) > MaxHandlers {
		return invalid("metadata handlers", "exceed limit %d", MaxHandlers)
	}
	if len(m.Config) > MaxCollectionItems || len(m.Capabilities) > MaxCollectionItems {
		return invalid("metadata", "collection exceeds limit %d", MaxCollectionItems)
	}
	seenCapabilities := make(map[Capability]struct{}, len(m.Capabilities))
	for index, capability := range m.Capabilities {
		if !knownCapability(capability) {
			return invalid(fmt.Sprintf("metadata.capabilities[%d]", index), "unknown capability %q", capability)
		}
		if _, exists := seenCapabilities[capability]; exists {
			return invalid("metadata.capabilities", "duplicate capability %q", capability)
		}
		seenCapabilities[capability] = struct{}{}
	}

	seenCommands := make(map[string]struct{}, len(m.Commands))
	totalAliases := 0
	for i, command := range m.Commands {
		if err := validIdentifier(fmt.Sprintf("commands[%d].name", i), command.Name); err != nil {
			return err
		}
		if err := validString(fmt.Sprintf("commands[%d].description", i), command.Description, false); err != nil {
			return err
		}
		commandKey := strings.ToLower(command.Name)
		if _, ok := seenCommands[commandKey]; ok {
			return invalid("metadata.commands", "duplicate command %q", command.Name)
		}
		seenCommands[commandKey] = struct{}{}
		if len(command.Aliases) > MaxCollectionItems {
			return invalid("metadata.commands.aliases", "has too many entries")
		}
		if totalAliases > MaxCollectionItems-len(command.Aliases) {
			return invalid("metadata.commands.aliases", "total exceeds limit %d", MaxCollectionItems)
		}
		totalAliases += len(command.Aliases)
		for aliasIndex, alias := range command.Aliases {
			if err := validIdentifier(fmt.Sprintf("commands[%d].aliases[%d]", i, aliasIndex), alias); err != nil {
				return err
			}
			aliasKey := strings.ToLower(alias)
			if _, exists := seenCommands[aliasKey]; exists {
				return invalid("metadata.commands", "duplicate command or alias %q", alias)
			}
			seenCommands[aliasKey] = struct{}{}
		}
	}
	seenWatchers := make(map[string]struct{}, len(m.Watchers))
	for i, watcher := range m.Watchers {
		if err := validIdentifier(fmt.Sprintf("watchers[%d].id", i), watcher.ID); err != nil {
			return err
		}
		if err := validString(fmt.Sprintf("watchers[%d].description", i), watcher.Description, false); err != nil {
			return err
		}
		if _, ok := seenWatchers[watcher.ID]; ok {
			return invalid("metadata.watchers", "duplicate watcher %q", watcher.ID)
		}
		seenWatchers[watcher.ID] = struct{}{}
		if err := watcher.Filter.Validate(); err != nil {
			return invalid(fmt.Sprintf("watchers[%d].filter", i), "%v", err)
		}
	}
	seenConfig := make(map[string]struct{}, len(m.Config))
	for index, spec := range m.Config {
		if err := validIdentifier(fmt.Sprintf("config[%d].key", index), spec.Key); err != nil {
			return err
		}
		if !knownConfigType(spec.Type) {
			return invalid(fmt.Sprintf("config[%d].type", index), "unknown config type %q", spec.Type)
		}
		if err := validString(fmt.Sprintf("config[%d].description", index), spec.Description, false); err != nil {
			return err
		}
		if _, exists := seenConfig[spec.Key]; exists {
			return invalid("metadata.config", "duplicate key %q", spec.Key)
		}
		seenConfig[spec.Key] = struct{}{}
		if len(spec.Default) > MaxStringBytes {
			return invalid(fmt.Sprintf("config[%d].default", index), "exceeds %d bytes", MaxStringBytes)
		}
		if len(spec.Default) != 0 && !json.Valid(spec.Default) {
			return invalid(fmt.Sprintf("config[%d].default", index), "must be one JSON value")
		}
	}
	return nil
}

func (f WatcherFilter) Validate() error {
	if len(f.Kinds) > MaxCollectionItems || len(f.ChatIDs) > MaxCollectionItems || len(f.SenderIDs) > MaxCollectionItems {
		return invalid("watcher.filter", "collection exceeds limit %d", MaxCollectionItems)
	}
	if err := validString("watcher.filter.text_prefix", f.TextPrefix, false); err != nil {
		return err
	}
	if len(f.TextPrefix) > MaxWatcherTextBytes {
		return invalid("watcher.filter.text_prefix", "exceeds %d bytes", MaxWatcherTextBytes)
	}
	seenKinds := make(map[UpdateKind]struct{}, len(f.Kinds))
	for index, kind := range f.Kinds {
		if !knownUpdateKind(kind) {
			return invalid(fmt.Sprintf("watcher.filter.kinds[%d]", index), "unknown update kind %q", kind)
		}
		if _, exists := seenKinds[kind]; exists {
			return invalid("watcher.filter.kinds", "duplicate update kind %q", kind)
		}
		seenKinds[kind] = struct{}{}
	}
	if err := uniqueInt64s("watcher.filter.chat_ids", f.ChatIDs); err != nil {
		return err
	}
	return uniqueInt64s("watcher.filter.sender_ids", f.SenderIDs)
}

func (h HandshakeRequest) Validate() error {
	if len(h.SupportedVersions) == 0 || len(h.SupportedVersions) > 16 {
		return invalid("supported_versions", "must contain 1..16 versions")
	}
	if err := validString("auth_token", h.AuthToken, true); err != nil {
		return err
	}
	if err := validIdentifier("instance_id", h.InstanceID); err != nil {
		return err
	}
	if err := validString("nonce", h.Nonce, true); err != nil {
		return err
	}
	if h.MaxFrameBytes == 0 {
		return invalid("max_frame_bytes", "must be positive")
	}
	return nil
}

func (h HandshakeResponse) Validate() error {
	if h.SelectedVersion != Version {
		return invalid("selected_version", "must be %q", Version)
	}
	if err := validString("nonce", h.Nonce, true); err != nil {
		return err
	}
	if h.MaxFrameBytes == 0 {
		return invalid("max_frame_bytes", "must be positive")
	}
	return h.Metadata.Validate()
}

func (d DispatchRequest) Validate() error {
	if err := d.Update.Validate(); err != nil {
		return err
	}
	if d.Command != nil {
		if err := validIdentifier("command.name", d.Command.Name); err != nil {
			return err
		}
		if len(d.Command.Args) > MaxCollectionItems {
			return invalid("command.args", "has too many entries")
		}
	}
	return nil
}

func (u Update) Validate() error {
	if err := validString("update.id", u.ID, true); err != nil {
		return err
	}
	switch u.Kind {
	case UpdateMessage, UpdateEditedMessage:
		if u.Message == nil {
			return invalid("update.message", "is required for %q", u.Kind)
		}
	case UpdateDeleted:
		if len(u.DeletedIDs) == 0 || len(u.DeletedIDs) > MaxCollectionItems {
			return invalid("update.deleted_ids", "must contain 1..%d values", MaxCollectionItems)
		}
	case UpdateCallback:
		if u.Callback == nil {
			return invalid("update.callback", "is required")
		}
	case UpdateInlineQuery:
		if u.InlineQuery == nil {
			return invalid("update.inline_query", "is required")
		}
	default:
		return invalid("update.kind", "unknown kind %q", u.Kind)
	}
	if u.Message != nil {
		if err := validString("message.text", u.Message.Text, false); err != nil {
			return err
		}
		if len(u.Message.Entities) > MaxCollectionItems {
			return invalid("message.entities", "has too many entries")
		}
		if u.Message.Media != nil {
			if err := validString("message.media.kind", u.Message.Media.Kind, true); err != nil {
				return err
			}
			if u.Message.Media.Size < 0 {
				return invalid("message.media.size", "must not be negative")
			}
			for field, value := range map[string]string{
				"message.media.file_id":   u.Message.Media.FileID,
				"message.media.file_name": u.Message.Media.FileName,
				"message.media.mime_type": u.Message.Media.MIMEType,
			} {
				if err := validString(field, value, false); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (d DispatchResponse) Validate() error {
	if len(d.Actions) > MaxActions {
		return invalid("actions", "exceed limit %d", MaxActions)
	}
	seen := make(map[string]struct{}, len(d.Actions))
	for i, action := range d.Actions {
		if err := action.Validate(); err != nil {
			return invalid(fmt.Sprintf("actions[%d]", i), "%v", err)
		}
		if _, exists := seen[action.ID]; exists {
			return invalid("actions", "duplicate action id %q", action.ID)
		}
		seen[action.ID] = struct{}{}
	}
	return nil
}

func (a Action) Validate() error {
	if err := validString("action.id", a.ID, true); err != nil {
		return err
	}
	for field, value := range map[string]string{
		"action.text":            a.Text,
		"action.parse_mode":      a.ParseMode,
		"action.callback_id":     a.CallbackID,
		"action.inline_query_id": a.InlineQueryID,
	} {
		if err := validString(field, value, false); err != nil {
			return err
		}
	}
	switch a.Kind {
	case ActionReply, ActionSend:
		if a.ChatID == 0 {
			return invalid("action.chat_id", "is required")
		}
	case ActionEdit, ActionDelete:
		if a.ChatID == 0 || a.MessageID == 0 {
			return invalid("action", "chat_id and message_id are required")
		}
	case ActionInlineAnswer:
		if (a.InlineQueryID == "") == (a.CallbackID == "") {
			return invalid("action", "exactly one of inline_query_id or callback_id is required")
		}
	case ActionClient:
		if a.ClientOperation == nil {
			return invalid("action.client_operation", "is required")
		}
		if err := validIdentifier("action.client_operation.name", a.ClientOperation.Name); err != nil {
			return err
		}
		if err := validString("action.client_operation.value", a.ClientOperation.Value, false); err != nil {
			return err
		}
	default:
		return invalid("action.kind", "unknown kind %q", a.Kind)
	}
	if a.Kind != ActionClient && a.ClientOperation != nil {
		return invalid("action.client_operation", "is only valid for kind %q", ActionClient)
	}
	if len(a.Buttons) > MaxCollectionItems || len(a.InlineResults) > MaxCollectionItems {
		return invalid("action", "collection exceeds limit %d", MaxCollectionItems)
	}
	budget := inlineBudget{}
	if err := validateInlineButtons("action.buttons", a.Buttons, &budget); err != nil {
		return err
	}
	seenResults := make(map[string]struct{}, len(a.InlineResults))
	for resultIndex, result := range a.InlineResults {
		prefix := fmt.Sprintf("action.inline_results[%d]", resultIndex)
		if err := validString(prefix+".id", result.ID, true); err != nil {
			return err
		}
		if _, exists := seenResults[result.ID]; exists {
			return invalid("action.inline_results", "duplicate result id %q", result.ID)
		}
		seenResults[result.ID] = struct{}{}
		if err := validIdentifier(prefix+".type", result.Type); err != nil {
			return err
		}
		for field, value := range map[string]string{
			prefix + ".title":       result.Title,
			prefix + ".description": result.Description,
			prefix + ".text":        result.Text,
		} {
			if err := validString(field, value, false); err != nil {
				return err
			}
		}
		if err := validateInlineButtons(prefix+".buttons", result.Buttons, &budget); err != nil {
			return err
		}
	}
	return nil
}

type inlineBudget struct {
	rows    int
	buttons int
}

func validateInlineButtons(field string, rows [][]InlineButton, budget *inlineBudget) error {
	if len(rows) > MaxCollectionItems-budget.rows {
		return invalid(field, "total rows exceed limit %d", MaxCollectionItems)
	}
	budget.rows += len(rows)
	for rowIndex, row := range rows {
		if len(row) == 0 {
			return invalid(fmt.Sprintf("%s[%d]", field, rowIndex), "must not be empty")
		}
		if len(row) > MaxCollectionItems-budget.buttons {
			return invalid(field, "total buttons exceed limit %d", MaxCollectionItems)
		}
		budget.buttons += len(row)
	}
	for rowIndex, row := range rows {
		for buttonIndex, button := range row {
			prefix := fmt.Sprintf("%s[%d][%d]", field, rowIndex, buttonIndex)
			if err := validString(prefix+".text", button.Text, true); err != nil {
				return err
			}
			for nestedField, value := range map[string]string{
				prefix + ".callback_data": button.CallbackData,
				prefix + ".url":           button.URL,
				prefix + ".switch_query":  button.SwitchQuery,
			} {
				if err := validString(nestedField, value, false); err != nil {
					return err
				}
			}
			targets := 0
			for _, value := range []string{button.CallbackData, button.URL, button.SwitchQuery} {
				if value != "" {
					targets++
				}
			}
			if targets != 1 {
				return invalid(prefix, "must set exactly one of callback_data, url, or switch_query")
			}
		}
	}
	return nil
}

func knownCapability(capability Capability) bool {
	switch capability {
	case CapabilityCommands, CapabilityWatchers, CapabilityInline, CapabilityStorage, CapabilityClient:
		return true
	default:
		return false
	}
}

func knownUpdateKind(kind UpdateKind) bool {
	switch kind {
	case UpdateMessage, UpdateEditedMessage, UpdateDeleted, UpdateCallback, UpdateInlineQuery:
		return true
	default:
		return false
	}
}

func knownConfigType(configType ConfigType) bool {
	switch configType {
	case ConfigString, ConfigInteger, ConfigNumber, ConfigBoolean, ConfigStrings, ConfigJSON:
		return true
	default:
		return false
	}
}

func uniqueInt64s(field string, values []int64) error {
	seen := make(map[int64]struct{}, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			return invalid(field, "duplicate value %d", value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func invalid(field, format string, args ...any) error {
	return fmt.Errorf("%w: %s: %s", ErrInvalidDTO, field, fmt.Sprintf(format, args...))
}

func validString(field, value string, required bool) error {
	if required && strings.TrimSpace(value) == "" {
		return invalid(field, "is required")
	}
	if !utf8.ValidString(value) {
		return invalid(field, "must be valid UTF-8")
	}
	if len(value) > MaxStringBytes {
		return invalid(field, "exceeds %d bytes", MaxStringBytes)
	}
	return nil
}

func validIdentifier(field, value string) error {
	if err := validString(field, value, true); err != nil {
		return err
	}
	if len(value) > 128 {
		return invalid(field, "exceeds 128 bytes")
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.' {
			continue
		}
		return invalid(field, "contains unsupported character %q", r)
	}
	return nil
}
