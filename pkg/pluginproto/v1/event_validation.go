package v1

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
)

func (r *EventPublishRequest) UnmarshalJSON(data []byte) error {
	type plain EventPublishRequest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var value plain
	if err := decoder.Decode(&value); err != nil {
		return fmt.Errorf("%w: event publish request: %v", ErrInvalidDTO, err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return fmt.Errorf("%w: event publish request has trailing data", ErrInvalidDTO)
	}
	*r = EventPublishRequest(value)
	return nil
}

func itoaPositive(value int) string { return strconv.Itoa(value) }

func validEventType(field, value string) error {
	if len(value) < 3 || len(value) > 128 || strings.ToLower(value) != value || strings.HasPrefix(value, "core.") {
		return invalid(field, "must be a lowercase, non-core namespaced type")
	}
	parts := strings.Split(value, ".")
	if len(parts) < 2 || len(parts) > 8 {
		return invalid(field, "must contain 2..8 exact namespace segments")
	}
	for _, part := range parts {
		if part == "" || part[0] < 'a' || part[0] > 'z' {
			return invalid(field, "segments must start with a lowercase letter")
		}
		for _, r := range part {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
				continue
			}
			return invalid(field, "contains unsupported character %q", r)
		}
	}
	return nil
}

func (p EventPublication) Validate() error {
	if err := validEventType("publication.type", p.Type); err != nil {
		return err
	}
	if p.Version < 1 || p.Version > 65535 {
		return invalid("publication.version", "must be between 1 and 65535")
	}
	if err := validString("publication.description", p.Description, false); err != nil {
		return err
	}
	if p.MaxPayloadBytes < 2 || p.MaxPayloadBytes > MaxEventPayloadBytes {
		return invalid("publication.max_payload_bytes", "is outside bounded range")
	}
	if p.DeliveryClass != EventDurable && p.DeliveryClass != EventEphemeral {
		return invalid("publication.delivery_class", "is unknown")
	}
	return validString("publication.schema", p.Schema, false)
}

func (s EventSubscription) Validate() error {
	if err := validEventType("subscription.type", s.Type); err != nil {
		return err
	}
	if s.Version < 1 || s.Version > 65535 {
		return invalid("subscription.version", "must be positive")
	}
	if err := validIdentifier("subscription.handler_id", s.HandlerID); err != nil {
		return err
	}
	if s.SourcePolicy != EventSourceAnyPublic && s.SourcePolicy != EventSourceExact && s.SourcePolicy != EventSourceCore {
		return invalid("subscription.source_policy", "is unknown")
	}
	if s.SourcePolicy == EventSourceExact {
		if err := validIdentifier("subscription.source_plugin", s.SourcePlugin); err != nil {
			return err
		}
	} else if s.SourcePlugin != "" {
		return invalid("subscription.source_plugin", "requires exact_plugin source policy")
	}
	if s.DeliveryClass != EventDurable && s.DeliveryClass != EventEphemeral {
		return invalid("subscription.delivery_class", "is unknown")
	}
	if s.MaxRuntimeMS < 1 || s.MaxRuntimeMS > 86400000 {
		return invalid("subscription.max_runtime_ms", "must be between 1 and 86400000")
	}
	if s.Retry.MaxAttempts < 1 || s.Retry.MaxAttempts > MaxEventAttempts || s.Retry.InitialDelayMS < 0 || s.Retry.MaxDelayMS < s.Retry.InitialDelayMS || s.Retry.MaxDelayMS > 86400000 {
		return invalid("subscription.retry", "is outside bounded range")
	}
	if s.DeliveryClass == EventEphemeral && (s.Retry.MaxAttempts != 1 || s.AcceptReplay) {
		return invalid("subscription", "ephemeral delivery cannot retry or replay")
	}
	return nil
}

func (r EventPublishRequest) Validate() error {
	if err := validEventType("event.type", r.Type); err != nil {
		return err
	}
	if r.Version < 1 || r.Version > 65535 {
		return invalid("event.version", "must be positive")
	}
	if r.OccurredUnixMS < 0 {
		return invalid("event.occurred_unix_ms", "must not be negative")
	}
	for field, value := range map[string]string{"event.correlation_id": r.CorrelationID, "event.causation_id": r.CausationID, "event.trace_id": r.TraceID, "event.dedupe_key": r.DedupeKey} {
		if err := validEventToken(field, value); err != nil {
			return err
		}
	}
	if r.ContentType != "" && r.ContentType != "application/json" {
		return invalid("event.content_type", "must be application/json")
	}
	return validateEventPayload(r.Payload, MaxEventPayloadBytes)
}

