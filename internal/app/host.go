package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/nihmadev/Serenox/internal/pluginpackage"
	"github.com/nihmadev/Serenox/internal/storage"
	"github.com/nihmadev/Serenox/internal/telegram"
	pluginv1 "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

type preSendContextKey struct{}

func (a *App) applyActions(ctx context.Context, actions []pluginv1.Action) error {
	_, err := a.applyActionsWithResults(ctx, "core", actions)
	return err
}

func (a *App) applyActionsWithResults(ctx context.Context, pluginID string, actions []pluginv1.Action) (pluginv1.ActionBatchResponse, error) {
	if pluginID == "" {
		pluginID = "core"
	}
	prepared := make([]pluginv1.Action, len(actions))
	copy(prepared, actions)
	if ctx.Value(preSendContextKey{}) == nil {
		for index := range prepared {
			if prepared[index].Kind != pluginv1.ActionSend {
				continue
			}
			transformed, transformErr := a.applyPreSendHooks(ctx, prepared[index])
			if transformErr != nil {
				return pluginv1.ActionBatchResponse{}, transformErr
			}
			prepared[index] = transformed
		}
	}
	started := time.Now()
	response, err := a.applyPreparedActions(ctx, pluginID, prepared)
	if err == nil {
		if routeErr := a.registerSuccessfulCallbacks(pluginID, prepared, response); routeErr != nil {
			err = routeErr
		}
	}
	_ = a.metrics.Add("serenox_actions_total", map[string]string{"outcome": batchOutcome(response, err)}, int64(len(actions)))
	_ = a.metrics.Add("serenox_action_latency_ms_total", nil, time.Since(started).Milliseconds())
	if response.Partial {
		_ = a.metrics.Add("serenox_action_partial_batches_total", nil, 1)
	}
	for _, result := range response.Results {
		_ = a.metrics.Add("serenox_action_results_total", map[string]string{"operation": result.Operation, "state": resultState(result)}, 1)
		if result.OutcomeUnknown {
			_ = a.metrics.Add("serenox_action_outcome_unknown_total", nil, 1)
		}
		if result.Retryable {
			_ = a.metrics.Add("serenox_action_retryable_total", nil, 1)
		}
	}
	return response, err
}

// applyPreparedActions keeps the Bot API response channel physically separate
// from the user-session transport. This is intentionally sequential: action
// order remains deterministic and an ambiguous Bot API mutation is returned as
// outcome_unknown rather than retried or misrouted through gotd.
func (a *App) applyPreparedActions(ctx context.Context, pluginID string, actions []pluginv1.Action) (pluginv1.ActionBatchResponse, error) {
	response := pluginv1.ActionBatchResponse{Results: make([]pluginv1.ActionResult, 0, len(actions))}
	for index, action := range actions {
		channel := action.ResponseChannel
		if channel == "" {
			channel = pluginv1.ResponseChannelUserSession
		}
		var result pluginv1.ActionResult
		var err error
		if action.Kind == pluginv1.ActionProfileUpdateName {
			result, err = a.applyProfileAction(ctx, pluginID, action)
		} else if channel == pluginv1.ResponseChannelControlBot {
			if a.controlBot == nil {
				return response, errors.New("control bot transport is not attached")
			}
			result, err = a.controlBot.Apply(ctx, action)
		} else {
			var one pluginv1.ActionBatchResponse
			one, err = telegram.ApplyBatchResults(ctx, a.transport, pluginv1.ActionBatchRequest{Actions: []pluginv1.Action{action}}, pluginID+"/account/"+fmt.Sprint(a.SelfID()), a.idempotency)
			if len(one.Results) != 0 {
				result = one.Results[0]
			}
		}
		response.Results = append(response.Results, result)
		if err != nil || result.Status == pluginv1.ActionResultFailure || result.Status == pluginv1.ActionResultOutcomeUnknown {
			response.Partial = index+1 < len(actions)
			return response, err
		}
	}
	return response, nil
}

func (a *App) applyPreSendHooks(ctx context.Context, action pluginv1.Action) (pluginv1.Action, error) {
	a.preSendMu.RLock()
	hooks := append([]preSendHook(nil), a.preSendHooks...)
	a.preSendMu.RUnlock()
	for _, hook := range hooks {
		request := pluginv1.DispatchRequest{PreSend: &pluginv1.PreSendRequest{
			Handler: hook.name,
			Action:  action,
		}}
		hookCtx := context.WithValue(ctx, preSendContextKey{}, true)
		response, err := hook.handler(hookCtx, request)
		if err != nil {
			return pluginv1.Action{}, fmt.Errorf("pre-send hook %s/%s: %w", hook.pluginID, hook.name, err)
		}
		if err := response.Validate(); err != nil {
			return pluginv1.Action{}, fmt.Errorf("pre-send hook %s/%s response: %w", hook.pluginID, hook.name, err)
		}
		if response.PreSend == nil {
			return pluginv1.Action{}, fmt.Errorf("pre-send hook %s/%s returned no action", hook.pluginID, hook.name)
		}
		transformed := response.PreSend.Action
		if transformed.ID != action.ID || transformed.Kind != pluginv1.ActionSend || transformed.ChatID != action.ChatID || transformed.ReplyToMessageID != action.ReplyToMessageID {
			return pluginv1.Action{}, fmt.Errorf("pre-send hook %s/%s changed immutable send fields", hook.pluginID, hook.name)
		}
		action = transformed
	}
	return action, nil
}

