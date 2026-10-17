package eventbus

import (
	"encoding/json"
	"fmt"

	protocol "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

// CoreEventFromUpdate converts only the finite, transport-neutral protocol
// update catalog. No raw Telegram library object can cross this signature.
func CoreEventFromUpdate(update protocol.Update) (protocol.EventPublishRequest, error) {
	if err := update.Validate(); err != nil {
		return protocol.EventPublishRequest{}, err
	}
	var eventType string
	payload := map[string]any{"update_id": update.ID, "occurred_unix_ms": update.OccurredUnixMS}
	switch update.Kind {
	case protocol.UpdateMessage, protocol.UpdateEditedMessage:
		if update.Kind == protocol.UpdateMessage {
			eventType = "telegram.message.received"
		} else {
			eventType = "telegram.message.edited"
		}
		payload["message"] = update.Message
	case protocol.UpdateDeleted:
		eventType = "telegram.message.deleted"
		payload["message_ids"] = update.DeletedIDs
	case protocol.UpdateCallback:
		eventType = "telegram.callback.received"
		payload["callback"] = update.Callback
	case protocol.UpdateInlineQuery:
		eventType = "telegram.inline_query.received"
		payload["inline_query"] = update.InlineQuery
	default:
		return protocol.EventPublishRequest{}, fmt.Errorf("eventbus: update kind %q is not in the core event catalog", update.Kind)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return protocol.EventPublishRequest{}, err
	}
	request := protocol.EventPublishRequest{Type: eventType, Version: 1, OccurredUnixMS: update.OccurredUnixMS, ContentType: "application/json", Payload: raw}
	return request, request.Validate()
}

func CoreLifecycleEvent(pluginID, state string, occurredUnixMS int64) (protocol.EventPublishRequest, error) {
	if pluginID == "" || state == "" {
		return protocol.EventPublishRequest{}, fmt.Errorf("eventbus: lifecycle identity and state are required")
	}
	raw, _ := json.Marshal(map[string]any{"plugin_id": pluginID, "state": state})
	request := protocol.EventPublishRequest{Type: "plugin.lifecycle.changed", Version: 1, OccurredUnixMS: occurredUnixMS, ContentType: "application/json", Payload: raw}
	return request, request.Validate()
}
