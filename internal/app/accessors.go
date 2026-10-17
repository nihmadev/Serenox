package app

import (
	"github.com/nihmadev/Serenox/internal/dispatch"
	"github.com/nihmadev/Serenox/internal/executor"
	"github.com/nihmadev/Serenox/internal/observability"
)

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

func (a *App) SelfID() int64 { return a.options.SelfID }

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

func (a *App) Metrics() []observability.Point { return a.metrics.Snapshot() }

func (a *App) Health() []observability.HealthStatus { return a.health.Snapshot() }
