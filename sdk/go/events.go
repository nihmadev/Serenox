package plugin

import (
	"context"
	"encoding/json"
	"time"

	protocol "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

type EventClient struct{ rpc *serverRPC }

func (c *EventClient) Publish(ctx context.Context, request protocol.EventPublishRequest) (protocol.EventPublishResult, error) {
	if err := request.Validate(); err != nil {
		return protocol.EventPublishResult{}, err
	}
	var result protocol.EventPublishResult
	if err := c.rpc.Call(ctx, protocol.MethodHostEvents, request, &result); err != nil {
		return protocol.EventPublishResult{}, err
	}
	if err := result.Validate(); err != nil {
		return protocol.EventPublishResult{}, err
	}
	return result, nil
}

func PublishJSON(ctx context.Context, client *EventClient, eventType string, version int, payload any, dedupeKey string) (protocol.EventPublishResult, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return protocol.EventPublishResult{}, err
	}
	return client.Publish(ctx, protocol.EventPublishRequest{Type: eventType, Version: version, DedupeKey: dedupeKey, ContentType: "application/json", Payload: raw})
}

func EventPublication(eventType string, version, maxPayloadBytes int, class protocol.EventDeliveryClass, description string) protocol.EventPublication {
	return protocol.EventPublication{Type: eventType, Version: version, MaxPayloadBytes: maxPayloadBytes, DeliveryClass: class, Description: description}
}
func EventSubscription(eventType string, version int, handlerID string, class protocol.EventDeliveryClass, runtime, initialDelay time.Duration, attempts int) protocol.EventSubscription {
	return protocol.EventSubscription{Type: eventType, Version: version, HandlerID: handlerID, SourcePolicy: protocol.EventSourceAnyPublic, DeliveryClass: class, MaxRuntimeMS: runtime.Milliseconds(), Retry: protocol.EventRetryPolicy{MaxAttempts: attempts, InitialDelayMS: initialDelay.Milliseconds(), MaxDelayMS: (initialDelay * 32).Milliseconds()}, AcceptReplay: class == protocol.EventDurable}
}

func EventSuccess() protocol.EventHandlerResult {
	return protocol.EventHandlerResult{Outcome: protocol.EventAcknowledged}
}
func EventRetry(code, message string) protocol.EventHandlerResult {
	return protocol.EventHandlerResult{Outcome: protocol.EventRetryable, Code: code, Message: message}
}
func EventReject(code, message string) protocol.EventHandlerResult {
	return protocol.EventHandlerResult{Outcome: protocol.EventTerminal, Code: code, Message: message}
}
func EventOutcomeUnknown(code, message string) protocol.EventHandlerResult {
	return protocol.EventHandlerResult{Outcome: protocol.EventAmbiguous, Code: code, Message: message}
}
