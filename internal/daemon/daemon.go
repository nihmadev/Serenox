// Package daemon owns the long-running Serenox runtime.
//
// It is shared by the canonical user CLI and the optional standalone
// serenoxd entrypoint. Keeping the runtime here means a normal installation
// only needs the serenox binary, while service managers can still invoke
// serenoxd directly when they want a stable service name.
package daemon

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/nihmadev/Serenox/internal/app"
	"github.com/nihmadev/Serenox/internal/bootstrap"
	"github.com/nihmadev/Serenox/internal/config"
	"github.com/nihmadev/Serenox/internal/controlbot"
	"github.com/nihmadev/Serenox/internal/manifest"
	"github.com/nihmadev/Serenox/internal/media"
	"github.com/nihmadev/Serenox/internal/observability"
	"github.com/nihmadev/Serenox/internal/telegram"
)

const Version = "0.1.0-dev"

// TransportDependencies allows tests and alternate binaries to replace the
// Telegram adapters without changing the runtime composition.
type TransportDependencies struct {
	LoadRecorded func(string, telegram.RecordedOptions) (*telegram.RecordedTransport, error)
	Authorize    func(context.Context, telegram.LiveConfig, telegram.Prompt) (telegram.Identity, error)
	NewLive      func(telegram.LiveConfig) (telegram.Transport, error)
}

func DefaultTransportDependencies() TransportDependencies {
	return TransportDependencies{
		LoadRecorded: telegram.LoadRecorded,
		Authorize:    telegram.Authorize,
		NewLive: func(config telegram.LiveConfig) (telegram.Transport, error) {
			return telegram.NewLive(config)
		},
	}
}

