package telegram

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/gotd/td/telegram/message"
	"github.com/gotd/td/telegram/message/entity"
	"github.com/gotd/td/telegram/message/html"
	"github.com/gotd/td/telegram/message/styling"
	"github.com/gotd/td/tg"
	pluginv1 "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

var (
	ErrActionUnsupported        = errors.New("native Telegram action is not implemented")
	ErrUnsupportedActionOptions = errors.New("native Telegram action contains unsupported options")
)

type actionAPI interface {
	MessagesSendMessage(context.Context, *tg.MessagesSendMessageRequest) (tg.UpdatesClass, error)
	MessagesEditMessage(context.Context, *tg.MessagesEditMessageRequest) (tg.UpdatesClass, error)
	MessagesDeleteMessages(context.Context, *tg.MessagesDeleteMessagesRequest) (*tg.MessagesAffectedMessages, error)
	ChannelsDeleteMessages(context.Context, *tg.ChannelsDeleteMessagesRequest) (*tg.MessagesAffectedMessages, error)
}

func (t *LiveTransport) Apply(ctx context.Context, action pluginv1.Action) (pluginv1.ActionResult, error) {
	result, err := t.apply(ctx, action)
	if err != nil && result.ActionID == "" {
		result = actionErrorResult(action, err)
	}
	return result, err
}

