// Package app composes the bounded core and owns startup/shutdown ordering.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nihmadev/Serenox/internal/builtin"
	"github.com/nihmadev/Serenox/internal/config"
	"github.com/nihmadev/Serenox/internal/dispatch"
	"github.com/nihmadev/Serenox/internal/executor"
	"github.com/nihmadev/Serenox/internal/modules"
	"github.com/nihmadev/Serenox/internal/observability"
	"github.com/nihmadev/Serenox/internal/ratelimit"
	"github.com/nihmadev/Serenox/internal/storage"
	"github.com/nihmadev/Serenox/internal/telegram"
	pluginv1 "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

type Options struct {
	Config                config.Runtime
	SelfID                int64
	Transport             telegram.Transport
	Store                 storage.Store
	Logger                *slog.Logger
	IngressCapacity       int
	CommandCapacity       int
	IngressWorkers        int
	CommandEnqueueTimeout time.Duration
	MaxMetricSeries       int
	PluginController      builtin.PluginController
	RequestRestart        func(context.Context, bool) error
}

// ExternalDispatch is the transport-neutral call implemented by a supervised
// plugin process. RegisterExternal wraps it in the same bounded executor used
// by built-ins; no RPC call is launched directly from Telegram admission.
type ExternalDispatch func(context.Context, pluginv1.DispatchRequest) (pluginv1.DispatchResponse, error)

