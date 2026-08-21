package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"

	"github.com/nihmadev/Serenox/internal/app"
	"github.com/nihmadev/Serenox/internal/bootstrap"
	"github.com/nihmadev/Serenox/internal/config"
	"github.com/nihmadev/Serenox/internal/executor"
	"github.com/nihmadev/Serenox/internal/observability"
	"github.com/nihmadev/Serenox/internal/plugin"
	"github.com/nihmadev/Serenox/internal/pluginpackage"
	"github.com/nihmadev/Serenox/internal/pluginruntime"
	"github.com/nihmadev/Serenox/internal/telegram"
	pluginv1 "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

const version = "0.1.0-dev"

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(arguments []string, stdout, stderr io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runContext(ctx, stop, arguments, stdout, stderr, defaultTransportDependencies())
}

type transportDependencies struct {
	loadRecorded func(string, telegram.RecordedOptions) (*telegram.RecordedTransport, error)
	authorize    func(context.Context, telegram.LiveConfig, telegram.Prompt) (telegram.Identity, error)
	newLive      func(telegram.LiveConfig) (telegram.Transport, error)
}

func defaultTransportDependencies() transportDependencies {
	return transportDependencies{
		loadRecorded: telegram.LoadRecorded,
		authorize:    telegram.Authorize,
		newLive: func(config telegram.LiveConfig) (telegram.Transport, error) {
			return telegram.NewLive(config)
		},
	}
}

func runContext(ctx context.Context, stop context.CancelFunc, arguments []string, stdout, stderr io.Writer, dependencies transportDependencies) int {
	flags := flag.NewFlagSet("serenoxd", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "path to Serenox JSON configuration")
	recordedPath := flags.String("recorded", "", "replay recorded NDJSON updates instead of a live adapter")
	selfID := flags.Int64("self-id", 0, "owner ID (required for -recorded; optional live identity assertion)")
	checkConfig := flags.Bool("check-config", false, "validate configuration and exit")
	showVersion := flags.Bool("version", false, "print version and exit")
	if err := flags.Parse(arguments); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "serenoxd accepts no positional arguments")
		return 2
	}
	if *showVersion {
		fmt.Fprintln(stdout, version)
		return 0
	}

	runtimeConfig, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(stderr, "configuration error: %v\n", err)
		return 2
	}
	// A phone override is an interactive-login input, not daemon state. Loading
	// it still validates the effective environment, then this copy is cleared
	// before it can be retained by the long-running application.
	runtimeConfig.Telegram.Phone = ""
	if *checkConfig {
		fmt.Fprintln(stdout, "configuration OK")
		return 0
	}
	transport, effectiveSelfID, err := selectTelegramTransport(ctx, runtimeConfig, *recordedPath, *selfID, dependencies)
	if err != nil {
		fmt.Fprintf(stderr, "Telegram transport: %v\n", err)
		return 1
	}
	level := slog.LevelInfo
	switch runtimeConfig.LogLevel {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	logger := observability.NewJSONLogger(stderr, level)
	var restart atomic.Bool
	var application *app.App
	pluginController, err := newPluginController(runtimeConfig, logger, func() *app.App { return application })
	if err != nil {
		_ = transport.Close(context.Background())
		fmt.Fprintf(stderr, "initialize plugin runtime: %v\n", err)
		return 1
	}
	application, err = app.New(app.Options{
		Config: runtimeConfig, SelfID: effectiveSelfID, Transport: transport, Logger: logger,
		PluginController: pluginController,
		RequestRestart: func(context.Context, bool) error {
			restart.Store(true)
			stop()
			return nil
		},
	})
	if err != nil {
		_ = pluginController.Close(context.Background())
		_ = transport.Close(context.Background())
		fmt.Fprintf(stderr, "initialize: %v\n", err)
		return 1
	}
	if err := application.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("application stopped", "error", err)
		return 1
	}
	if restart.Load() {
		// EX_TEMPFAIL lets a service manager distinguish a controlled restart
		// request from a clean operator stop.
		return 75
	}
	return 0
}