func (t *LiveTransport) apply(ctx context.Context, action pluginv1.Action) (pluginv1.ActionResult, error) {
	if ctx == nil {
		return pluginv1.ActionResult{}, errors.New("nil Telegram action context")
	}
	if err := action.Validate(); err != nil {
		return pluginv1.ActionResult{}, fmt.Errorf("invalid Telegram action: %w", err)
	}
	t.mu.Lock()
	ready := t.running && t.identity.UserID != 0 && !t.closed && t.api != nil && t.peers != nil
	t.mu.Unlock()
	if !ready {
		return pluginv1.ActionResult{}, ErrNotAuthorized
	}
	if action.Kind == pluginv1.ActionInlineAnswer || action.Kind == pluginv1.ActionClient {
		return pluginv1.ActionResult{}, fmt.Errorf("%w: %s", ErrActionUnsupported, action.Kind)
	}
	if action.CallbackID != "" || action.InlineQueryID != "" || action.ClientOperation != nil || len(action.InlineResults) != 0 {
		return pluginv1.ActionResult{}, fmt.Errorf("%w: inline answers and client operations are not available", ErrUnsupportedActionOptions)
	}
	if action.Media != nil {
		if len(action.Buttons) != 0 || action.Kind == pluginv1.ActionEdit {
			return pluginv1.ActionResult{}, fmt.Errorf("%w: media actions do not support edit or buttons", ErrUnsupportedActionOptions)
		}
		t.mu.Lock()
		downloader := t.mediaDownloader
		t.mu.Unlock()
		if downloader == nil {
			return pluginv1.ActionResult{}, fmt.Errorf("%w: media attachment adapter is not configured for this Telegram session", ErrActionUnsupported)
		}
		peer, resolveErr := t.peers.resolve(action.ChatID)
		if resolveErr != nil {
			return pluginv1.ActionResult{}, resolveErr
		}
		temporaryDir, err := os.MkdirTemp("", "serenox-telegram-media-")
		if err != nil {
			return pluginv1.ActionResult{}, err
		}
		path := filepath.Join(temporaryDir, safeMediaFilename(action.Media))
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			_ = os.Remove(temporaryDir)
			return pluginv1.ActionResult{}, err
		}
		defer file.Close()
		defer func() {
			_ = os.Remove(path)
			_ = os.Remove(temporaryDir)
		}()
		if err := downloader.DownloadAny(ctx, action.Media.Handle, file); err != nil {
			return pluginv1.ActionResult{}, fmt.Errorf("materialize media attachment: %w", err)
		}
		if err := file.Sync(); err != nil {
			return pluginv1.ActionResult{}, fmt.Errorf("sync media attachment: %w", err)
		}
		if err := file.Close(); err != nil {
			return pluginv1.ActionResult{}, fmt.Errorf("close media attachment: %w", err)
		}
		builder := message.NewSender(t.client.API()).To(peer).CloneBuilder()
		replyTo := action.ReplyToMessageID
		if replyTo == 0 && action.Kind == pluginv1.ActionReply {
			replyTo = action.MessageID
		}
		if replyTo != 0 {
			messageID, idErr := telegramMessageID(replyTo)
			if idErr != nil {
				return pluginv1.ActionResult{}, idErr
			}
			builder = builder.Reply(messageID)
		}
		upload := builder.Upload(message.FromPath(path))
		caption := styling.Plain(action.Media.Caption)
		var updates tg.UpdatesClass
		if action.Media.Kind == "photo" {
			updates, err = upload.Photo(ctx, caption)
		} else if action.Media.Kind == "video" {
			updates, err = upload.Video(ctx, caption)
		} else {
			input, inputErr := upload.AsInputFile(ctx)
			if inputErr != nil {
				err = inputErr
			} else {
				document := message.UploadedDocument(input, caption).
					Filename(safeMediaFilename(action.Media)).
					ForceFile(true)
				if mimeType := strings.TrimSpace(action.Media.MIMEType); mimeType != "" {
					document.MIME(mimeType)
				}
				updates, err = builder.Media(ctx, document)
			}
		}
		if err != nil {
			return pluginv1.ActionResult{}, fmt.Errorf("send Telegram media: %w", err)
		}
		return actionSuccessResult(action, messageResultFromUpdates(updates, action, peer, action.Media.Caption)), nil
	}
	markup, markupErr := inlineReplyMarkup(action.Buttons)
	if markupErr != nil {
		return pluginv1.ActionResult{}, markupErr
	}

	var err error
	result := actionSuccessResult(action, nil)
	switch action.Kind {
	case pluginv1.ActionSend, pluginv1.ActionReply, pluginv1.ActionEdit:
		peer, resolveErr := t.peers.resolve(action.ChatID)
		if resolveErr != nil {
			return pluginv1.ActionResult{}, resolveErr
		}
		text, entities, parseErr := parseActionText(action.Text, action.ParseMode, t.resolveInputUser)
		if parseErr != nil {
			return pluginv1.ActionResult{}, parseErr
		}
		switch action.Kind {
		case pluginv1.ActionSend:
			result, err = t.send(ctx, action, peer, text, entities, 0, markup)
		case pluginv1.ActionReply:
			replyTo := action.ReplyToMessageID
			if replyTo == 0 {
				replyTo = action.MessageID
			}
			if replyTo == 0 {
				return pluginv1.ActionResult{}, errors.New("reply action requires reply_to_message_id or message_id")
			}
			result, err = t.send(ctx, action, peer, text, entities, replyTo, markup)
		case pluginv1.ActionEdit:
			messageID, idErr := telegramMessageID(action.MessageID)
			if idErr != nil {
				return pluginv1.ActionResult{}, idErr
			}
			updates, editErr := t.api.MessagesEditMessage(ctx, &tg.MessagesEditMessageRequest{
				Peer: peer, ID: messageID, Message: text, Entities: entities, ReplyMarkup: markup,
			})
			err = editErr
			if err == nil {
				result = actionSuccessResult(action, messageResultFromUpdates(updates, action, peer, text))
			}
		}
	case pluginv1.ActionDelete:
		peer, resolveErr := t.peers.resolve(action.ChatID)
		if resolveErr != nil {
			return pluginv1.ActionResult{}, resolveErr
		}
		err = t.delete(ctx, peer, action.MessageID)
		if err == nil {
			result = actionSuccessResult(action, nil)
			result.MessageID = action.MessageID
			result.Peer = peerResult(peer, t.identity.UserID)
		}
	default:
		return pluginv1.ActionResult{}, fmt.Errorf("%w: %s", ErrActionUnsupported, action.Kind)
	}
	if err != nil {
		return pluginv1.ActionResult{}, fmt.Errorf("apply Telegram %s action: %w", action.Kind, err)
	}
	return result, nil
}