func (a *App) registerSuccessfulCallbacks(pluginID string, actions []pluginv1.Action, response pluginv1.ActionBatchResponse) error {
	if pluginID == "" {
		pluginID = "core"
	}
	success := make(map[string]struct{}, len(response.Results))
	for _, result := range response.Results {
		if result.Status == pluginv1.ActionResultSuccess || (result.Status == "" && result.Success) {
			success[result.ActionID] = struct{}{}
		}
	}
	registered := make([]string, 0)
	cleanup := func() {
		for _, data := range registered {
			a.callbacks.UnregisterDynamic(data)
		}
	}
	register := func(data string) error {
		if err := a.callbacks.RegisterDynamic(data, pluginID, "callback", time.Now().Add(10*time.Minute)); err != nil {
			cleanup()
			return err
		}
		registered = append(registered, data)
		return nil
	}
	for _, action := range actions {
		if _, ok := success[action.ID]; !ok {
			continue
		}
		for _, row := range action.Buttons {
			for _, button := range row {
				if button.CallbackData != "" {
					if err := register(button.CallbackData); err != nil {
						return fmt.Errorf("register callback route: %w", err)
					}
				}
			}
		}
		for _, inline := range action.InlineResults {
			for _, row := range inline.Buttons {
				for _, button := range row {
					if button.CallbackData != "" {
						if err := register(button.CallbackData); err != nil {
							return fmt.Errorf("register callback route: %w", err)
						}
					}
				}
			}
		}
	}
	return nil
}

func resultState(result pluginv1.ActionResult) string {
	if result.Status != "" {
		return string(result.Status)
	}
	if result.Success {
		return string(pluginv1.ActionResultSuccess)
	}
	return string(pluginv1.ActionResultFailure)
}

func batchOutcome(response pluginv1.ActionBatchResponse, err error) string {
	if err != nil {
		return "error"
	}
	if response.Partial {
		return "partial"
	}
	return "complete"
}

func (a *App) applyActionsForPlugin(ctx context.Context, pluginID string, actions []pluginv1.Action) error {
	_, err := a.applyActionsWithResults(ctx, pluginID, actions)
	return err
}

// ApplyActions applies a bounded action batch emitted by a supervised duplex
// plugin while its dispatch handler is still running.
func (a *App) ApplyActions(ctx context.Context, actions []pluginv1.Action) error {
	if ctx == nil {
		ctx = context.Background()
	}
	return a.applyActions(ctx, actions)
}

// ApplyActionsWithResults is the typed reverse-RPC action boundary. It keeps
// Telegram execution sequential and returns partial results without claiming
// atomicity.
func (a *App) ApplyActionsWithResults(ctx context.Context, pluginID string, actions []pluginv1.Action) (pluginv1.ActionBatchResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	return a.applyActionsWithResults(ctx, pluginID, actions)
}

// HandleHostStorage is the only storage bridge exposed to plugin processes.
// The plugin and account components are applied by the host, so a plugin
// cannot read another plugin's namespace by changing its request namespace.
func (a *App) HandleHostStorage(ctx context.Context, pluginID string, accountID int64, request pluginv1.StorageRequest) (pluginv1.StorageResult, error) {
	if a.store == nil {
		return pluginv1.StorageResult{}, errors.New("storage is unavailable")
	}
	if pluginID == "" {
		return pluginv1.StorageResult{}, fmt.Errorf("%w: missing plugin identity", pluginv1.ErrInvalidDTO)
	}
	namespace := func(value string) string { return fmt.Sprintf("plugin/%s/account/%d/%s", pluginID, accountID, value) }
	switch request.Operation {
	case pluginv1.StorageGet:
		value, found, err := a.store.Get(ctx, namespace(request.Namespace), request.Key)
		return pluginv1.StorageResult{Found: found, Value: value}, err
	case pluginv1.StorageSet:
		if err := a.store.Set(ctx, namespace(request.Namespace), request.Key, request.Value); err != nil {
			return pluginv1.StorageResult{}, err
		}
		return pluginv1.StorageResult{Committed: true}, nil
	case pluginv1.StorageDelete:
		if err := a.store.Delete(ctx, namespace(request.Namespace), request.Key); err != nil {
			return pluginv1.StorageResult{}, err
		}
		return pluginv1.StorageResult{Committed: true}, nil
	case pluginv1.StorageList:
		entries, err := a.store.List(ctx, namespace(request.Namespace))
		if err != nil {
			return pluginv1.StorageResult{}, err
		}
		result := pluginv1.StorageResult{Entries: make([]pluginv1.StorageEntry, len(entries))}
		for i, entry := range entries {
			result.Entries[i] = pluginv1.StorageEntry{Key: entry.Key, Value: entry.Value}
		}
		return result, nil
	case pluginv1.StorageBatch:
		ops := make([]storage.Operation, len(request.Batch))
		for i, item := range request.Batch {
			typ := storage.OperationDelete
			if item.Operation == pluginv1.StorageSet {
				typ = storage.OperationSet
			}
			ops[i] = storage.Operation{Type: typ, Namespace: namespace(item.Namespace), Key: item.Key, Value: item.Value}
		}
		if err := a.store.Batch(ctx, ops); err != nil {
			return pluginv1.StorageResult{}, err
		}
		return pluginv1.StorageResult{Committed: true}, nil
	default:
		return pluginv1.StorageResult{}, fmt.Errorf("%w: unknown storage operation", pluginv1.ErrInvalidDTO)
	}
}

