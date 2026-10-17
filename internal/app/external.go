package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/nihmadev/Serenox/internal/dispatch"
	"github.com/nihmadev/Serenox/internal/eventbus"
	"github.com/nihmadev/Serenox/internal/executor"
	"github.com/nihmadev/Serenox/internal/jobs"
	pluginv1 "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

// RegisterExternal atomically publishes a ready process plugin's command and
// watcher declarations. On any conflict or invalid filter, it rolls back every
// registration and joins the newly-created worker pool before returning.
func (a *App) RegisterExternal(ctx context.Context, metadata pluginv1.PluginMetadata, handler ExternalDispatch) error {
	if ctx == nil {
		return errors.New("external plugin registration requires a context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	a.externalMu.Lock()
	defer a.externalMu.Unlock()
	if a.closing.Load() {
		return errors.New("application is shutting down")
	}
	if handler == nil {
		return errors.New("external plugin dispatch is required")
	}
	if err := metadata.Validate(); err != nil {
		return fmt.Errorf("external plugin metadata: %w", err)
	}
	if strings.HasPrefix(metadata.ID, "core.") {
		return errors.New("external plugin id uses the protected core. namespace")
	}
	if a.executor == nil || a.ctx == nil {
		return errors.New("application runtime is not started")
	}
	if len(metadata.Commands) > 0 && !hasCapability(metadata.Capabilities, pluginv1.CapabilityCommands) {
		return errors.New("plugin declares commands without the commands capability")
	}
	if len(metadata.Watchers) > 0 && !hasCapability(metadata.Capabilities, pluginv1.CapabilityWatchers) {
		return errors.New("plugin declares watchers without the watchers capability")
	}
	if (len(metadata.InlineHandlers) > 0 || len(metadata.CallbackHandlers) > 0) && !hasCapability(metadata.Capabilities, pluginv1.CapabilityInline) {
		return errors.New("plugin declares inline or callback handlers without the inline capability")
	}
	if len(metadata.PreSendHandlers) > 0 && !hasCapability(metadata.Capabilities, pluginv1.CapabilityPreSend) {
		return errors.New("plugin declares pre-send handlers without the pre_send capability")
	}
	if len(metadata.ControlCommands) > 0 && !hasCapability(metadata.Capabilities, pluginv1.CapabilityControlBot) {
		return errors.New("plugin declares control commands without the control_bot capability")
	}
	if len(metadata.Jobs) > 0 && !hasCapability(metadata.Capabilities, pluginv1.CapabilityJobs) {
		return errors.New("plugin declares jobs without the jobs capability")
	}
	eventPermissions := make([]string, 0, len(metadata.EventPublications)+len(metadata.EventSubscriptions))
	for _, declaration := range metadata.EventPublications {
		eventPermissions = append(eventPermissions, pluginv1.EventPermission("publish", declaration.Type, declaration.Version))
	}
	for _, declaration := range metadata.EventSubscriptions {
		eventPermissions = append(eventPermissions, pluginv1.EventPermission("subscribe", declaration.Type, declaration.Version))
	}
	if err := a.events.Register(eventbus.Registration{PluginID: metadata.ID, AccountID: a.SelfID(), Capabilities: metadata.Capabilities, Permissions: eventPermissions, Publications: metadata.EventPublications, Subscriptions: metadata.EventSubscriptions}); err != nil {
		return fmt.Errorf("register event declarations: %w", err)
	}
	_, err := a.executor.Register(metadata.ID, a.executorConfig(), func(jobCtx context.Context, job executor.Job) error {
		if job.Kind == executor.KindEvent {
			work, ok := job.Payload.(eventWork)
			if !ok {
				return errors.New("external plugin received invalid event work")
			}
			return a.dispatchEvent(jobCtx, metadata.ID, handler, work)
		}
		if job.Kind == executor.KindScheduled {
			run, ok := job.Payload.(jobs.Run)
			if !ok {
				return errors.New("external plugin received invalid scheduled job payload")
			}
			response, dispatchErr := handler(jobCtx, pluginv1.DispatchRequest{Job: &run.Invocation})
			if dispatchErr == nil && jobCtx.Err() != nil {
				dispatchErr = jobCtx.Err()
			}
			if dispatchErr == nil {
				dispatchErr = response.Validate()
			}
			if dispatchErr == nil {
				dispatchErr = a.applyActionsForPlugin(jobCtx, metadata.ID, response.Actions)
			}
			if run.Complete != nil {
				run.Complete(dispatchErr)
			}
			return dispatchErr
		}
		request, ok := job.Payload.(pluginv1.DispatchRequest)
		if !ok {
			return errors.New("external plugin received invalid dispatch payload")
		}
		response, dispatchErr := handler(jobCtx, request)
		if dispatchErr != nil {
			return dispatchErr
		}
		if validateErr := response.Validate(); validateErr != nil {
			return fmt.Errorf("external plugin response: %w", validateErr)
		}
		return a.applyActionsForPlugin(jobCtx, metadata.ID, response.Actions)
	}, executor.Hooks{OnResult: func(result executor.Result) {
		if result.Error != nil {
			a.options.Logger.Error("external plugin job failed", "plugin", result.PluginID, "job", result.Job.ID, "kind", result.Job.Kind, "error", result.Error)
		}
	}})
	if err != nil {
		a.events.Unregister(metadata.ID, a.SelfID())
		return fmt.Errorf("create external plugin executor: %w", err)
	}
	rollback := func(cause error) error {
		a.events.Unregister(metadata.ID, a.SelfID())
		a.jobMu.Lock()
		delete(a.jobHandlers, metadata.ID)
		a.jobMu.Unlock()
		a.commands.UnregisterPlugin(metadata.ID)
		a.watchers.UnregisterPlugin(metadata.ID)
		a.inlines.UnregisterPlugin(metadata.ID)
		a.callbacks.UnregisterPlugin(metadata.ID)
		a.controlCommands.UnregisterPlugin(metadata.ID)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), a.options.Config.ShutdownTimeout.Duration())
		defer cancel()
		if closeErr := a.executor.Unregister(shutdownCtx, metadata.ID); closeErr != nil {
			return errors.Join(cause, closeErr)
		}
		return cause
	}
	for _, command := range metadata.Commands {
		target := dispatch.CommandTarget{
			Name: command.Name, PluginID: metadata.ID, HandlerID: command.Name,
		}
		if err := a.commands.Register(target, command.Aliases...); err != nil {
			return rollback(fmt.Errorf("register external command %s: %w", command.Name, err))
		}
	}
	for _, watcher := range metadata.Watchers {
		filterSpec, err := protocolWatcherSpec(watcher.Filter)
		if err != nil {
			return rollback(fmt.Errorf("register external watcher %s: %w", watcher.ID, err))
		}
		compiled, err := dispatch.CompileWatcher(filterSpec, a.parser.Load())
		if err != nil {
			return rollback(fmt.Errorf("register external watcher %s: %w", watcher.ID, err))
		}
		if err := a.watchers.Register(dispatch.WatcherTarget{
			PluginID: metadata.ID, HandlerID: watcher.ID, Filter: compiled,
		}); err != nil {
			return rollback(fmt.Errorf("register external watcher %s: %w", watcher.ID, err))
		}
	}
	for _, handler := range metadata.InlineHandlers {
		if err := a.inlines.Register(dispatch.InlineTarget{PluginID: metadata.ID, HandlerID: handler.Name, QueryPrefix: handler.QueryPrefix, SenderIDs: handler.SenderIDs, AllowedUsers: handler.AllowedUsers, OwnerOnly: handler.OwnerOnly}); err != nil {
			return rollback(fmt.Errorf("register external inline handler %s: %w", handler.Name, err))
		}
	}
	for _, handler := range metadata.CallbackHandlers {
		if err := a.callbacks.Register(dispatch.CallbackTarget{PluginID: metadata.ID, HandlerID: handler.Name, Data: handler.Data, DataPrefix: handler.DataPrefix, ChatIDs: handler.ChatIDs, SenderIDs: handler.SenderIDs, AllowedUsers: handler.AllowedUsers, OwnerOnly: handler.OwnerOnly}); err != nil {
			return rollback(fmt.Errorf("register external callback handler %s: %w", handler.Name, err))
		}
	}
	if err := a.controlCommands.Register(metadata.ID, metadata.ControlCommands, metadata.Capabilities); err != nil {
		return rollback(fmt.Errorf("register external control commands: %w", err))
	}
	a.preSendMu.Lock()
	for _, preSend := range metadata.PreSendHandlers {
		a.preSendHooks = append(a.preSendHooks, preSendHook{pluginID: metadata.ID, name: preSend.Name, handler: handler})
	}
	a.preSendMu.Unlock()
	declaredJobs := make(map[string]pluginv1.JobHandlerSpec, len(metadata.Jobs))
	for _, job := range metadata.Jobs {
		declaredJobs[job.Name] = job
	}
	a.jobMu.Lock()
	a.jobHandlers[metadata.ID] = declaredJobs
	a.jobMu.Unlock()
	if a.jobs != nil {
		if err := a.jobs.SetOwnerState(ctx, metadata.ID, a.SelfID(), pluginv1.JobStateEnabled); err != nil {
			return rollback(fmt.Errorf("resume plugin jobs: %w", err))
		}
	}
	return nil
}

