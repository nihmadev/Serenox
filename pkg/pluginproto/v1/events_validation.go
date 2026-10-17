package v1

import "fmt"

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
		if err := validateCallback(*u.Callback); err != nil {
			return err
		}
	case UpdateInlineQuery:
		if u.InlineQuery == nil {
			return invalid("update.inline_query", "is required")
		}
		if err := validateInlineQuery(*u.InlineQuery); err != nil {
			return err
		}
	case UpdateControlMessage, UpdateControlCallback, UpdateControlInlineQuery:
		if u.Control == nil {
			return invalid("update.control", "is required for %q", u.Kind)
		}
		if err := validateControlEvent(*u.Control, u.Kind); err != nil {
			return err
		}
	default:
		return invalid("update.kind", "unknown kind %q", u.Kind)
	}
	if u.Control != nil && u.Kind != UpdateControlMessage && u.Kind != UpdateControlCallback && u.Kind != UpdateControlInlineQuery {
		return invalid("update.control", "is only valid for a control-bot update")
	}
	if u.Message != nil {
		if err := validateMessage(*u.Message, "message"); err != nil {
			return err
		}
	}
	if u.Callback != nil && u.Callback.Message != nil {
		if err := validateMessage(*u.Callback.Message, "callback.message"); err != nil {
			return err
		}
	}
	return nil
}

func validateControlEvent(event ControlEvent, kind UpdateKind) error {
	if event.Bot.ID == 0 || event.Actor.ID == 0 {
		return invalid("control", "bot and actor IDs must be non-zero")
	}
	for field, value := range map[string]string{
		"control.bot.username": event.Bot.Username, "control.actor.username": event.Actor.Username,
		"control.inline_message_id": event.InlineMessageID, "control.chat_instance": event.ChatInstance,
		"control.query_id": event.QueryID, "control.query": event.Query, "control.offset": event.Offset,
		"control.chat_type": event.ChatType, "control.callback_data": event.CallbackData,
		"control.command": event.Command, "control.command_arguments": event.CommandArguments,
	} {
		if err := validString(field, value, false); err != nil {
			return err
		}
	}
	if len(event.Query) > MaxControlQueryBytes || len(event.CommandArguments) > MaxControlQueryBytes {
		return invalid("control", "query and command arguments exceed %d bytes", MaxControlQueryBytes)
	}
	if len(event.CallbackData) > MaxCallbackDataBytes || len(event.QueryID) > MaxQueryIDBytes || len(event.InlineMessageID) > MaxQueryIDBytes || len(event.ChatInstance) > MaxQueryIDBytes {
		return invalid("control", "identifier exceeds its bounded limit")
	}
	switch kind {
	case UpdateControlMessage:
		if event.ChatID == 0 || event.MessageID == 0 || event.Command == "" {
			return invalid("control", "private command needs chat_id, message_id, and command")
		}
	case UpdateControlCallback:
		if event.QueryID == "" || event.CallbackData == "" {
			return invalid("control", "callback needs query_id and callback_data")
		}
		if event.MessageID == 0 && event.InlineMessageID == "" {
			return invalid("control", "callback needs message_id or inline_message_id")
		}
	case UpdateControlInlineQuery:
		if event.QueryID == "" {
			return invalid("control.query_id", "is required")
		}
	}
	return nil
}

func validateMessage(message Message, prefix string) error {
	if message.ID <= 0 || message.ChatID == 0 {
		return invalid(prefix, "id must be positive and chat_id must be non-zero")
	}
	if err := validString(prefix+".text", message.Text, false); err != nil {
		return err
	}
	if len(message.Entities) > MaxCollectionItems {
		return invalid(prefix+".entities", "has too many entries")
	}
	if len(message.Text) > MaxClientTextBytes || len(message.Entities) > MaxClientEntities {
		return invalid(prefix, "text or entity count exceeds the client bound")
	}
	for index, entity := range message.Entities {
		if entity.Offset < 0 || entity.Length <= 0 {
			return invalid(prefix+".entities", "entity %d has an invalid range", index)
		}
		if err := validIdentifier(prefix+".entities.type", entity.Type); err != nil {
			return err
		}
		if err := validString(prefix+".entities.url", entity.URL, false); err != nil {
			return err
		}
		if err := validString(prefix+".entities.language", entity.Language, false); err != nil {
			return err
		}
	}
	if message.Peer != nil {
		if err := message.Peer.Validate(); err != nil {
			return invalid(prefix+".peer", "%v", err)
		}
	}
	if message.Sender != nil {
		if err := message.Sender.Validate(); err != nil {
			return invalid(prefix+".sender", "%v", err)
		}
	}
	if message.ReplyTo != nil && (message.ReplyTo.PeerID == 0 || message.ReplyTo.MessageID <= 0) {
		return invalid(prefix+".reply_to", "has invalid reference")
	}
	if message.Forward != nil {
		if err := validString(prefix+".forward.post_author", message.Forward.PostAuthor, false); err != nil {
			return err
		}
		if message.Forward.FromPeer != nil {
			if err := message.Forward.FromPeer.Validate(); err != nil {
				return invalid(prefix+".forward.from_peer", "%v", err)
			}
		}
	}
	if message.Reactions != nil {
		if len(message.Reactions.Reactions) > MaxCollectionItems {
			return invalid(prefix+".reactions", "has too many entries")
		}
		for index, reaction := range message.Reactions.Reactions {
			if err := validString(fmt.Sprintf("%s.reactions[%d].key", prefix, index), reaction.Key, true); err != nil {
				return err
			}
			if reaction.Count < 0 {
				return invalid(prefix+".reactions", "count must not be negative")
			}
		}
	}
	if message.Poll != nil {
		if err := validString(prefix+".poll.question", message.Poll.Question, true); err != nil {
			return err
		}
		if err := validIdentifier(prefix+".poll.kind", message.Poll.Kind); err != nil {
			return err
		}
		if len(message.Poll.Options) > MaxCollectionItems {
			return invalid(prefix+".poll.options", "has too many entries")
		}
	}
	if message.Service != nil {
		if err := validIdentifier(prefix+".service.kind", message.Service.Kind); err != nil {
			return err
		}
		if err := validString(prefix+".service.text", message.Service.Text, false); err != nil {
			return err
		}
	}
	if message.Media == nil {
		return nil
	}
	if err := validString(prefix+".media.kind", message.Media.Kind, true); err != nil {
		return err
	}
	if message.Media.Size < 0 {
		return invalid(prefix+".media.size", "must not be negative")
	}
	for field, value := range map[string]string{
		prefix + ".media.file_id":   message.Media.FileID,
		prefix + ".media.handle":    message.Media.Handle,
		prefix + ".media.file_name": message.Media.FileName,
		prefix + ".media.mime_type": message.Media.MIMEType,
		prefix + ".media.checksum":  message.Media.Checksum,
	} {
		if err := validString(field, value, false); err != nil {
			return err
		}
	}
	if message.Media.Attributes != nil {
		attributes := message.Media.Attributes
		if attributes.Width < 0 || attributes.Height < 0 || attributes.DurationSeconds < 0 || attributes.Width > 100000 || attributes.Height > 100000 || attributes.DurationSeconds > 7*24*60*60 {
			return invalid(prefix+".media.attributes", "is outside its bound")
		}
	}
	return nil
}
