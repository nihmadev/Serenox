package v1

import "encoding/json"

type EventDeliveryClass string

const (
	EventDurable   EventDeliveryClass = "durable"
	EventEphemeral EventDeliveryClass = "ephemeral"
)

type EventSourcePolicy string

const (
	EventSourceAnyPublic EventSourcePolicy = "any_public_plugin"
	EventSourceExact     EventSourcePolicy = "exact_plugin"
	EventSourceCore      EventSourcePolicy = "core"
)

type EventPublication struct {
	Type            string             `json:"type"`
	Version         int                `json:"version"`
	Description     string             `json:"description,omitempty"`
	MaxPayloadBytes int                `json:"max_payload_bytes"`
	DeliveryClass   EventDeliveryClass `json:"delivery_class"`
	Schema          string             `json:"schema,omitempty"`
}

type EventRetryPolicy struct {
	MaxAttempts    int   `json:"max_attempts"`
	InitialDelayMS int64 `json:"initial_delay_ms"`
	MaxDelayMS     int64 `json:"max_delay_ms"`
}

type EventSubscription struct {
	Type          string             `json:"type"`
	Version       int                `json:"version"`
	HandlerID     string             `json:"handler_id"`
	SourcePolicy  EventSourcePolicy  `json:"source_policy"`
	SourcePlugin  string             `json:"source_plugin,omitempty"`
	DeliveryClass EventDeliveryClass `json:"delivery_class"`
	MaxRuntimeMS  int64              `json:"max_runtime_ms"`
	Retry         EventRetryPolicy   `json:"retry"`
	AcceptReplay  bool               `json:"accept_replay,omitempty"`
}

// EventPublishRequest contains only publisher-controlled fields. Identity,
// account, routing, attempt and acceptance timestamps are always host-owned.
type EventPublishRequest struct {
	Type           string          `json:"type"`
	Version        int             `json:"version"`
	OccurredUnixMS int64           `json:"occurred_unix_ms,omitempty"`
	CorrelationID  string          `json:"correlation_id,omitempty"`
	CausationID    string          `json:"causation_id,omitempty"`
	TraceID        string          `json:"trace_id,omitempty"`
	DedupeKey      string          `json:"dedupe_key,omitempty"`
	ContentType    string          `json:"content_type,omitempty"`
	Payload        json.RawMessage `json:"payload"`
}

type EventEnvelope struct {
	ID             string          `json:"id"`
	Type           string          `json:"type"`
	Version        int             `json:"version"`
	Source         string          `json:"source"`
	AccountID      int64           `json:"account_id"`
	OccurredUnixMS int64           `json:"occurred_unix_ms"`
	AcceptedUnixMS int64           `json:"accepted_unix_ms"`
	CorrelationID  string          `json:"correlation_id,omitempty"`
	CausationID    string          `json:"causation_id,omitempty"`
	TraceID        string          `json:"trace_id"`
	DedupeKey      string          `json:"dedupe_key,omitempty"`
	ContentType    string          `json:"content_type"`
	Attempt        int             `json:"attempt"`
	Hop            int             `json:"hop"`
	Payload        json.RawMessage `json:"payload"`
}

type EventDelivery struct {
	SubscriptionID string        `json:"subscription_id"`
	Envelope       EventEnvelope `json:"envelope"`
	PreviousError  string        `json:"previous_error,omitempty"`
}

type EventHandlerOutcome string

const (
	EventAcknowledged EventHandlerOutcome = "acknowledged"
	EventRetryable    EventHandlerOutcome = "retryable_failure"
	EventTerminal     EventHandlerOutcome = "terminal_failure"
	EventAmbiguous    EventHandlerOutcome = "ambiguous_outcome"
)

type EventHandlerResult struct {
	Outcome EventHandlerOutcome `json:"outcome"`
	Code    string              `json:"code,omitempty"`
	Message string              `json:"message,omitempty"`
}

type EventPublishResult struct {
	EventID            string             `json:"event_id"`
	Accepted           bool               `json:"accepted"`
	Deduplicated       bool               `json:"deduplicated,omitempty"`
	MatchedSubscribers int                `json:"matched_subscribers"`
	DeliveryClass      EventDeliveryClass `json:"delivery_class"`
	Warnings           []string           `json:"warnings,omitempty"`
}

func EventPermission(operation string, eventType string, version int) string {
	return "event." + operation + "." + eventType + ".v" + itoaPositive(version)
}
