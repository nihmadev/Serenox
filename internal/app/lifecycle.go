package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/nihmadev/Serenox/internal/builtin"
	"github.com/nihmadev/Serenox/internal/executor"
	"github.com/nihmadev/Serenox/internal/jobs"
	"github.com/nihmadev/Serenox/internal/modules"
	"github.com/nihmadev/Serenox/internal/observability"
	"github.com/nihmadev/Serenox/internal/telegram"
)

// Run starts the owned runtime, admits transport updates, and drains accepted
// work before returning. Startup and shutdown order live here so components do
// not need to know about one another's lifecycle.
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
	a.workers.Add(1)
	go a.runEventCoordinator()
	jobLimits := a.options.Config.JobsConfig()
	jobStore, storeErr := jobs.NewStorageStore(a.store, jobLimits.MaxJobsPerPlugin)
	if storeErr != nil {
		return storeErr
	}
	jobScheduler, schedulerErr := jobs.New(jobStore, jobs.Config{Maximum: jobLimits.MaxJobsPerPlugin, Executor: executorManager})
	if schedulerErr != nil {
		return schedulerErr
	}
	a.jobs = jobScheduler
	if err := a.jobs.Restore(a.ctx, "", a.SelfID()); err != nil {
		return err
	}

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
	if err := a.jobs.Start(a.ctx); err != nil {
		return err
	}
	if err := a.health.Set("core", observability.HealthReady, "accepting updates"); err != nil {
		return err
	}
	if a.options.Ready != nil {
		a.options.Ready()
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

func (a *App) Close(ctx context.Context) error {
	a.closeOnce.Do(func() {
		a.closing.Store(true)
		a.closeErr = errors.Join(a.closeErr, a.restoreAllProfiles(ctx))
		if a.jobs != nil {
			a.closeErr = errors.Join(a.closeErr, a.jobs.Stop(ctx))
		}
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
		if stopper, ok := a.controlBot.(interface{ Stop(context.Context) error }); ok {
			a.closeErr = errors.Join(a.closeErr, stopper.Stop(ctx))
		}
		// Keep action delivery available while accepted executor work drains;
		// close the Telegram connection only after every handler pool stopped.
		if err := a.transport.Close(ctx); err != nil {
			a.closeErr = errors.Join(a.closeErr, err)
		}
		a.closeErr = errors.Join(a.closeErr, a.modules.StopAll(ctx))
		if a.idempotency != nil {
			a.idempotency.Close()
		}
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