// RunContext validates arguments, builds the runtime, and runs it until the
// parent context is cancelled. stop is also used by the controlled restart
// command exposed by the built-in updater module.
func RunContext(ctx context.Context, stop context.CancelFunc, arguments []string, stdout, stderr io.Writer, dependencies TransportDependencies) int {
	flags := flag.NewFlagSet("serenox", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "path to Serenox JSON configuration")
	manifestPath := flags.String("manifest", "", "path to a serenox.yaml application manifest")
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
		fmt.Fprintln(stderr, "serenox accepts no positional arguments")
		return 2
	}
	if *showVersion {
		fmt.Fprintln(stdout, Version)
		return 0
	}

	var runtimeConfig config.Runtime
	if *manifestPath == "" && *configPath == "" {
		if discovered, ok, discoverErr := manifest.Discover("."); discoverErr != nil {
			fmt.Fprintf(stderr, "configuration discovery error: %v\n", discoverErr)
			return 2
		} else if ok {
			*manifestPath = discovered
		}
	}
	if *manifestPath != "" {
		effective, loadErr := manifest.LoadEffective(manifest.LoadOptions{Path: *manifestPath})
		if loadErr != nil {
			fmt.Fprintf(stderr, "manifest error: %v\n", loadErr)
			return 2
		}
		if backendErr := effective.BackendSupported(); backendErr != nil {
			fmt.Fprintf(stderr, "manifest backend error: %v\n", backendErr)
			return 2
		}
		runtimeConfig = effective.Runtime
	} else {
		loaded, loadErr := config.LoadWithOptions(config.LoadOptions{Path: *configPath, LoadLocalTelegramCredentials: true})
		if loadErr != nil {
			fmt.Fprintf(stderr, "configuration error: %v\n", loadErr)
			return 2
		}
		runtimeConfig = loaded
	}
	// A phone override is an interactive-login input, not daemon state. Loading
	// it still validates the effective environment, then this copy is cleared
	// before it can be retained by the long-running application.
	runtimeConfig.Telegram.Phone = ""
	if *checkConfig {
		fmt.Fprintln(stdout, "configuration OK")
		return 0
	}
	transport, effectiveSelfID, err := SelectTelegramTransport(ctx, runtimeConfig, *recordedPath, *selfID, dependencies)
	if err != nil {
		fmt.Fprintf(stderr, "Telegram transport: %v\n", err)
		return 1
	}
	var mediaSource media.TelegramSource
	if source, ok := transport.(media.TelegramSource); ok {
		mediaSource = source
	}
	mediaRoot, err := filepath.Abs(runtimeConfig.DataDir)
	if err != nil {
		_ = transport.Close(context.Background())
		fmt.Fprintf(stderr, "initialize media gateway path: %v\n", err)
		return 1
	}
	mediaGateway, err := media.NewGateway(mediaRoot, mediaSource, media.Limits{})
	if err != nil {
		_ = transport.Close(context.Background())
		fmt.Fprintf(stderr, "initialize media gateway: %v\n", err)
		return 1
	}
	defer func() { _ = mediaGateway.Close() }()
	if configurable, ok := transport.(interface {
		SetMediaDownloader(telegram.MediaDownloader)
	}); ok {
		configurable.SetMediaDownloader(mediaGateway)
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
	logger := observability.NewHumanLogger(stderr, level)
	if strings.EqualFold(strings.TrimSpace(os.Getenv("SERENOX_LOG_FORMAT")), "json") {
		logger = observability.NewJSONLogger(stderr, level)
	}
	var restart atomic.Bool
	ready := make(chan struct{})
	var readyOnce sync.Once
	var application *app.App
	pluginController, err := newPluginController(runtimeConfig, logger, func() *app.App { return application }, mediaGateway)
	if err != nil {
		_ = transport.Close(context.Background())
		fmt.Fprintf(stderr, "initialize plugin runtime: %v\n", err)
		return 1
	}
	controlStore, storeErr := controlbot.NewSecretStore(runtimeConfig.DataDir)
	if storeErr != nil {
		_ = pluginController.Close(context.Background())
		_ = transport.Close(context.Background())
		fmt.Fprintf(stderr, "initialize control-bot storage: %v\n", storeErr)
		return 1
	}
	controlManager, managerErr := controlbot.NewManager(controlStore, controlbot.ClientOptions{})
	if managerErr != nil {
		_ = pluginController.Close(context.Background())
		_ = transport.Close(context.Background())
		fmt.Fprintf(stderr, "initialize control bot: %v\n", managerErr)
		return 1
	}
	application, err = app.New(app.Options{
		Config: runtimeConfig, SelfID: effectiveSelfID, Transport: transport, Logger: logger,
		PluginController: pluginController,
		ControlBot:       controlManager,
		Ready: func() {
			if controlManager.Status().Attached {
				if startErr := controlManager.Start(ctx, application); startErr != nil {
					logger.Error("control bot is unavailable", "error", startErr)
				}
			}
			readyOnce.Do(func() { close(ready) })
		},
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
	controlServer, err := NewControlServer(runtimeConfig.DataDir, pluginController, ready)
	if err != nil {
		_ = application.Close(context.Background())
		fmt.Fprintf(stderr, "initialize daemon control plane: %v\n", err)
		return 1
	}
	controlServer.SetLogger(logger)
	controlServer.SetControlBotReload(func(context.Context) error {
		// Stop also clears a terminated poller handle. Calling it regardless of
		// Running makes attachment/reload recover from a previous local error.
		if err := controlManager.Stop(context.Background()); err != nil {
			return err
		}
		return controlManager.Start(ctx, application)
	})
	controlServer.SetControlBotStatus(func() ControlBotStatus {
		status := controlManager.Status()
		return ControlBotStatus{
			Attached: status.Attached, Running: status.Running,
			BotID: status.Bot.ID, Username: status.Bot.Username,
			SavedOffset: status.Offset, LastError: status.LastError,
			RestartCount: status.RestartCount,
		}
	})
	controlServer.SetEventOperations(application.EventStatus, application.EventRecords, application.EventRecord, application.ReplayEvent, application.DiscardEvent)
	if err := controlServer.Start(ctx); err != nil {
		_ = application.Close(context.Background())
		fmt.Fprintf(stderr, "start daemon control plane: %v\n", err)
		return 1
	}
	defer func() { _ = controlServer.Close(context.Background()) }()
	transportName := "live"
	if *recordedPath != "" {
		transportName = "recorded"
	}
	logger.Info("Serenox started", "transport", transportName, "self_id", effectiveSelfID)
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

// SelectTelegramTransport chooses the deterministic recorded adapter or the
// authenticated live adapter for a validated runtime configuration.
func SelectTelegramTransport(
	ctx context.Context,
	runtimeConfig config.Runtime,
	recordedPath string,
	requestedSelfID int64,
	dependencies TransportDependencies,
) (telegram.Transport, int64, error) {
	if recordedPath != "" {
		if requestedSelfID == 0 {
			return nil, 0, errors.New("-self-id must be non-zero when -recorded is used")
		}
		if dependencies.LoadRecorded == nil {
			return nil, 0, errors.New("recorded Telegram transport dependency is missing")
		}
		transport, err := dependencies.LoadRecorded(recordedPath, telegram.RecordedOptions{})
		if err != nil {
			return nil, 0, fmt.Errorf("recorded updates: %w", err)
		}
		return transport, requestedSelfID, nil
	}
	if dependencies.Authorize == nil || dependencies.NewLive == nil {
		return nil, 0, errors.New("native Telegram transport dependency is missing")
	}
	liveConfig, err := bootstrap.TelegramLiveConfig(runtimeConfig)
	if err != nil {
		return nil, 0, err
	}
	identity, err := dependencies.Authorize(ctx, liveConfig, nil)
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
	transport, err := dependencies.NewLive(liveConfig)
	if err != nil {
		return nil, 0, fmt.Errorf("create native live transport: %w", err)
	}
	return transport, identity.UserID, nil
}
