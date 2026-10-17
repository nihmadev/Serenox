package plugin

import (
	"context"
	"fmt"

	protocol "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

// ActionClient is the typed, bounded reverse-RPC action surface. It exposes
// only protocol DTOs and never a Telegram library object.
type ActionClient struct{ rpc *serverRPC }

func (c *ActionClient) Apply(ctx context.Context, action protocol.Action) (protocol.ActionResult, error) {
	if c == nil || c.rpc == nil {
		return protocol.ActionResult{}, fmt.Errorf("action client is unavailable")
	}
	if err := action.Validate(); err != nil {
		return protocol.ActionResult{}, err
	}
	response, err := c.Batch(ctx, protocol.ActionBatchRequest{Actions: []protocol.Action{action}})
	if err != nil {
		return protocol.ActionResult{}, err
	}
	if len(response.Results) != 1 {
		return protocol.ActionResult{}, fmt.Errorf("host returned an invalid action result count")
	}
	return response.Results[0], nil
}

func (c *ActionClient) Batch(ctx context.Context, request protocol.ActionBatchRequest) (protocol.ActionBatchResponse, error) {
	if c == nil || c.rpc == nil {
		return protocol.ActionBatchResponse{}, fmt.Errorf("action client is unavailable")
	}
	if err := request.Validate(); err != nil {
		return protocol.ActionBatchResponse{}, err
	}
	var response protocol.ActionBatchResponse
	if err := c.rpc.Call(ctx, protocol.MethodHostActions, request, &response); err != nil {
		return protocol.ActionBatchResponse{}, err
	}
	if err := response.Validate(); err != nil {
		return protocol.ActionBatchResponse{}, err
	}
	return response, nil
}

func (c *ActionClient) Send(ctx context.Context, id string, chatID int64, text string) (protocol.ActionResult, error) {
	return c.Apply(ctx, protocol.Action{ID: id, Kind: protocol.ActionSend, ChatID: chatID, Text: text})
}

func (c *ActionClient) Reply(ctx context.Context, id string, chatID, messageID int64, text string) (protocol.ActionResult, error) {
	return c.Apply(ctx, protocol.Action{ID: id, Kind: protocol.ActionReply, ChatID: chatID, MessageID: messageID, Text: text})
}

func (c *ActionClient) Edit(ctx context.Context, id string, chatID, messageID int64, text string) (protocol.ActionResult, error) {
	return c.Apply(ctx, protocol.Action{ID: id, Kind: protocol.ActionEdit, ChatID: chatID, MessageID: messageID, Text: text})
}

func (c *ActionClient) Delete(ctx context.Context, id string, chatID, messageID int64) (protocol.ActionResult, error) {
	return c.Apply(ctx, protocol.Action{ID: id, Kind: protocol.ActionDelete, ChatID: chatID, MessageID: messageID})
}

func (c *ActionClient) AttachMedia(ctx context.Context, id string, action protocol.Action) (protocol.ActionResult, error) {
	if action.Media == nil {
		return protocol.ActionResult{}, fmt.Errorf("media attachment is required")
	}
	action.ID = id
	if action.Kind == "" {
		action.Kind = protocol.ActionSend
	}
	return c.Apply(ctx, action)
}

func (c *ActionClient) ProfileAcquire(ctx context.Context, id, idempotencyKey string) (protocol.ActionResult, error) {
	return c.profile(ctx, id, idempotencyKey, protocol.ProfileNameAction{Operation: protocol.ProfileAcquire})
}

func (c *ActionClient) ProfileUpdate(ctx context.Context, id, idempotencyKey, firstName, lastName string, generation uint64, leaseToken string) (protocol.ActionResult, error) {
	return c.profile(ctx, id, idempotencyKey, protocol.ProfileNameAction{Operation: protocol.ProfileUpdate, FirstName: firstName, LastName: lastName, Generation: generation, LeaseToken: leaseToken})
}

func (c *ActionClient) ProfileRestore(ctx context.Context, id, idempotencyKey string, generation uint64, leaseToken string) (protocol.ActionResult, error) {
	return c.profile(ctx, id, idempotencyKey, protocol.ProfileNameAction{Operation: protocol.ProfileRestore, Generation: generation, LeaseToken: leaseToken})
}

func (c *ActionClient) ProfileRelease(ctx context.Context, id, idempotencyKey string, generation uint64, leaseToken string) (protocol.ActionResult, error) {
	return c.profile(ctx, id, idempotencyKey, protocol.ProfileNameAction{Operation: protocol.ProfileRelease, Generation: generation, LeaseToken: leaseToken})
}

func (c *ActionClient) profile(ctx context.Context, id, idempotencyKey string, profile protocol.ProfileNameAction) (protocol.ActionResult, error) {
	return c.Apply(ctx, protocol.Action{ID: id, Kind: protocol.ActionProfileUpdateName, IdempotencyKey: idempotencyKey, Profile: &profile})
}
