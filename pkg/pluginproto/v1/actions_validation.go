package v1

import (
	"fmt"
	"strings"
)

func (d DispatchResponse) Validate() error {
	if len(d.Events) > MaxEventsPerResponse {
		return invalid("dispatch.events", "exceed limit %d", MaxEventsPerResponse)
	}
	for i, event := range d.Events {
		if err := event.Validate(); err != nil {
			return invalid(fmt.Sprintf("dispatch.events[%d]", i), "%v", err)
		}
	}
	if d.EventResult != nil {
		if err := d.EventResult.Validate(); err != nil {
			return err
		}
	}
	if d.PreSend != nil {
		if len(d.Actions) != 0 {
			return invalid("dispatch response", "pre_send response cannot contain actions")
		}
		if err := d.PreSend.Action.Validate(); err != nil {
			return invalid("pre_send.action", "%v", err)
		}
		if d.PreSend.Action.Kind != ActionSend {
			return invalid("pre_send.action.kind", "must be %q", ActionSend)
		}
		return nil
	}
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

func (r ActionBatchRequest) Validate() error {
	if len(r.Actions) > MaxActions {
		return invalid("actions", "exceed limit %d", MaxActions)
	}
	seen := make(map[string]struct{}, len(r.Actions))
	for i, action := range r.Actions {
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

func (r ActionBatchResponse) Validate() error {
	if len(r.Results) > MaxActions {
		return invalid("results", "exceed limit %d", MaxActions)
	}
	seen := make(map[string]struct{}, len(r.Results))
	for i := range r.Results {
		if err := r.Results[i].Validate(); err != nil {
			return invalid(fmt.Sprintf("results[%d]", i), "%v", err)
		}
		if _, exists := seen[r.Results[i].ActionID]; exists {
			return invalid("results", "duplicate action id %q", r.Results[i].ActionID)
		}
		seen[r.Results[i].ActionID] = struct{}{}
	}
	if r.Stopped && !r.Partial {
		return invalid("stopped", "a stopped batch must be partial")
	}
	return nil
}

func (r ActionResult) Validate() error {
	if err := validString("result.action_id", r.ActionID, true); err != nil {
		return err
	}
	if err := validIdentifier("result.operation", r.Operation); err != nil {
		return err
	}
	status := r.Status
	if status == "" {
		if r.OutcomeUnknown {
			status = ActionResultOutcomeUnknown
		} else if r.Success {
			status = ActionResultSuccess
		} else {
			status = ActionResultFailure
		}
	}
	switch status {
	case ActionResultSuccess, ActionResultFailure, ActionResultSkipped, ActionResultNoop, ActionResultPartial, ActionResultOutcomeUnknown:
	default:
		return invalid("result.status", "unknown status %q", status)
	}
	if r.OutcomeUnknown != (status == ActionResultOutcomeUnknown) && r.OutcomeUnknown {
		return invalid("result.outcome_unknown", "must agree with status")
	}
	if status == ActionResultSuccess && !r.Success {
		return invalid("result.success", "must be true for a successful result")
	}
	if status != ActionResultSuccess && r.Success {
		return invalid("result.success", "must be false for a non-success result")
	}
	if status == ActionResultOutcomeUnknown && r.Error == nil {
		return invalid("result.error", "is required for an unknown outcome")
	}
	if status == ActionResultFailure && r.Error == nil {
		return invalid("result.error", "is required for a failure")
	}
	if status == ActionResultSkipped && r.Error != nil {
		return invalid("result.error", "is not valid for a skipped action")
	}
	if r.Error != nil {
		if err := r.Error.Validate(); err != nil {
			return err
		}
	}
	if r.Profile != nil {
		if err := r.Profile.Validate(); err != nil {
			return err
		}
	}
	if r.FloodWaitSeconds < 0 || r.FloodWaitSeconds > 24*60*60 {
		return invalid("result.flood_wait_seconds", "is outside the bounded range")
	}
	if r.MessageID < 0 || r.DateUnixMS < 0 {
		return invalid("result", "message identifiers and dates must not be negative")
	}
	if r.Message != nil {
		if err := validateMessage(*r.Message, "result.message"); err != nil {
			return err
		}
	}
	if r.Peer != nil {
		if err := r.Peer.Validate(); err != nil {
			return err
		}
	}
	if err := validString("result.media_handle", r.MediaHandle, false); err != nil {
		return err
	}
	if len(r.Metadata) > MaxActionMetadata {
		return invalid("result.metadata", "exceed limit %d", MaxActionMetadata)
	}
	for key, value := range r.Metadata {
		if err := validIdentifier("result.metadata.key", key); err != nil {
			return err
		}
		if err := validString("result.metadata.value", value, false); err != nil {
			return err
		}
		if len(value) > MaxActionMetadataValueBytes {
			return invalid("result.metadata.value", "exceeds %d bytes", MaxActionMetadataValueBytes)
		}
	}
	return nil
}

func (a Action) Validate() error {
	if err := validString("action.id", a.ID, true); err != nil {
		return err
	}
	if err := validString("action.idempotency_key", a.IdempotencyKey, false); err != nil {
		return err
	}
	if len(a.IdempotencyKey) > MaxIdempotencyKeyBytes {
		return invalid("action.idempotency_key", "exceeds %d bytes", MaxIdempotencyKeyBytes)
	}
	for field, value := range map[string]string{
		"action.text":              a.Text,
		"action.parse_mode":        a.ParseMode,
		"action.callback_id":       a.CallbackID,
		"action.inline_query_id":   a.InlineQueryID,
		"action.inline_message_id": a.InlineMessageID,
		"action.callback_text":     a.CallbackText,
		"action.next_offset":       a.NextOffset,
	} {
		if err := validString(field, value, false); err != nil {
			return err
		}
	}
	channel := a.ResponseChannel
	if channel == "" {
		channel = ResponseChannelUserSession
	}
	if channel != ResponseChannelUserSession && channel != ResponseChannelControlBot {
		return invalid("action.response_channel", "unknown channel %q", a.ResponseChannel)
	}
	botAction := a.Kind == ActionBotAnswerInline || a.Kind == ActionBotAnswerCallback || a.Kind == ActionBotSend || a.Kind == ActionBotEdit || a.Kind == ActionBotDelete
	if botAction && channel != ResponseChannelControlBot {
		return invalid("action.response_channel", "must be control_bot for bot action")
	}
	if !botAction && channel == ResponseChannelControlBot {
		return invalid("action.response_channel", "control_bot requires a bot action")
	}
	if a.CacheTimeSeconds < 0 || a.CacheTimeSeconds > 24*60*60 {
		return invalid("action.cache_time_seconds", "is outside the bounded range")
	}
	switch a.Kind {
	case ActionProfileUpdateName:
		if a.Profile == nil {
			return invalid("action.profile", "is required")
		}
		if a.ResponseChannel == ResponseChannelControlBot {
			return invalid("action.response_channel", "profile updates require the user session")
		}
		if a.IdempotencyKey == "" {
			return invalid("action.idempotency_key", "is required for profile updates")
		}
		if err := a.Profile.Validate(); err != nil {
			return err
		}
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
	case ActionBotAnswerInline:
		if a.InlineQueryID == "" || a.CallbackID != "" {
			return invalid("action", "bot inline answer requires inline_query_id only")
		}
	case ActionBotAnswerCallback:
		if a.CallbackID == "" || a.InlineQueryID != "" {
			return invalid("action", "bot callback answer requires callback_id only")
		}
	case ActionBotSend:
		if a.ChatID == 0 {
			return invalid("action.chat_id", "is required")
		}
	case ActionBotEdit:
		if a.InlineMessageID == "" && (a.ChatID == 0 || a.MessageID == 0) {
			return invalid("action", "bot edit requires inline_message_id or chat_id and message_id")
		}
	case ActionBotDelete:
		if a.InlineMessageID != "" {
			return invalid("action.inline_message_id", "cannot delete an inline message")
		}
		if a.ChatID == 0 || a.MessageID == 0 {
			return invalid("action", "bot delete requires chat_id and message_id")
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
		if strings.HasPrefix(a.ClientOperation.Name, "storage.") {
			if a.StorageMutation == nil {
				return invalid("action.storage_mutation", "is required for storage client actions")
			}
			if err := a.StorageMutation.Validate(); err != nil {
				return err
			}
		}
	default:
		return invalid("action.kind", "unknown kind %q", a.Kind)
	}
	if a.Kind != ActionClient && a.ClientOperation != nil {
		return invalid("action.client_operation", "is only valid for kind %q", ActionClient)
	}
	if a.StorageMutation != nil && a.Kind != ActionClient {
		return invalid("action.storage_mutation", "is only valid for client actions")
	}
	if a.Profile != nil && a.Kind != ActionProfileUpdateName {
		return invalid("action.profile", "is only valid for profile_update_name")
	}
	if a.Media != nil {
		if a.Kind != ActionReply && a.Kind != ActionSend && a.Kind != ActionEdit && a.Kind != ActionBotSend && a.Kind != ActionBotEdit {
			return invalid("action.media", "is only valid for reply, send, or edit actions")
		}
		if err := a.Media.Validate(); err != nil {
			return err
		}
	}
	if len(a.Buttons) > MaxCollectionItems {
		return invalid("action.buttons", "collection exceeds limit %d", MaxCollectionItems)
	}
	if len(a.InlineResults) > MaxInlineResults {
		return invalid("action.inline_results", "collection exceeds limit %d", MaxInlineResults)
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

func validateCallback(callback Callback) error {
	if err := validString("callback.id", callback.ID, true); err != nil {
		return err
	}
	if len(callback.ID) > MaxQueryIDBytes {
		return invalid("callback.id", "exceeds %d bytes", MaxQueryIDBytes)
	}
	if callback.SenderID == 0 {
		return invalid("callback.sender_id", "must be non-zero")
	}
	if err := validString("callback.data", callback.Data, false); err != nil {
		return err
	}
	if len(callback.Data) > MaxCallbackDataBytes {
		return invalid("callback.data", "exceeds %d bytes", MaxCallbackDataBytes)
	}
	return nil
}

func validateInlineQuery(query InlineQuery) error {
	if err := validString("inline_query.id", query.ID, true); err != nil {
		return err
	}
	if len(query.ID) > MaxQueryIDBytes {
		return invalid("inline_query.id", "exceeds %d bytes", MaxQueryIDBytes)
	}
	if query.SenderID == 0 {
		return invalid("inline_query.sender_id", "must be non-zero")
	}
	if err := validString("inline_query.query", query.Query, false); err != nil {
		return err
	}
	return validString("inline_query.offset", query.Offset, false)
}

type inlineBudget struct {
	rows    int
	buttons int
}

func validateInlineButtons(field string, rows [][]InlineButton, budget *inlineBudget) error {
	if len(rows) > MaxInlineRows || len(rows) > MaxCollectionItems-budget.rows {
		return invalid(field, "rows exceed limit %d", MaxInlineRows)
	}
	budget.rows += len(rows)
	for rowIndex, row := range rows {
		if len(row) == 0 {
			return invalid(fmt.Sprintf("%s[%d]", field, rowIndex), "must not be empty")
		}
		if len(row) > MaxButtonsPerRow || len(row) > MaxCollectionItems-budget.buttons {
			return invalid(field, "buttons per row exceed limit %d", MaxButtonsPerRow)
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
			if len(button.CallbackData) > MaxCallbackDataBytes {
				return invalid(prefix+".callback_data", "exceeds %d bytes", MaxCallbackDataBytes)
			}
			if len(button.Text) > MaxButtonTextBytes {
				return invalid(prefix+".text", "exceeds %d bytes", MaxButtonTextBytes)
			}
			if len(button.URL) > MaxButtonURLBytes {
				return invalid(prefix+".url", "exceeds %d bytes", MaxButtonURLBytes)
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
