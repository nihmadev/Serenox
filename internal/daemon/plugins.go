package daemon

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/nihmadev/Serenox/internal/app"
	"github.com/nihmadev/Serenox/internal/config"
	"github.com/nihmadev/Serenox/internal/executor"
	"github.com/nihmadev/Serenox/internal/media"
	"github.com/nihmadev/Serenox/internal/plugin"
	"github.com/nihmadev/Serenox/internal/pluginpackage"
	"github.com/nihmadev/Serenox/internal/pluginruntime"
	"github.com/nihmadev/Serenox/internal/pythonruntime"
	pluginv1 "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

func newPluginController(runtimeConfig config.Runtime, logger *slog.Logger, resolveApp func() *app.App, mediaGateway *media.Gateway) (*pluginruntime.Controller, error) {
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
	pythonRoots := []string{"."}
	if executable, executableErr := os.Executable(); executableErr == nil {
		pythonRoots = append(pythonRoots, filepath.Join(filepath.Dir(executable), ".."))
	}
	python := discoverPythonRuntimeContract(runtimeConfig.Plugins.PythonExecutable, runtimeConfig.Plugins.PythonManagedRuntime, pythonRoots)
	pythonArgs := []string{"-m", "serenox_sidecar", "--queue-capacity", strconv.Itoa(runtimeConfig.Plugins.PythonQueueCapacity), "--workers", strconv.Itoa(runtimeConfig.Plugins.PythonWorkers), "--action-batch", strconv.Itoa(runtimeConfig.Plugins.PythonActionBatch), "--action-bytes", strconv.Itoa(runtimeConfig.Plugins.PythonActionBytes), "--storage-entries", strconv.Itoa(runtimeConfig.Plugins.PythonStorageEntries), "--storage-value-bytes", strconv.Itoa(runtimeConfig.Plugins.PythonStorageValueBytes), "--callback-capacity", strconv.Itoa(runtimeConfig.Plugins.PythonCallbackCapacity), "--max-frame-bytes", strconv.Itoa(runtimeConfig.Plugins.PythonMaxFrameBytes), "--handler-timeout-ms", strconv.Itoa(int(runtimeConfig.Plugins.PythonHandlerTimeout.Duration() / time.Millisecond)), "--shutdown-timeout-ms", strconv.Itoa(int(runtimeConfig.Plugins.PythonShutdownTimeout.Duration() / time.Millisecond))}
	factory, err := pluginruntime.NewSupervisorFactory(pluginruntime.SupervisorFactoryConfig{
		Template: plugin.SupervisorConfig{
			HostVersion: Version, DispatchTimeout: runtimeConfig.Execution.HandlerTimeout.Duration(),
			OutputLimitBytes: 64 << 10,
			Env:              python.env,
		},
		EnablePython:      python.enabled,
		PythonExecutable:  python.executable,
		PythonSidecarArgs: pythonArgs,
		InterpreterPath:   python.info.Executable, InterpreterVersion: python.info.PythonVersion,
		SidecarVersion: python.info.SidecarVersion, SDKVersion: python.info.SDKVersion,
		ProtocolVersion: python.info.Protocol,
		PythonLimits: pluginpackage.ResourceLimits{
			QueueCapacity: runtimeConfig.Plugins.PythonQueueCapacity, Workers: runtimeConfig.Plugins.PythonWorkers,
			ActionBatch: runtimeConfig.Plugins.PythonActionBatch, ActionBytes: runtimeConfig.Plugins.PythonActionBytes,
			StorageEntries: runtimeConfig.Plugins.PythonStorageEntries, StorageValueBytes: runtimeConfig.Plugins.PythonStorageValueBytes,
			CallbackCapacity: runtimeConfig.Plugins.PythonCallbackCapacity, MaxFrameBytes: runtimeConfig.Plugins.PythonMaxFrameBytes,
			HandlerTimeoutMS: int(runtimeConfig.Plugins.PythonHandlerTimeout.Duration() / time.Millisecond), ShutdownTimeoutMS: int(runtimeConfig.Plugins.PythonShutdownTimeout.Duration() / time.Millisecond),
		},
		ValidateRuntime: func(spec pluginruntime.LaunchSpec) error {
			if spec.Category != pluginpackage.CategoryPythonSidecar {
				return nil
			}
			if python.err != nil {
				return python.err
			}
			protocolRange := spec.Protocol
			if protocolRange.Min == "" {
				protocolRange = pluginpackage.VersionRange{Min: pythonruntime.ProtocolVersion}
			}
			return pythonruntime.ValidateManifest(python.info, pluginpackage.Manifest{Category: pluginpackage.CategoryPythonSidecar, Protocol: protocolRange, SDK: spec.SDK, Sidecar: spec.Sidecar})
		},
		ActionHandler: func(ctx context.Context, actions []pluginv1.Action) error {
			application := resolveApp()
			if application == nil {
				return errors.New("application is not initialized")
			}
			return application.ApplyActions(ctx, actions)
		},
		ActionResultsHandler: func(ctx context.Context, actions []pluginv1.Action) (pluginv1.ActionBatchResponse, error) {
			application := resolveApp()
			if application == nil {
				return pluginv1.ActionBatchResponse{}, errors.New("application is not initialized")
			}
			return application.ApplyActionsWithResults(ctx, plugin.PluginIdentity(ctx), actions)
		},
		StorageHandler: func(ctx context.Context, pluginID string, accountID int64, request pluginv1.StorageRequest) (pluginv1.StorageResult, error) {
			application := resolveApp()
			if application == nil {
				return pluginv1.StorageResult{}, errors.New("application is not initialized")
			}
			if accountID == 0 {
				accountID = application.SelfID()
			}
			return application.HandleHostStorage(ctx, pluginID, accountID, request)
		},
		ClientHandler: func(ctx context.Context, pluginID string, accountID int64, request pluginv1.ClientRequest) (pluginv1.ClientResult, error) {
			application := resolveApp()
			if application == nil {
				return pluginv1.ClientResult{}, errors.New("application is not initialized")
			}
			if accountID == 0 {
				accountID = application.SelfID()
			}
			return application.HandleHostClient(ctx, pluginID, accountID, request)
		},
		ModerationHandler: func(ctx context.Context, pluginID string, accountID int64, permission string, request pluginv1.ModerationRequest) (pluginv1.ModerationResult, error) {
			application := resolveApp()
			if application == nil {
				return pluginv1.ModerationResult{}, errors.New("application is not initialized")
			}
			if accountID == 0 {
				accountID = application.SelfID()
			}
			return application.HandleHostModeration(ctx, pluginID, accountID, permission, request)
		},
		MediaHandler: func(ctx context.Context, pluginID string, accountID int64, request pluginv1.MediaRequest) (pluginv1.MediaResult, error) {
			if mediaGateway == nil {
				return pluginv1.MediaResult{}, errors.New("media gateway is not initialized")
			}
			if accountID == 0 {
				application := resolveApp()
				if application == nil {
					return pluginv1.MediaResult{}, errors.New("application is not initialized")
				}
				accountID = application.SelfID()
			}
			return mediaGateway.Media(ctx, pluginID, accountID, request)
		},
		HTTPHandler: func(ctx context.Context, pluginID string, accountID int64, policy pluginpackage.NetworkPolicy, request pluginv1.HTTPRequest) (pluginv1.HTTPResponse, error) {
			application := resolveApp()
			if application == nil {
				return pluginv1.HTTPResponse{}, errors.New("application is not initialized")
			}
			if accountID == 0 {
				accountID = application.SelfID()
			}
			return application.HandleHostHTTP(ctx, pluginID, accountID, policy, request)
		},
		JobsHandler: func(ctx context.Context, pluginID string, accountID int64, request pluginv1.JobRPCRequest) (any, error) {
			application := resolveApp()
			if application == nil {
				return nil, errors.New("application is not initialized")
			}
			return application.HandleHostJobs(ctx, pluginID, accountID, request)
		},
		EventsHandler: func(ctx context.Context, pluginID string, accountID int64, request pluginv1.EventPublishRequest) (pluginv1.EventPublishResult, error) {
			application := resolveApp()
			if application == nil {
				return pluginv1.EventPublishResult{}, errors.New("application is not initialized")
			}
			if accountID == 0 {
				accountID = application.SelfID()
			}
			return application.HandleHostEvents(ctx, pluginID, accountID, request)
		},
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
				if err := application.RegisterExternal(context.Background(), metadata, app.ExternalDispatch(dispatch)); err != nil {
					return err
				}
				logger.Info("plugin ready", "plugin", metadata.ID, "version", metadata.Version)
				return nil
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
