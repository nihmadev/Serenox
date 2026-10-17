package app

import (
	"errors"
	"log/slog"
	"time"

	"github.com/nihmadev/Serenox/internal/controlbot"
	"github.com/nihmadev/Serenox/internal/dispatch"
	"github.com/nihmadev/Serenox/internal/eventbus"
	"github.com/nihmadev/Serenox/internal/httpservice"
	"github.com/nihmadev/Serenox/internal/modules"
	"github.com/nihmadev/Serenox/internal/observability"
	"github.com/nihmadev/Serenox/internal/ratelimit"
	"github.com/nihmadev/Serenox/internal/storage"
	"github.com/nihmadev/Serenox/internal/telegram"
	pluginv1 "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

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
	if o.HTTPService == nil {
		service, serviceErr := httpservice.New(httpservice.Config{
			MaxRequestBytes: o.Config.HTTP.MaxRequestBytes, MaxResponseBytes: o.Config.HTTP.MaxResponseBytes,
			MaxHeaders: o.Config.HTTP.MaxHeaders, MaxHeaderBytes: o.Config.HTTP.MaxHeaderBytes,
			MaxConcurrent: o.Config.HTTP.MaxConcurrent, QueueCapacity: o.Config.HTTP.QueueCapacity,
			RequestsPerSecond: o.Config.HTTP.RequestsPerSecond, Burst: o.Config.HTTP.Burst,
		})
		if serviceErr != nil {
			return Options{}, serviceErr
		}
		o.HTTPService = service
	}
	return o, nil
}

// New validates options and composes the application-owned runtime. Runtime
// goroutines are started by Run, keeping construction side-effect free apart
// from creating local resources such as the default store and HTTP service.
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
	inlines, err := dispatch.NewInlineRouter(pluginv1.MaxHandlers)
	if err != nil {
		return nil, err
	}
	callbacks, err := dispatch.NewCallbackRouter(pluginv1.MaxCollectionItems)
	if err != nil {
		return nil, err
	}
	inlines.SetOwnerID(options.SelfID)
	callbacks.SetOwnerID(options.SelfID)
	controlCommands, err := controlbot.NewRegistry(options.SelfID, pluginv1.MaxControlCommands)
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
		commands: commands, watchers: watchers, inlines: inlines, callbacks: callbacks, controlCommands: controlCommands, controlBot: options.ControlBot, limiter: limiter, ingress: ingress,
		metrics: metrics, health: health, sampler: sampler, modules: moduleRegistry, http: options.HTTPService,
		profile:      newProfileLeaseManager(),
		preSendHooks: make([]preSendHook, 0),
		jobHandlers:  make(map[string]map[string]pluginv1.JobHandlerSpec),
		startedAt:    time.Now().UTC(),
	}
	app.idempotency, err = telegram.NewIdempotencyStore(0, 0)
	if err != nil {
		return nil, err
	}
	eventStore, err := eventbus.NewStorageStore(store)
	if err != nil {
		return nil, err
	}
	app.events, err = eventbus.New(eventStore, eventbus.RandomIDs{}, nil, fabricMetrics{app: app}, eventbus.DefaultLimits())
	if err != nil {
		return nil, err
	}
	coreTypes := []string{"telegram.message.received", "telegram.message.edited", "telegram.message.deleted", "telegram.callback.received", "telegram.inline_query.received"}
	corePublications := make([]pluginv1.EventPublication, 0, len(coreTypes))
	corePermissions := make([]string, 0, len(coreTypes))
	for _, eventType := range coreTypes {
		corePublications = append(corePublications, pluginv1.EventPublication{Type: eventType, Version: 1, MaxPayloadBytes: pluginv1.MaxEventPayloadBytes, DeliveryClass: pluginv1.EventDurable})
		corePermissions = append(corePermissions, pluginv1.EventPermission("publish", eventType, 1))
	}
	if err := app.events.Register(eventbus.Registration{PluginID: "core.telegram", AccountID: options.SelfID, Capabilities: []pluginv1.Capability{pluginv1.CapabilityEventsPublish}, Permissions: corePermissions, Publications: corePublications}); err != nil {
		return nil, err
	}
	app.parser.Store(parser)
	app.prefix.Store(options.Config.Dispatch.CommandPrefix)
	return app, nil
}
