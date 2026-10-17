package app

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/nihmadev/Serenox/internal/dispatch"
	"github.com/nihmadev/Serenox/internal/eventbus"
	"github.com/nihmadev/Serenox/internal/executor"
	"github.com/nihmadev/Serenox/internal/telegram"
	pluginv1 "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

var errEventRateLimited = errors.New("event rate limit exceeded")

// admit is the bounded transport admission point. Commands use their own lane
// because control messages must not be starved by watcher traffic.
func (a *App) admit(ctx context.Context, update pluginv1.Update) error {
	message, hasMessage := telegram.ToDispatchMessage(update)
	if hasMessage {
		if _, command := a.parser.Load().Parse(message); command {
			enqueueCtx, cancel := context.WithTimeout(ctx, a.options.CommandEnqueueTimeout)
			defer cancel()
			a.pending.Add(1)
			if err := a.ingress.SubmitCommand(enqueueCtx, update); err != nil {
				a.pending.Done()
				_ = a.metrics.Add("serenox_ingress_rejected_total", map[string]string{"lane": "command"}, 1)
				return err
			}
			return nil
		}
	}
	a.pending.Add(1)
	if err := a.ingress.TryUpdate(update); err != nil {
		a.pending.Done()
		_ = a.metrics.Add("serenox_ingress_rejected_total", map[string]string{"lane": "update"}, 1)
		if errors.Is(err, telegram.ErrIngressFull) {
			return nil // watcher backpressure is an observed drop, not transport failure.
		}
		return err
	}
	return nil
}

func (a *App) ingressWorker() {
	defer a.workers.Done()
	for {
		update, err := a.ingress.Receive(a.ctx)
		if err != nil {
			return
		}
		a.dispatchUpdate(a.ctx, update)
		a.pending.Done()
	}
}

func (a *App) dispatchUpdate(ctx context.Context, update pluginv1.Update) {
	if coreEvent, err := eventbus.CoreEventFromUpdate(update); err == nil {
		if _, publishErr := a.events.Publish(ctx, eventbus.Identity{PluginID: "core.telegram", AccountID: a.SelfID()}, coreEvent); publishErr != nil {
			a.options.Logger.Error("core event publication failed", "type", coreEvent.Type, "error", publishErr)
		}
	}
	if update.Message != nil && update.Message.Media == nil && update.Message.ReplyToMessageID != 0 {
		if reader, ok := a.transport.(ClientReader); ok {
			if replied, err := reader.GetMessage(ctx, update.Message.ChatID, update.Message.ReplyToMessageID); err == nil && replied != nil {
				update.Message.Media = replied.Media
			}
		}
	}
	message, hasMessage := telegram.ToDispatchMessage(update)
	if hasMessage {
		parser := a.parser.Load()
		command, isCommand := parser.Parse(message)
		if isCommand {
			target, resolved := a.commands.Resolve(command.Name)
			if resolved {
				userDecision := a.limiter.Allow("user:" + strconv.FormatInt(message.SenderID, 10))
				chatDecision := a.limiter.Allow("chat:" + strconv.FormatInt(message.ChatID, 10))
				if userDecision.Allowed && chatDecision.Allowed {
					a.enqueueCommand(ctx, update, command, target)
				} else {
					_ = a.metrics.Add("serenox_commands_rate_limited_total", nil, 1)
				}
			}
		}
		a.routeWatchers(ctx, update, message)
	}
	switch update.Kind {
	case pluginv1.UpdateInlineQuery:
		if update.InlineQuery == nil {
			return
		}
		query := dispatch.InlineQuery{ID: update.InlineQuery.ID, SenderID: update.InlineQuery.SenderID, Query: update.InlineQuery.Query}
		a.inlines.Route(ctx, query, func(_ context.Context, target dispatch.InlineTarget) error {
			if decision := a.limiter.Allow("user:" + strconv.FormatInt(query.SenderID, 10)); !decision.Allowed {
				return errEventRateLimited
			}
			request := pluginv1.DispatchRequest{Update: update}
			err := a.executor.TrySubmit(target.PluginID, executor.Job{ID: update.ID, HandlerID: target.HandlerID, Kind: executor.KindInline, Payload: request})
			a.observeEvent("inline", err)
			return err
		})
	case pluginv1.UpdateCallback:
		if update.Callback == nil {
			return
		}
		callback := dispatch.Callback{ID: update.Callback.ID, SenderID: update.Callback.SenderID, ChatID: update.Callback.ChatID, Data: update.Callback.Data}
		a.callbacks.Route(ctx, callback, func(_ context.Context, target dispatch.CallbackTarget) error {
			if decision := a.limiter.Allow("user:" + strconv.FormatInt(callback.SenderID, 10)); !decision.Allowed {
				return errEventRateLimited
			}
			if callback.ChatID != 0 {
				if decision := a.limiter.Allow("chat:" + strconv.FormatInt(callback.ChatID, 10)); !decision.Allowed {
					return errEventRateLimited
				}
			}
			request := pluginv1.DispatchRequest{Update: update}
			err := a.executor.TrySubmit(target.PluginID, executor.Job{ID: update.ID, HandlerID: target.HandlerID, Kind: executor.KindCallback, Payload: request})
			a.observeEvent("callback", err)
			return err
		})
	case pluginv1.UpdateDeleted:
		a.routeWatchers(ctx, update, dispatch.Message{UpdateKind: string(pluginv1.UpdateDeleted)})
	}
}