func mediaTempSuffix(media *pluginv1.MediaAttachment) string {
	if media == nil {
		return ".bin"
	}
	if extension := strings.ToLower(filepath.Ext(media.FileName)); extension != "" && len(extension) <= 10 {
		return extension
	}
	switch {
	case media.Kind == "photo":
		return ".jpg"
	case strings.EqualFold(media.MIMEType, "image/png"):
		return ".png"
	case strings.EqualFold(media.MIMEType, "image/jpeg"):
		return ".jpg"
	case strings.EqualFold(media.MIMEType, "image/gif"):
		return ".gif"
	default:
		return ".bin"
	}
}

func safeMediaFilename(media *pluginv1.MediaAttachment) string {
	name := "converted" + mediaTempSuffix(media)
	if media != nil {
		candidate := strings.TrimSpace(strings.ReplaceAll(media.FileName, "\\", "/"))
		candidate = filepath.Base(candidate)
		if candidate != "" && candidate != "." && candidate != ".." {
			name = candidate
			if filepath.Ext(name) == "" {
				name += mediaTempSuffix(media)
			}
		}
	}
	return name
}

func parseActionText(text, parseMode string, resolver func(int64) (tg.InputUserClass, error)) (string, []tg.MessageEntityClass, error) {
	switch strings.ToLower(strings.TrimSpace(parseMode)) {
	case "", "plain", "none":
		return text, nil, nil
	case "html":
		builder := entity.Builder{}
		if err := styling.Perform(&builder, html.String(resolver, text)); err != nil {
			return "", nil, fmt.Errorf("parse Telegram HTML: %w", err)
		}
		parsed, entities := builder.Complete()
		if len(entities) > pluginv1.MaxCollectionItems {
			return "", nil, fmt.Errorf("Telegram HTML produces more than %d entities", pluginv1.MaxCollectionItems)
		}
		return parsed, entities, nil
	default:
		return "", nil, fmt.Errorf("%w: parse_mode %q; supported values are plain and HTML", ErrUnsupportedActionOptions, parseMode)
	}
}

func (t *LiveTransport) resolveInputUser(userID int64) (tg.InputUserClass, error) {
	peer, err := t.peers.resolve(userID)
	if err != nil {
		return nil, err
	}
	switch value := peer.(type) {
	case *tg.InputPeerSelf:
		return &tg.InputUserSelf{}, nil
	case *tg.InputPeerUser:
		return &tg.InputUser{UserID: value.UserID, AccessHash: value.AccessHash}, nil
	default:
		return nil, fmt.Errorf("%w: user_id=%d", ErrPeerNotResolved, userID)
	}
}

func (t *LiveTransport) send(ctx context.Context, action pluginv1.Action, peer tg.InputPeerClass, text string, entities []tg.MessageEntityClass, replyTo int64, markup tg.ReplyMarkupClass) (pluginv1.ActionResult, error) {
	randomID, err := randomMessageID()
	if err != nil {
		return pluginv1.ActionResult{}, err
	}
	request := &tg.MessagesSendMessageRequest{Peer: peer, Message: text, Entities: entities, RandomID: randomID, ReplyMarkup: markup}
	if replyTo != 0 {
		messageID, idErr := telegramMessageID(replyTo)
		if idErr != nil {
			return pluginv1.ActionResult{}, idErr
		}
		request.ReplyTo = &tg.InputReplyToMessage{ReplyToMsgID: messageID}
	}
	result, err := t.api.MessagesSendMessage(ctx, request)
	if err != nil {
		return pluginv1.ActionResult{}, err
	}
	// Telegram commonly answers a user-session send with
	// updateShortSentMessage. gotd intentionally converts that response into a
	// message without peer or text because the TL response omits both fields;
	// the normal dispatcher therefore cannot produce an application DTO from
	// it. Reconstruct the transport-neutral update from the action we just sent
	// and the authoritative server-assigned ID. This is not a retry: it only
	// bridges the already accepted response into the bounded ingress queue.
	if short, ok := result.(*tg.UpdateShortSentMessage); ok && short != nil {
		message, convertErr := sentMessageResult(peer, t.identity.UserID, text, entities, replyTo, short)
		if convertErr != nil {
			return pluginv1.ActionResult{}, convertErr
		}
		if enqueueErr := t.enqueueSentMessage(ctx, peer, text, entities, replyTo, short); enqueueErr != nil {
			return pluginv1.ActionResult{}, enqueueErr
		}
		return actionSuccessResult(action, message), nil
	}
	return actionSuccessResult(action, messageResultFromUpdates(result, action, peer, text)), nil
}

