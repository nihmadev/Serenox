package app

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/nihmadev/Serenox/internal/controlbot"
	"github.com/nihmadev/Serenox/internal/dispatch"
	"github.com/nihmadev/Serenox/internal/executor"
	pluginv1 "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

// HandleControlUpdate is the Bot API ingress boundary. It receives only a
// validated control DTO from the core-owned poller and never synthesizes a
// user-session message, preserving the ordinary owner's command parser.
func (a *App) HandleControlUpdate(ctx context.Context, update pluginv1.Update) error {
	if err := update.Validate(); err != nil {
		return err
	}
	if a.closing.Load() || a.executor == nil || update.Control == nil {
		return errors.New("control bot is unavailable")
	}
	control := update.Control
	var name, argument string
	var surface pluginv1.ControlSurface
	switch update.Kind {
	case pluginv1.UpdateControlMessage:
		name, argument, surface = control.Command, control.CommandArguments, pluginv1.ControlSurfacePrivate
	case pluginv1.UpdateControlInlineQuery:
		name, argument = splitControlQuery(control.Query)
		surface = pluginv1.ControlSurfaceInline
		if handled, err := a.handleBuiltinControlInline(ctx, update, name); handled {
			return err
		}
	case pluginv1.UpdateControlCallback:
		callback := dispatch.Callback{ID: control.QueryID, SenderID: control.Actor.ID, ChatID: control.ChatID, Data: control.CallbackData}
		result := a.callbacks.Route(ctx, callback, func(queueCtx context.Context, target dispatch.CallbackTarget) error {
			return a.executor.TrySubmit(target.PluginID, executor.Job{ID: update.ID, HandlerID: target.HandlerID, Kind: executor.KindCallback, Payload: pluginv1.DispatchRequest{Update: update}})
		})
		if result.Enqueued == 0 {
			a.answerControlCallback(ctx, control.QueryID, "This control has expired or is unavailable.", true)
			return errors.New("control callback was not admitted")
		}
		a.answerControlCallback(ctx, control.QueryID, "", false)
		return nil
	default:
		return errors.New("unsupported control-bot update")
	}
	target, err := a.controlCommands.Resolve(name, control.Actor.ID, surface, argument)
	if err != nil {
		return err
	}
	if surface == pluginv1.ControlSurfaceInline && !target.Command.AllowQueryExecution {
		return controlbot.ErrCommandConfirmation
	}
	deadline := 2 * time.Second
	if surface == pluginv1.ControlSurfacePrivate {
		deadline = a.options.Config.Execution.HandlerTimeout.Duration()
	}
	queueCtx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	request := pluginv1.DispatchRequest{Update: update}
	err = a.executor.Submit(queueCtx, target.PluginID, executor.Job{ID: update.ID, HandlerID: target.Command.HandlerID, Kind: executor.KindCommand, Payload: request})
	if err != nil {
		return err
	}
	_ = a.metrics.Add("serenox_control_commands_total", map[string]string{"outcome": "queued"}, 1)
	return nil
}

// handleBuiltinControlInline supplies a non-mutating production smoke path:
// typing @bot, @bot help, or @bot ping always verifies polling and answerInline
// without requiring any plugin package. It is owner-only and intentionally
// returns static information; community commands use ControlCommand metadata.
func (a *App) handleBuiltinControlInline(ctx context.Context, update pluginv1.Update, name string) (bool, error) {
	if update.Control == nil || update.Control.Actor.ID != a.SelfID() || a.controlBot == nil {
		return false, nil
	}
	if name != "" && name != "help" && name != "ping" {
		return false, nil
	}
	title, text := "Serenox control bot", "Control bot is online. Try a community command exposed for inline use."
	if name == "ping" {
		title, text = "Serenox control bot: pong", "Polling and inline answers are working."
	}
	ctx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	_, err := a.controlBot.Apply(ctx, pluginv1.Action{ID: "control-smoke-" + update.ID, Kind: pluginv1.ActionBotAnswerInline, ResponseChannel: pluginv1.ResponseChannelControlBot, InlineQueryID: update.Control.QueryID, IsPersonal: true, CacheTimeSeconds: 0, InlineResults: []pluginv1.InlineResult{{ID: "serenox-control", Type: "article", Title: title, Description: "Owner-only control transport", Text: text}}})
	return true, err
}

func (a *App) answerControlCallback(parent context.Context, callbackID, text string, alert bool) {
	if a.controlBot == nil || callbackID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(parent, 1500*time.Millisecond)
	defer cancel()
	_, _ = a.controlBot.Apply(ctx, pluginv1.Action{ID: "control-callback-" + callbackID, Kind: pluginv1.ActionBotAnswerCallback, ResponseChannel: pluginv1.ResponseChannelControlBot, CallbackID: callbackID, CallbackText: text, ShowAlert: alert})
}

func splitControlQuery(query string) (string, string) {
	query = strings.TrimSpace(query)
	if query == "" {
		return "", ""
	}
	parts := strings.Fields(query)
	name := strings.ToLower(parts[0])
	return name, strings.TrimSpace(strings.TrimPrefix(query, parts[0]))
}