func (a *App) routeWatchers(ctx context.Context, update pluginv1.Update, message dispatch.Message) {
	a.watchers.Route(ctx, message, func(_ context.Context, target dispatch.WatcherTarget, _ dispatch.Message) error {
		request := pluginv1.DispatchRequest{Update: update}
		err := a.executor.TrySubmit(target.PluginID, executor.Job{ID: update.ID, HandlerID: target.HandlerID, Kind: executor.KindWatcher, Payload: request})
		a.observeEvent("watcher", err)
		return err
	})
}

func (a *App) observeEvent(kind string, err error) {
	outcome := "queued"
	if errors.Is(err, errEventRateLimited) {
		outcome = "rate_limited"
	}
	if err != nil && outcome == "queued" {
		outcome = "rejected"
	}
	a.observeEventOutcome(kind, outcome)
}

func (a *App) observeEventOutcome(kind, outcome string) {
	_ = a.metrics.Add("serenox_events_total", map[string]string{"kind": kind, "outcome": outcome}, 1)
}

func (a *App) enqueueCommand(ctx context.Context, update pluginv1.Update, command dispatch.Command, target dispatch.CommandTarget) {
	protocolCommand := pluginv1.Command{
		Name: target.Name, Prefix: command.Prefix, RawArgs: command.Args,
		Args: strings.Fields(command.Args), Message: *update.Message,
	}
	request := pluginv1.DispatchRequest{Update: update, Command: &protocolCommand}
	enqueueCtx, cancel := context.WithTimeout(ctx, a.options.CommandEnqueueTimeout)
	defer cancel()
	err := a.executor.Submit(enqueueCtx, target.PluginID, executor.Job{
		ID: update.ID, HandlerID: target.HandlerID, Kind: executor.KindCommand, Payload: request,
	})
	if err == nil {
		_ = a.metrics.Add("serenox_commands_enqueued_total", map[string]string{"plugin": target.PluginID}, 1)
		return
	}
	_ = a.metrics.Add("serenox_executor_rejected_total", map[string]string{"plugin": target.PluginID}, 1)
	busy := pluginv1.Action{
		ID: "busy-" + update.ID, Kind: pluginv1.ActionEdit,
		ChatID: command.Message.ChatID, MessageID: command.Message.ID, Text: "Serenox is busy; retry shortly.",
	}
	_, _ = a.transport.Apply(ctx, busy)
}