func (o Options) normalized() (Options, error) {
	if err := o.Config.Validate(); err != nil {
		return Options{}, err
	}
	if o.SelfID == 0 || o.Transport == nil {
		return Options{}, errors.New("application requires self ID and Telegram transport")
	}
	if o.IngressCapacity == 0 {
		o.IngressCapacity = 1024
	}
	if o.CommandCapacity == 0 {
		o.CommandCapacity = 64
	}
	if o.IngressWorkers == 0 {
		o.IngressWorkers = 2
	}
	if o.CommandEnqueueTimeout == 0 {
		o.CommandEnqueueTimeout = 250 * time.Millisecond
	}
	if o.MaxMetricSeries == 0 {
		o.MaxMetricSeries = 1024
	}
	if o.IngressCapacity < 1 || o.IngressCapacity > 1_000_000 || o.CommandCapacity < 1 || o.CommandCapacity > 65_536 ||
		o.IngressWorkers < 1 || o.IngressWorkers > 256 || o.CommandEnqueueTimeout <= 0 || o.CommandEnqueueTimeout > time.Minute ||
		o.MaxMetricSeries < 16 || o.MaxMetricSeries > 1_000_000 {
		return Options{}, errors.New("invalid application resource limits")
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	return o, nil
}

type App struct {
	options   Options
	transport telegram.Transport
	store     storage.Store
	ownsStore bool

	parser   atomic.Pointer[dispatch.Parser]
	prefix   atomic.Value // string
	commands *dispatch.CommandRegistry
	watchers *dispatch.WatcherRouter
	executor *executor.Manager
	limiter  *ratelimit.Limiter
	ingress  *telegram.Ingress
	metrics  *observability.Metrics
	health   *observability.Health
	sampler  *observability.Sampler
	modules  *modules.Registry
	builtins *builtin.Catalog

	ctx        context.Context
	cancel     context.CancelFunc
	workers    sync.WaitGroup
	pending    sync.WaitGroup
	startedAt  time.Time
	closeOnce  sync.Once
	closeErr   error
	externalMu sync.Mutex
	closing    atomic.Bool
}

func New(options Options) (*App, error) {
	options, err := options.normalized()
	if err != nil {
		return nil, err
	}
	parser, err := dispatch.NewParser(options.Config.Dispatch.CommandPrefix, options.SelfID)
	if err != nil {
		return nil, err
	}
	commands, err := dispatch.NewCommandRegistry(dispatch.RegistryLimits{
		MaxCommands: options.Config.Dispatch.MaxCommands, MaxAliases: options.Config.Dispatch.MaxAliases,
	})
	if err != nil {
		return nil, err
	}
	watchers, err := dispatch.NewWatcherRouter(options.Config.Dispatch.MaxWatchers)
	if err != nil {
		return nil, err
	}
	ingress, err := telegram.NewIngress(options.IngressCapacity, options.CommandCapacity)
	if err != nil {
		return nil, err
	}
	metrics, err := observability.NewMetrics(options.MaxMetricSeries)
	if err != nil {
		return nil, err
	}
	health, err := observability.NewHealth(options.Config.Execution.MaxPlugins + 8)
	if err != nil {
		return nil, err
	}
	sampler, err := observability.NewSampler(time.Second)
	if err != nil {
		return nil, err
	}
	limiterConfig, err := options.Config.RateLimiterConfig()
	if err != nil {
		return nil, err
	}
	limiter, err := ratelimit.New(limiterConfig)
	if err != nil {
		return nil, err
	}
	store := options.Store
	ownsStore := false
	if store == nil {
		storageOptions, storageErr := options.Config.StorageOptions()
		if storageErr != nil {
			return nil, storageErr
		}
		store, err = storage.NewFile(options.Config.StoragePath(), storageOptions)
		if err != nil {
			return nil, err
		}
		ownsStore = true
	}
	moduleRegistry, err := modules.NewRegistry(options.Config.Execution.MaxPlugins, modules.Services{})
	if err != nil {
		if ownsStore {
			_ = store.Close()
		}
		return nil, err
	}
	app := &App{
		options: options, transport: options.Transport, store: store, ownsStore: ownsStore,
		commands: commands, watchers: watchers, limiter: limiter, ingress: ingress,
		metrics: metrics, health: health, sampler: sampler, modules: moduleRegistry,
		startedAt: time.Now().UTC(),
	}
	app.parser.Store(parser)
	app.prefix.Store(options.Config.Dispatch.CommandPrefix)
	return app, nil
}

func (a *App) Run(parent context.Context) (returnErr error) {
	if parent == nil {
		return errors.New("nil application context")
	}
	if a.ctx != nil {
		return errors.New("application already started")
	}
	// Component ownership is independent from the caller's cancellation so the
	// shutdown path can drain accepted work after SIGTERM. The transport still
	// receives parent directly and stops admission immediately.
	a.ctx, a.cancel = context.WithCancel(context.Background())
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), a.options.Config.ShutdownTimeout.Duration())
		defer cancel()
		if err := a.Close(shutdownCtx); err != nil {
			returnErr = errors.Join(returnErr, err)
		}
	}()

	if err := a.health.Set("storage", observability.HealthReady, "open"); err != nil {
		return err
	}
	if err := a.sampler.Start(a.ctx); err != nil {
		return err
	}
	if err := a.limiter.Start(a.ctx); err != nil {
		return err
	}
	executorManager, err := executor.NewManager(a.ctx, a.options.Config.Execution.MaxPlugins)
	if err != nil {
		return err
	}
	a.executor = executorManager

	catalog, err := builtin.New(builtin.Services{
		Store: a.store, Plugins: a.options.PluginController, StartedAt: a.startedAt,
		Prefix: a.Prefix, SetPrefix: a.SetPrefix, RequestRestart: a.options.RequestRestart,
		Resources: a.sampler.Snapshot, RateStats: a.limiter.Stats, ExecutorStats: a.executorStats,
		ModuleStatuses: a.modules.Snapshot,
	}, a.applyActions)
	if err != nil {
		return err
	}
	a.builtins = catalog
	for _, definition := range catalog.Definitions() {
		if err := a.modules.Register(noopModule{metadata: modules.Metadata{
			ID: definition.ID, Name: definition.Name, Version: "1", Description: definition.Description, Core: true,
		}}); err != nil {
			return err
		}
	}
	if err := a.modules.ConfigureAll(a.ctx); err != nil {
		return err
	}
	if err := catalog.Start(a.ctx, a.executor, a.commands, a.executorConfig()); err != nil {
		return err
	}
	if err := a.modules.StartAll(a.ctx); err != nil {
		return err
	}
	if restorer, ok := a.options.PluginController.(interface{ Restore(context.Context) error }); ok {
		if err := restorer.Restore(a.ctx); err != nil {
			a.options.Logger.Error("one or more installed plugins failed to restore", "error", err)
			if healthErr := a.health.Set("plugins", observability.HealthDegraded, "one or more installed plugins failed to restore"); healthErr != nil {
				return healthErr
			}
		} else if err := a.health.Set("plugins", observability.HealthReady, "installed plugins restored"); err != nil {
			return err
		}
	}
	if err := a.health.Set("core", observability.HealthReady, "accepting updates"); err != nil {
		return err
	}

	for worker := 0; worker < a.options.IngressWorkers; worker++ {
		a.workers.Add(1)
		go a.ingressWorker()
	}
	transportErr := a.transport.Run(parent, telegram.SinkFunc(a.admit))
	if transportErr == nil {
		drained := make(chan struct{})
		go func() { a.pending.Wait(); close(drained) }()
		select {
		case <-drained:
		case <-a.ctx.Done():
		}
	}
	if transportErr != nil && !errors.Is(transportErr, context.Canceled) && !errors.Is(transportErr, telegram.ErrClosed) {
		return fmt.Errorf("Telegram transport: %w", transportErr)
	}
	return nil
}

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
	message, ok := telegram.ToDispatchMessage(update)
	if !ok {
		return
	}
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
	a.watchers.Route(ctx, message, func(_ context.Context, target dispatch.WatcherTarget, routed dispatch.Message) error {
		request := pluginv1.DispatchRequest{Update: update}
		return a.executor.TrySubmit(target.PluginID, executor.Job{
			ID: update.ID, HandlerID: target.HandlerID, Kind: executor.KindWatcher, Payload: request,
		})
	})
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

