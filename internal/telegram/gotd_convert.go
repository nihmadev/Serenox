package telegram

import (
	"errors"
	"fmt"

	"github.com/gotd/td/tg"
	pluginv1 "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

func convertMessage(kind pluginv1.UpdateKind, messageClass tg.MessageClass, entities tg.Entities, selfID int64) (pluginv1.Update, bool, error) {
	message, ok := messageClass.(*tg.Message)
	if !ok || message == nil || message.ID == 0 || message.PeerID == nil {
		return pluginv1.Update{}, false, nil
	}
	chatID, chatKind, ok := peerChat(message.PeerID, entities)
	if !ok {
		return pluginv1.Update{}, false, nil
	}
	senderID := int64(0)
	if sender, present := message.GetFromID(); present {
		senderID, _, _ = peerChat(sender, entities)
	} else if message.Out {
		senderID = selfID
	} else if user, isUser := message.PeerID.(*tg.PeerUser); isUser {
		senderID = user.UserID
	}
	replyTo := int64(0)
	if headerClass, present := message.GetReplyTo(); present {
		if header, isMessage := headerClass.(*tg.MessageReplyHeader); isMessage {
			if id, set := header.GetReplyToMsgID(); set {
				replyTo = int64(id)
			}
		}
	}
	editDate, _ := message.GetEditDate()
	_, viaBot := message.GetViaBotID()
	_, forwarded := message.GetFwdFrom()
	convertedEntities, err := convertEntities(message.Entities)
	if err != nil {
		return pluginv1.Update{}, false, err
	}
	occurred := message.Date
	if kind == pluginv1.UpdateEditedMessage && editDate > 0 {
		occurred = editDate
	}
	converted := &pluginv1.Message{
		ID: int64(message.ID), ChatID: chatID, SenderID: senderID,
		DateUnixMS: int64(message.Date) * 1000, EditDateUnixMS: int64(editDate) * 1000,
		Text: message.Message, Outgoing: message.Out, Mentioned: message.Mentioned,
		ChatKind: chatKind, ViaBot: viaBot, Forwarded: forwarded,
		ReplyToMessageID: replyTo, Entities: convertedEntities,
	}
	converted.Media = convertMedia(message.Media, fmt.Sprintf("tg_%d_%d", chatID, message.ID))
	if groupedID, present := message.GetGroupedID(); present {
		converted.GroupedID = groupedID
	}
	if forwardedHeader, present := message.GetFwdFrom(); present {
		forward := &pluginv1.ForwardReference{DateUnixMS: int64(forwardedHeader.Date) * 1000, PostAuthor: forwardedHeader.PostAuthor}
		if fromID, ok := forwardedHeader.GetFromID(); ok {
			if id, kind, found := peerChat(fromID, entities); found {
				forward.FromPeer = peerDTO(id, kind)
			}
		}
		converted.Forward = forward
	}
	if reactions, present := message.GetReactions(); present {
		converted.Reactions = convertReactionSummary(reactions)
	}
	update := pluginv1.Update{
		ID:   fmt.Sprintf("telegram:%s:%d:%d:%d", kind, chatID, message.ID, occurred),
		Kind: kind, OccurredUnixMS: int64(occurred) * 1000, Message: converted,
	}
	return update, true, nil
}

func convertReactionSummary(value tg.MessageReactions) *pluginv1.ReactionSummary {
	result := &pluginv1.ReactionSummary{CanSee: value.CanSeeList, Reactions: make([]pluginv1.Reaction, 0, len(value.Results))}
	for _, item := range value.Results {
		key := "reaction"
		switch reaction := item.Reaction.(type) {
		case *tg.ReactionEmoji:
			key = reaction.Emoticon
		case *tg.ReactionCustomEmoji:
			key = fmt.Sprintf("custom:%d", reaction.DocumentID)
		}
		result.Reactions = append(result.Reactions, pluginv1.Reaction{Key: key, Count: item.Count, Chosen: item.ChosenOrder > 0})
		result.Count += item.Count
	}
	return result
}

func convertEntities(source []tg.MessageEntityClass) ([]pluginv1.Entity, error) {
	if len(source) == 0 {
		return nil, nil
	}
	if len(source) > pluginv1.MaxCollectionItems {
		return nil, fmt.Errorf("Telegram message contains more than %d entities", pluginv1.MaxCollectionItems)
	}
	result := make([]pluginv1.Entity, 0, len(source))
	for _, value := range source {
		if value == nil || value.GetOffset() < 0 || value.GetLength() <= 0 {
			return nil, errors.New("Telegram message contains an invalid entity range")
		}
		entity := pluginv1.Entity{Type: entityType(value), Offset: value.GetOffset(), Length: value.GetLength()}
		switch typed := value.(type) {
		case *tg.MessageEntityPre:
			entity.Language = typed.Language
		case *tg.MessageEntityTextURL:
			entity.URL = typed.URL
		case *tg.MessageEntityMentionName:
			entity.UserID = typed.UserID
		}
		result = append(result, entity)
	}
	return result, nil
}

func entityType(value tg.MessageEntityClass) string {
	switch value.(type) {
	case *tg.MessageEntityMention:
		return "mention"
	case *tg.MessageEntityHashtag:
		return "hashtag"
	case *tg.MessageEntityBotCommand:
		return "bot_command"
	case *tg.MessageEntityURL:
		return "url"
	case *tg.MessageEntityEmail:
		return "email"
	case *tg.MessageEntityBold:
		return "bold"
	case *tg.MessageEntityItalic:
		return "italic"
	case *tg.MessageEntityCode:
		return "code"
	case *tg.MessageEntityPre:
		return "pre"
	case *tg.MessageEntityTextURL:
		return "text_url"
	case *tg.MessageEntityMentionName:
		return "text_mention"
	case *tg.MessageEntityPhone:
		return "phone_number"
	case *tg.MessageEntityCashtag:
		return "cashtag"
	case *tg.MessageEntityUnderline:
		return "underline"
	case *tg.MessageEntityStrike:
		return "strikethrough"
	case *tg.MessageEntityBankCard:
		return "bank_card"
	case *tg.MessageEntitySpoiler:
		return "spoiler"
	case *tg.MessageEntityCustomEmoji:
		return "custom_emoji"
	case *tg.MessageEntityBlockquote:
		return "blockquote"
	case *tg.MessageEntityFormattedDate:
		return "formatted_date"
	default:
		return "unknown"
	}
}

func peerChat(peer tg.PeerClass, entities tg.Entities) (int64, string, bool) {
	switch value := peer.(type) {
	case *tg.PeerUser:
		return value.UserID, "private", value.UserID != 0
	case *tg.PeerChat:
		return -value.ChatID, "group", value.ChatID != 0
	case *tg.PeerChannel:
		kind := "channel"
		if channel := entities.Channels[value.ChannelID]; channel != nil && channel.Megagroup {
			kind = "supergroup"
		}
		return channelChatID(value.ChannelID), kind, value.ChannelID != 0
	default:
		return 0, "", false
	}
}

func convertMedia(media tg.MessageMediaClass, handle string) *pluginv1.Media {
	switch value := media.(type) {
	case nil, *tg.MessageMediaEmpty:
		return nil
	case *tg.MessageMediaPhoto:
		return &pluginv1.Media{Kind: "photo", Handle: handle, FileName: "photo.jpg", MIMEType: "image/jpeg"}
	case *tg.MessageMediaDocument:
		return convertDocumentMedia(value, handle)
	case *tg.MessageMediaDice:
		return &pluginv1.Media{Kind: "dice"}
	case *tg.MessageMediaGeo, *tg.MessageMediaGeoLive, *tg.MessageMediaVenue:
		return &pluginv1.Media{Kind: "location"}
	case *tg.MessageMediaContact:
		return &pluginv1.Media{Kind: "contact"}
	case *tg.MessageMediaPoll:
		return &pluginv1.Media{Kind: "poll"}
	default:
		return &pluginv1.Media{Kind: "other"}
	}
}

func convertDocumentMedia(media *tg.MessageMediaDocument, handles ...string) *pluginv1.Media {
	if media == nil {
		return nil
	}
	handle := ""
	if len(handles) != 0 {
		handle = handles[0]
	}
	document, ok := media.Document.(*tg.Document)
	base := pluginv1.Media{Kind: "document", Handle: handle}
	if ok && document != nil {
		base.Size = document.Size
		base.MIMEType = document.MimeType
		for _, attribute := range document.Attributes {
			if value, isFilename := attribute.(*tg.DocumentAttributeFilename); isFilename {
				base.FileName = value.FileName
				break
			}
		}
	}
	if media.Voice {
		base.Kind = "voice"
		return &base
	}
	if media.Video || media.Round {
		base.Kind = "video"
		return &base
	}
	if document == nil {
		return &base
	}
	for _, attribute := range document.Attributes {
		switch value := attribute.(type) {
		case *tg.DocumentAttributeSticker:
			base.Kind = "sticker"
			return &base
		case *tg.DocumentAttributeAudio:
			base.Attributes = &pluginv1.MediaAttributes{DurationSeconds: value.Duration}
			if value.Voice {
				base.Kind = "voice"
				return &base
			}
			base.Kind = "audio"
			return &base
		case *tg.DocumentAttributeVideo:
			base.Attributes = &pluginv1.MediaAttributes{Width: value.W, Height: value.H, DurationSeconds: int(value.Duration)}
			base.Kind = "video"
			return &base
		}
	}
	return &base
}