func actionSuccessResult(action pluginv1.Action, message *pluginv1.Message) pluginv1.ActionResult {
	result := pluginv1.ActionResult{ActionID: action.ID, Operation: action.OperationName(), Status: pluginv1.ActionResultSuccess, Success: true, Message: message}
	if message != nil {
		result.MessageID, result.DateUnixMS = message.ID, message.DateUnixMS
		result.MediaHandle = func() string {
			if message.Media != nil {
				return message.Media.Handle
			}
			return ""
		}()
	}
	return result
}

func peerResult(peer tg.InputPeerClass, selfID int64) *pluginv1.Peer {
	id, kind, ok := inputPeerID(peer, selfID)
	if !ok {
		return nil
	}
	return &pluginv1.Peer{ID: id, Kind: kind}
}

func inputPeerID(peer tg.InputPeerClass, selfID int64) (int64, string, bool) {
	switch value := peer.(type) {
	case *tg.InputPeerSelf:
		return selfID, "private", selfID != 0
	case *tg.InputPeerUser:
		return value.UserID, "private", value.UserID != 0
	case *tg.InputPeerChat:
		return -value.ChatID, "group", value.ChatID != 0
	case *tg.InputPeerChannel:
		return channelChatID(value.ChannelID), "channel", value.ChannelID != 0
	default:
		return 0, "", false
	}
}

func messageResultFromUpdates(updates tg.UpdatesClass, action pluginv1.Action, peer tg.InputPeerClass, text string) *pluginv1.Message {
	if short, ok := updates.(*tg.UpdateShortSentMessage); ok && short != nil {
		message, _ := sentMessageResult(peer, 0, text, short.Entities, action.ReplyToMessageID, short)
		return message
	}
	return nil
}

func sentMessageResult(peer tg.InputPeerClass, selfID int64, text string, entities []tg.MessageEntityClass, replyTo int64, sent *tg.UpdateShortSentMessage) (*pluginv1.Message, error) {
	if sent == nil || sent.ID <= 0 {
		return nil, errors.New("Telegram response did not contain a message ID")
	}
	chatID, chatKind, ok := inputPeerID(peer, selfID)
	if !ok {
		return nil, errors.New("Telegram response peer cannot be converted")
	}
	result := &pluginv1.Message{ID: int64(sent.ID), ChatID: chatID, DateUnixMS: int64(sent.Date) * 1000, Text: text, Outgoing: sent.Out, ChatKind: chatKind}
	if replyTo != 0 {
		result.ReplyToMessageID = replyTo
	}
	converted, err := convertEntities(sent.Entities)
	if err != nil {
		return nil, err
	}
	if len(converted) == 0 {
		converted, err = convertEntities(entities)
	}
	if err != nil {
		return nil, err
	}
	result.Entities = converted
	result.Media = convertMedia(sent.Media, fmt.Sprintf("tg_%d_%d", chatID, sent.ID))
	return result, nil
}