func selectTelegramTransport(
	ctx context.Context,
	runtimeConfig config.Runtime,
	recordedPath string,
	requestedSelfID int64,
	dependencies transportDependencies,
) (telegram.Transport, int64, error) {
	if recordedPath != "" {
		if requestedSelfID == 0 {
			return nil, 0, errors.New("-self-id must be non-zero when -recorded is used")
		}
		if dependencies.loadRecorded == nil {
			return nil, 0, errors.New("recorded Telegram transport dependency is missing")
		}
		transport, err := dependencies.loadRecorded(recordedPath, telegram.RecordedOptions{})
		if err != nil {
			return nil, 0, fmt.Errorf("recorded updates: %w", err)
		}
		return transport, requestedSelfID, nil
	}
	if !runtimeConfig.Telegram.Enabled {
		return nil, 0, errors.New("live Telegram is disabled; set telegram.enabled=true, run serenox login, or use -recorded for fixtures")
	}
	if dependencies.authorize == nil || dependencies.newLive == nil {
		return nil, 0, errors.New("native Telegram transport dependency is missing")
	}
	liveConfig, err := bootstrap.TelegramLiveConfig(runtimeConfig)
	if err != nil {
		return nil, 0, err
	}
	identity, err := dependencies.authorize(ctx, liveConfig, nil)
	if err != nil {
		if errors.Is(err, telegram.ErrAuthPromptRequired) || errors.Is(err, telegram.ErrNotAuthorized) {
			return nil, 0, errors.New("Telegram session is not authorized; run serenox login before serenox run")
		}
		return nil, 0, fmt.Errorf("verify saved session: %w; run serenox login to repair or replace it", err)
	}
	if identity.UserID == 0 {
		return nil, 0, errors.New("saved Telegram session returned an empty account identity")
	}
	if requestedSelfID != 0 && requestedSelfID != identity.UserID {
		return nil, 0, fmt.Errorf("-self-id does not match the authorized Telegram account (expected %d)", identity.UserID)
	}
	transport, err := dependencies.newLive(liveConfig)
	if err != nil {
		return nil, 0, fmt.Errorf("create native live transport: %w", err)
	}
	return transport, identity.UserID, nil
}

func newPluginController(runtimeConfig config.Runtime, logger *slog.Logger, resolveApp func() *app.App) (*pluginruntime.Controller, error) {
	policy := pluginpackage.TrustPolicy{Mode: pluginpackage.TrustMode(runtimeConfig.Plugins.TrustMode)}
	if trustPath := runtimeConfig.PluginTrustPath(); trustPath != "" {
		loaded, err := pluginpackage.LoadTrustPolicy(trustPath)
		if err != nil {
			return nil, err
		}
		loaded.Mode = policy.Mode
		policy = loaded
	}
	verify := pluginpackage.DefaultVerifyOptions()
	verify.Trust = policy
	packageManager, err := pluginpackage.NewManager(pluginpackage.ManagerConfig{
		Root: runtimeConfig.DataDir, Verify: verify,
		RetainVersions: runtimeConfig.Plugins.RetainVersions,
	})
	if err != nil {
		return nil, err
	}
	factory, err := pluginruntime.NewSupervisorFactory(pluginruntime.SupervisorFactoryConfig{
		Template: plugin.SupervisorConfig{
			HostVersion: version, DispatchTimeout: runtimeConfig.Execution.HandlerTimeout.Duration(),
			OutputLimitBytes: 64 << 10,
		},
		EnablePython:      runtimeConfig.Plugins.PythonExecutable != "",
		PythonExecutable:  runtimeConfig.Plugins.PythonExecutable,
		PythonSidecarArgs: []string{"-m", "serenox_sidecar"},
	})
	if err != nil {
		return nil, err
	}
	controller, err := pluginruntime.New(pluginruntime.Config{
		Manager: packageManager, Factory: factory, MaxPlugins: runtimeConfig.Plugins.MaxLoaded,
		Hooks: pluginruntime.Hooks{
			OnReady: func(metadata pluginv1.PluginMetadata, dispatch pluginruntime.DispatchFunc) error {
				application := resolveApp()
				if application == nil {
					return errors.New("application is not initialized")
				}
				return application.RegisterExternal(context.Background(), metadata, app.ExternalDispatch(dispatch))
			},
			OnStopped: func(pluginID string) {
				application := resolveApp()
				if application == nil {
					return
				}
				ctx, cancel := context.WithTimeout(context.Background(), runtimeConfig.ShutdownTimeout.Duration())
				defer cancel()
				if err := application.UnregisterExternal(ctx, pluginID); err != nil && !errors.Is(err, executor.ErrPluginNotFound) {
					logger.Error("unregister stopped plugin", "plugin", pluginID, "error", err)
				}
			},
		},
	})
	if err != nil {
		return nil, err
	}
	return controller, nil
}