func (a *App) HandleHostClient(ctx context.Context, pluginID string, accountID int64, request pluginv1.ClientRequest) (pluginv1.ClientResult, error) {
	if err := request.Validate(); err != nil {
		return pluginv1.ClientResult{}, &pluginv1.Error{Code: pluginv1.ErrorInvalidRequest, Operation: string(request.Operation), Message: err.Error()}
	}
	if reader, ok := a.transport.(telegram.ReadClient); ok {
		return reader.Read(ctx, telegram.ReadScope{PluginID: pluginID, AccountID: accountID}, request)
	}
	reader, ok := a.transport.(ClientReader)
	if !ok {
		return pluginv1.ClientResult{}, &pluginv1.Error{Code: pluginv1.ErrorUnsupported, Message: "Telegram read surface is unavailable"}
	}
	switch request.Operation {
	case pluginv1.ClientGetMe:
		me, err := reader.GetMe(ctx)
		return pluginv1.ClientResult{Me: me}, err
	case pluginv1.ClientGetMessage:
		message, err := reader.GetMessage(ctx, request.PeerID, request.MessageID)
		return pluginv1.ClientResult{Message: message}, err
	case pluginv1.ClientResolvePeer:
		peer, err := reader.ResolvePeer(ctx, request.PeerID, request.Username)
		return pluginv1.ClientResult{Peer: peer}, err
	default:
		return pluginv1.ClientResult{}, fmt.Errorf("%w: unsupported client operation", pluginv1.ErrInvalidDTO)
	}
}

func (a *App) HandleHostModeration(ctx context.Context, pluginID string, accountID int64, grantedPermission string, request pluginv1.ModerationRequest) (pluginv1.ModerationResult, error) {
	started := time.Now()
	defer func() {
		_ = a.metrics.Add("serenox_moderation_latency_ms_total", map[string]string{"operation": string(request.Operation)}, time.Since(started).Milliseconds())
	}()
	if err := request.Validate(); err != nil {
		return pluginv1.ModerationResult{}, &pluginv1.Error{Code: pluginv1.ErrorInvalidRequest, Operation: string(request.Operation), Message: err.Error()}
	}
	if grantedPermission != pluginv1.ModerationPermission(request.Operation) {
		return pluginv1.ModerationResult{}, &pluginv1.Error{Code: pluginv1.ErrorPermissionDenied, Operation: string(request.Operation), Message: "moderation permission does not match operation"}
	}
	moderator, ok := a.transport.(telegram.ModerationClient)
	if !ok {
		return pluginv1.ModerationResult{}, &pluginv1.Error{Code: pluginv1.ErrorUnsupported, Operation: string(request.Operation), Message: "Telegram moderation surface is unavailable"}
	}
	result, err := moderator.Moderate(ctx, telegram.ModerationScope{PluginID: pluginID, AccountID: accountID, Permission: grantedPermission}, request)
	state := string(result.Status)
	if state == "" {
		state = "failure"
	}
	_ = a.metrics.Add("serenox_moderation_requests_total", map[string]string{"operation": string(request.Operation), "state": state}, 1)
	if result.IdempotentReplay {
		_ = a.metrics.Add("serenox_moderation_idempotency_hits_total", nil, 1)
	}
	return result, err
}

// HandleHostHTTP is the core-owned outbound-only network boundary. The
// verified package policy is supplied by the supervisor, never by the plugin.
func (a *App) HandleHostHTTP(ctx context.Context, pluginID string, _ int64, policy pluginpackage.NetworkPolicy, request pluginv1.HTTPRequest) (pluginv1.HTTPResponse, error) {
	if a.http == nil {
		return pluginv1.HTTPResponse{}, errors.New("HTTP service is unavailable")
	}
	return a.http.Do(ctx, pluginID, policy, request)
}
