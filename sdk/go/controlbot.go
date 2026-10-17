package plugin

import (
	"context"
	"errors"

	protocol "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

// ControlBot is the finite plugin-side helper for control UI actions. It has
// no Bot API client, HTTP capability, token, or socket; every request is sent
// through the authenticated host.actions RPC and checked by the core.
type ControlBot struct{ actions *ActionClient }

func NewControlBot(actions *ActionClient) (*ControlBot, error) {
	if actions == nil {
		return nil, errors.New("control bot actions are required")
	}
	return &ControlBot{actions: actions}, nil
}

func (b *ControlBot) AnswerInline(ctx context.Context, id, queryID string, results []protocol.InlineResult, personal bool, nextOffset string) (protocol.ActionResult, error) {
	return b.actions.Apply(ctx, protocol.Action{ID: id, Kind: protocol.ActionBotAnswerInline, ResponseChannel: protocol.ResponseChannelControlBot, InlineQueryID: queryID, InlineResults: results, IsPersonal: personal, NextOffset: nextOffset})
}

func (b *ControlBot) AnswerCallback(ctx context.Context, id, callbackID, text string, alert bool) (protocol.ActionResult, error) {
	return b.actions.Apply(ctx, protocol.Action{ID: id, Kind: protocol.ActionBotAnswerCallback, ResponseChannel: protocol.ResponseChannelControlBot, CallbackID: callbackID, CallbackText: text, ShowAlert: alert})
}

func (b *ControlBot) Send(ctx context.Context, id string, chatID int64, text string, buttons [][]protocol.InlineButton) (protocol.ActionResult, error) {
	return b.actions.Apply(ctx, protocol.Action{ID: id, Kind: protocol.ActionBotSend, ResponseChannel: protocol.ResponseChannelControlBot, ChatID: chatID, Text: text, Buttons: buttons})
}

func (b *ControlBot) Edit(ctx context.Context, id string, chatID, messageID int64, inlineMessageID, text string, buttons [][]protocol.InlineButton) (protocol.ActionResult, error) {
	return b.actions.Apply(ctx, protocol.Action{ID: id, Kind: protocol.ActionBotEdit, ResponseChannel: protocol.ResponseChannelControlBot, ChatID: chatID, MessageID: messageID, InlineMessageID: inlineMessageID, Text: text, Buttons: buttons})
}

func (b *ControlBot) Delete(ctx context.Context, id string, chatID, messageID int64) (protocol.ActionResult, error) {
	return b.actions.Apply(ctx, protocol.Action{ID: id, Kind: protocol.ActionBotDelete, ResponseChannel: protocol.ResponseChannelControlBot, ChatID: chatID, MessageID: messageID})
}