// UnregisterExternal stops admission first and then drains the plugin's fixed
// worker pool within the caller's deadline.
func (a *App) UnregisterExternal(ctx context.Context, pluginID string) error {
	if ctx == nil {
		return errors.New("external plugin unregistration requires a context")
	}
	if strings.HasPrefix(pluginID, "core.") {
		return errors.New("core plugins are protected")
	}
	a.externalMu.Lock()
	defer a.externalMu.Unlock()
	profileErr := a.releaseProfileOwner(ctx, pluginID)
	if a.jobs != nil {
		_ = a.jobs.SetOwnerState(ctx, pluginID, a.SelfID(), pluginv1.JobStatePaused)
	}
	a.commands.UnregisterPlugin(pluginID)
	a.watchers.UnregisterPlugin(pluginID)
	a.inlines.UnregisterPlugin(pluginID)
	a.callbacks.UnregisterPlugin(pluginID)
	a.controlCommands.UnregisterPlugin(pluginID)
	a.events.Unregister(pluginID, a.SelfID())
	a.preSendMu.Lock()
	filtered := a.preSendHooks[:0]
	for _, hook := range a.preSendHooks {
		if hook.pluginID != pluginID {
			filtered = append(filtered, hook)
		}
	}
	a.preSendHooks = filtered
	a.preSendMu.Unlock()
	a.jobMu.Lock()
	delete(a.jobHandlers, pluginID)
	a.jobMu.Unlock()
	if a.idempotency != nil {
		a.idempotency.ForgetScope(pluginID + "/account/" + fmt.Sprint(a.SelfID()))
	}
	if a.executor == nil {
		return errors.New("application runtime is not started")
	}
	return errors.Join(profileErr, a.executor.Unregister(ctx, pluginID))
}

func hasCapability(capabilities []pluginv1.Capability, expected pluginv1.Capability) bool {
	return slices.Contains(capabilities, expected)
}

func protocolWatcherSpec(filter pluginv1.WatcherFilter) (dispatch.WatcherSpec, error) {
	direction := dispatch.DirectionAny
	if filter.Incoming && !filter.Outgoing {
		direction = dispatch.DirectionIncoming
	} else if filter.Outgoing && !filter.Incoming {
		direction = dispatch.DirectionOutgoing
	}
	kinds := make([]string, len(filter.Kinds))
	for index, kind := range filter.Kinds {
		switch kind {
		case pluginv1.UpdateMessage, pluginv1.UpdateEditedMessage, pluginv1.UpdateDeleted:
			kinds[index] = string(kind)
		default:
			return dispatch.WatcherSpec{}, fmt.Errorf("update kind %q has no message watcher DTO; use an inline/callback capability", kind)
		}
	}
	return dispatch.WatcherSpec{
		UpdateKinds: kinds, Direction: direction, TextPrefix: filter.TextPrefix,
		ChatIDs: slices.Clone(filter.ChatIDs), SenderIDs: slices.Clone(filter.SenderIDs),
	}, nil
}