func (a *App) applyActions(ctx context.Context, actions []pluginv1.Action) error {
	_, err := telegram.ApplyBatch(ctx, a.transport, actions, pluginv1.MaxActions)
	return err
}

func (a *App) executorConfig() executor.Config {
	configValue, _ := a.options.Config.ExecutorConfig()
	return configValue
}

func (a *App) executorStats() map[string]executor.Stats {
	result := make(map[string]executor.Stats)
	if a.executor == nil || a.builtins == nil {
		return result
	}
	for _, definition := range a.builtins.Definitions() {
		if pool, ok := a.executor.Pool(definition.ID); ok {
			result[definition.ID] = pool.Stats()
		}
	}
	return result
}

func (a *App) Prefix() string { return a.prefix.Load().(string) }

func (a *App) SetPrefix(prefix string) error {
	parser, err := dispatch.NewParser(prefix, a.options.SelfID)
	if err != nil {
		return err
	}
	a.parser.Store(parser)
	a.prefix.Store(prefix)
	return nil
}

func (a *App) RegisterWatcher(target dispatch.WatcherTarget) error {
	return a.watchers.Register(target)
}

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
	_, err := a.executor.Register(metadata.ID, a.executorConfig(), func(jobCtx context.Context, job executor.Job) error {
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
		return a.applyActions(jobCtx, response.Actions)
	}, executor.Hooks{})
	if err != nil {
		return fmt.Errorf("create external plugin executor: %w", err)
	}
	rollback := func(cause error) error {
		a.commands.UnregisterPlugin(metadata.ID)
		a.watchers.UnregisterPlugin(metadata.ID)
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
			return rollback(fmt.Errorf("compile external watcher %s: %w", watcher.ID, err))
		}
		if err := a.watchers.Register(dispatch.WatcherTarget{
			PluginID: metadata.ID, HandlerID: watcher.ID, Filter: compiled,
		}); err != nil {
			return rollback(fmt.Errorf("register external watcher %s: %w", watcher.ID, err))
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
	a.commands.UnregisterPlugin(pluginID)
	a.watchers.UnregisterPlugin(pluginID)
	if a.executor == nil {
		return errors.New("application runtime is not started")
	}
	return a.executor.Unregister(ctx, pluginID)
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
		case pluginv1.UpdateMessage, pluginv1.UpdateEditedMessage:
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

func (a *App) Metrics() []observability.Point { return a.metrics.Snapshot() }

func (a *App) Health() []observability.HealthStatus { return a.health.Snapshot() }

func (a *App) Close(ctx context.Context) error {
	a.closeOnce.Do(func() {
		a.closing.Store(true)
		a.ingress.Close()
		pendingDone := make(chan struct{})
		go func() { a.pending.Wait(); close(pendingDone) }()
		select {
		case <-pendingDone:
		case <-ctx.Done():
			a.closeErr = errors.Join(a.closeErr, ctx.Err())
		}
		workersDone := make(chan struct{})
		go func() { a.workers.Wait(); close(workersDone) }()
		select {
		case <-workersDone:
		case <-ctx.Done():
			a.closeErr = errors.Join(a.closeErr, ctx.Err())
		}
		if a.builtins != nil && a.executor != nil {
			a.closeErr = errors.Join(a.closeErr, a.builtins.Stop(ctx, a.executor))
		}
		if closer, ok := a.options.PluginController.(interface{ Close(context.Context) error }); ok {
			a.closeErr = errors.Join(a.closeErr, closer.Close(ctx))
		}
		if a.executor != nil {
			a.externalMu.Lock()
			a.closeErr = errors.Join(a.closeErr, a.executor.Close(ctx))
			a.externalMu.Unlock()
		}
		// Keep action delivery available while accepted executor work drains;
		// close the Telegram connection only after every handler pool stopped.
		if err := a.transport.Close(ctx); err != nil {
			a.closeErr = errors.Join(a.closeErr, err)
		}
		a.closeErr = errors.Join(a.closeErr, a.modules.StopAll(ctx))
		a.closeErr = errors.Join(a.closeErr, a.limiter.Close(ctx))
		a.closeErr = errors.Join(a.closeErr, a.sampler.Close(ctx))
		if a.ownsStore {
			a.closeErr = errors.Join(a.closeErr, a.store.Close())
		}
		if a.cancel != nil {
			a.cancel()
		}
		_ = a.health.Set("core", observability.HealthStopped, "shutdown complete")
	})
	return a.closeErr
}

type noopModule struct{ metadata modules.Metadata }

func (m noopModule) Metadata() modules.Metadata                      { return m.metadata }
func (noopModule) Configure(context.Context, modules.Services) error { return nil }
func (noopModule) Start(context.Context) error                       { return nil }
func (noopModule) Stop(context.Context) error                        { return nil }