func (t *LiveTransport) enqueueSentMessage(ctx context.Context, peer tg.InputPeerClass, text string, entities []tg.MessageEntityClass, replyTo int64, sent *tg.UpdateShortSentMessage) error {
	if sent == nil || sent.ID <= 0 {
		return errors.New("Telegram send response did not contain a message ID")
	}
	identity, _ := t.Identity()
	peerID, ok := inputPeerAsPeer(peer, identity.UserID)
	if !ok {
		return errors.New("Telegram send response peer cannot be converted")
	}
	message := &tg.Message{ID: sent.ID, PeerID: peerID, Date: sent.Date, Message: text, Out: sent.Out, Entities: sent.Entities}
	if message.Entities == nil {
		message.Entities = entities
	}
	if sent.Media != nil {
		message.Media = sent.Media
	}
	if replyTo != 0 {
		message.SetReplyTo(&tg.MessageReplyHeader{ReplyToMsgID: int(replyTo)})
	}
	update, found, err := convertMessage(pluginv1.UpdateMessage, message, tg.Entities{
		Users: make(map[int64]*tg.User), Chats: make(map[int64]*tg.Chat), Channels: make(map[int64]*tg.Channel),
	}, identity.UserID)
	if err != nil {
		return err
	}
	if !found {
		return errors.New("Telegram send response could not be converted")
	}
	return t.enqueue(ctx, update)
}

func inputPeerAsPeer(peer tg.InputPeerClass, selfID int64) (tg.PeerClass, bool) {
	switch value := peer.(type) {
	case *tg.InputPeerSelf:
		return &tg.PeerUser{UserID: selfID}, selfID != 0
	case *tg.InputPeerUser:
		return &tg.PeerUser{UserID: value.UserID}, value.UserID != 0
	case *tg.InputPeerChat:
		return &tg.PeerChat{ChatID: value.ChatID}, value.ChatID != 0
	case *tg.InputPeerChannel:
		return &tg.PeerChannel{ChannelID: value.ChannelID}, value.ChannelID != 0
	default:
		return nil, false
	}
}

func inlineReplyMarkup(rows [][]pluginv1.InlineButton) (tg.ReplyMarkupClass, error) {
	if len(rows) == 0 {
		return nil, nil
	}
	markup := &tg.ReplyInlineMarkup{Rows: make([]tg.KeyboardButtonRow, len(rows))}
	for rowIndex, row := range rows {
		markup.Rows[rowIndex].Buttons = make([]tg.KeyboardButtonClass, len(row))
		for buttonIndex, button := range row {
			var value tg.KeyboardButtonClass
			switch {
			case button.CallbackData != "":
				value = &tg.KeyboardButtonCallback{Text: button.Text, Data: []byte(button.CallbackData)}
			case button.URL != "":
				value = &tg.KeyboardButtonURL{Text: button.Text, URL: button.URL}
			case button.SwitchQuery != "":
				value = &tg.KeyboardButtonSwitchInline{Text: button.Text, Query: button.SwitchQuery}
			default:
				return nil, fmt.Errorf("%w: button %q has no supported target", ErrUnsupportedActionOptions, button.Text)
			}
			markup.Rows[rowIndex].Buttons[buttonIndex] = value
		}
	}
	return markup, nil
}

func (t *LiveTransport) delete(ctx context.Context, peer tg.InputPeerClass, rawMessageID int64) error {
	messageID, err := telegramMessageID(rawMessageID)
	if err != nil {
		return err
	}
	if channel, ok := peer.(*tg.InputPeerChannel); ok {
		_, err = t.api.ChannelsDeleteMessages(ctx, &tg.ChannelsDeleteMessagesRequest{
			Channel: &tg.InputChannel{ChannelID: channel.ChannelID, AccessHash: channel.AccessHash},
			ID:      []int{messageID},
		})
		return err
	}
	_, err = t.api.MessagesDeleteMessages(ctx, &tg.MessagesDeleteMessagesRequest{Revoke: true, ID: []int{messageID}})
	return err
}

func telegramMessageID(value int64) (int, error) {
	if value <= 0 || value > math.MaxInt32 {
		return 0, fmt.Errorf("Telegram message ID %d is outside the protocol range", value)
	}
	return int(value), nil
}

func randomMessageID() (int64, error) {
	var payload [8]byte
	if _, err := cryptorand.Read(payload[:]); err != nil {
		return 0, fmt.Errorf("generate Telegram random ID: %w", err)
	}
	value := int64(binary.LittleEndian.Uint64(payload[:]))
	if value == 0 {
		value = 1
	}
	return value, nil
}