func validEventToken(field, value string) error {
	if value == "" {
		return nil
	}
	if len(value) > MaxIdempotencyKeyBytes {
		return invalid(field, "exceeds %d bytes", MaxIdempotencyKeyBytes)
	}
	return validString(field, value, false)
}

func validateEventPayload(raw json.RawMessage, maximum int) error {
	if len(raw) < 2 || len(raw) > maximum {
		return invalid("event.payload", "must contain bounded JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return invalid("event.payload", "invalid JSON: %v", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return invalid("event.payload", "contains trailing JSON")
	}
	fields := 0
	if err := walkEventJSON(value, 1, &fields); err != nil {
		return err
	}
	return nil
}

func walkEventJSON(value any, depth int, fields *int) error {
	if depth > MaxEventPayloadDepth {
		return invalid("event.payload", "exceeds depth %d", MaxEventPayloadDepth)
	}
	switch typed := value.(type) {
	case map[string]any:
		if len(typed) > MaxEventCollectionItems {
			return invalid("event.payload", "object exceeds item limit")
		}
		*fields += len(typed)
		if *fields > MaxEventPayloadFields {
			return invalid("event.payload", "exceeds field limit")
		}
		for key, child := range typed {
			if key == "" || len(key) > 128 {
				return invalid("event.payload", "has invalid field name")
			}
			if err := walkEventJSON(child, depth+1, fields); err != nil {
				return err
			}
		}
	case []any:
		if len(typed) > MaxEventCollectionItems {
			return invalid("event.payload", "array exceeds item limit")
		}
		for _, child := range typed {
			if err := walkEventJSON(child, depth+1, fields); err != nil {
				return err
			}
		}
	case string:
		if len(typed) > MaxEventStringBytes {
			return invalid("event.payload", "string exceeds %d bytes", MaxEventStringBytes)
		}
	case json.Number:
		if _, err := typed.Float64(); err != nil {
			return invalid("event.payload", "invalid number")
		}
	case bool, nil:
	default:
		return fmt.Errorf("%w: event.payload: unsupported JSON value", ErrInvalidDTO)
	}
	return nil
}

func (e EventEnvelope) Validate() error {
	if err := validIdentifier("event.id", e.ID); err != nil {
		return err
	}
	if err := validEventType("event.type", e.Type); err != nil {
		return err
	}
	if err := validIdentifier("event.source", e.Source); err != nil {
		return err
	}
	if e.AccountID == 0 || e.OccurredUnixMS <= 0 || e.AcceptedUnixMS <= 0 || e.Attempt < 1 || e.Hop < 0 || e.Hop > MaxEventHop {
		return invalid("event", "host metadata is invalid")
	}
	if e.Version < 1 || e.Version > 65535 {
		return invalid("event.version", "must be positive")
	}
	for field, value := range map[string]string{"event.correlation_id": e.CorrelationID, "event.causation_id": e.CausationID, "event.trace_id": e.TraceID, "event.dedupe_key": e.DedupeKey} {
		if err := validEventToken(field, value); err != nil {
			return err
		}
	}
	if e.TraceID == "" || e.ContentType != "application/json" {
		return invalid("event", "trace_id and application/json content type are required")
	}
	return validateEventPayload(e.Payload, MaxEventPayloadBytes)
}

func (r EventPublishResult) Validate() error {
	if err := validIdentifier("event_result.event_id", r.EventID); err != nil {
		return err
	}
	if !r.Accepted || r.MatchedSubscribers < 0 || r.MatchedSubscribers > MaxEventFanout {
		return invalid("event_result", "acceptance or fan-out is invalid")
	}
	if r.DeliveryClass != EventDurable && r.DeliveryClass != EventEphemeral {
		return invalid("event_result.delivery_class", "is unknown")
	}
	if len(r.Warnings) > 16 {
		return invalid("event_result.warnings", "exceed limit")
	}
	for _, warning := range r.Warnings {
		if len(warning) > 256 {
			return invalid("event_result.warnings", "warning is too long")
		}
	}
	return nil
}

func (r EventHandlerResult) Validate() error {
	switch r.Outcome {
	case EventAcknowledged, EventRetryable, EventTerminal, EventAmbiguous:
	default:
		return invalid("event_result.outcome", "is unknown")
	}
	if err := validString("event_result.code", r.Code, false); err != nil {
		return err
	}
	if len(r.Message) > 1024 {
		return invalid("event_result.message", "exceeds 1024 bytes")
	}
	return nil
}
