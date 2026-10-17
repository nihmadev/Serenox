// Package pluginruntime connects verified .srnx package installation to an
// isolated plugin process. Package verification and activation are delegated
// to pluginpackage.Manager; this package never executes an archive directly.
package pluginruntime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/nihmadev/Serenox/internal/builtin"
	"github.com/nihmadev/Serenox/internal/pluginpackage"
	pluginv1 "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

const (
	DefaultMaxPlugins      = 32
	DefaultStopTimeout     = 5 * time.Second
	DefaultRollbackTimeout = 10 * time.Second
)

var (
	ErrClosed                  = errors.New("plugin runtime is closed")
	ErrCapacity                = errors.New("plugin runtime capacity reached")
	ErrInvalidPackageExtension = errors.New("plugin package must use the .srnx extension")
	ErrNotLoaded               = errors.New("plugin is not loaded")
	ErrInconsistentInstall     = errors.New("installed plugin state is inconsistent")
	ErrPermissionDenied        = errors.New("plugin action permission denied")
)

// PackageManager is the subset of pluginpackage.Manager required for a
// transactional runtime update. A successful Install must mean the archive
// was fully verified before it was extracted and made active.
type PackageManager interface {
	RetainVersions() int
	Install(context.Context, string) (pluginpackage.Receipt, error)
	List(context.Context) ([]pluginpackage.InstalledPlugin, error)
	Rollback(context.Context, string, string) (pluginpackage.Receipt, error)
	Uninstall(context.Context, string, string) error
}

// LaunchSpec contains only paths and metadata from an installed, verified
// package. Entrypoint is never a path inside the source archive.
type LaunchSpec struct {
	ID                 string
	Name               string
	Version            string
	Category           pluginpackage.Category
	VersionRoot        string
	Entrypoint         string
	Module             string
	Class              string
	ManifestDigest     string
	Capabilities       []string
	Permissions        []string
	EventPublications  []pluginv1.EventPublication
	EventSubscriptions []pluginv1.EventSubscription
	Network            pluginpackage.NetworkPolicy
	Signed             bool
	Trusted            bool
	SDK                pluginpackage.VersionRange
	Protocol           pluginpackage.VersionRange
	Sidecar            pluginpackage.VersionRange
	Resources          pluginpackage.ResourceLimits
	Dependencies       pluginpackage.DependencyMetadata
}

type RuntimeStatus struct {
	InterpreterPath    string
	InterpreterVersion string
	SidecarVersion     string
	SDKVersion         string
	ProtocolVersion    string
	ProcessState       string
	RestartCount       uint64
	CircuitState       string
	QueueDepth         int
	WorkerCount        int
	RSSBytes           uint64
	LastError          string
}

// Process is an owned plugin process. Wait must return when its context is
// cancelled. Stop must synchronously finish its bounded shutdown attempt.
type Process interface {
	Metadata() pluginv1.PluginMetadata
	Dispatch(context.Context, pluginv1.DispatchRequest) (pluginv1.DispatchResponse, error)
	Wait(context.Context) error
	Stop(context.Context) error
}

// Factory starts either a native executable host or the Python sidecar for
// the selected installed package. The context owns the complete process
// lifetime.
type Factory interface {
	Start(context.Context, LaunchSpec) (Process, error)
}

type DispatchFunc func(context.Context, pluginv1.DispatchRequest) (pluginv1.DispatchResponse, error)

// Hooks connect a ready process to the core dispatcher without importing the
// application package. OnReady is part of startup, so an error triggers the
// same process and package rollback as any other startup failure. Hooks must
// return promptly and must not synchronously call Controller lifecycle methods.
type Hooks struct {
	OnReady   func(pluginv1.PluginMetadata, DispatchFunc) error
	OnStopped func(string)
}

type Config struct {
	Manager         PackageManager
	Factory         Factory
	MaxPlugins      int
	StopTimeout     time.Duration
	RollbackTimeout time.Duration
	Hooks           Hooks
}

type runtimeState string

const (
	stateStarting runtimeState = "starting"
	stateRunning  runtimeState = "running"
	stateStopping runtimeState = "stopping"
	stateStopped  runtimeState = "stopped"
	stateFailed   runtimeState = "failed"
)

type entry struct {
	spec        LaunchSpec
	state       runtimeState
	process     Process
	cancel      context.CancelFunc
	monitorDone chan struct{}
	generation  uint64
	registered  bool
	lastError   string
}

// Controller serializes package mutations while allowing lock-free process
// work with a small state lock. Its state and monitor goroutines are bounded by
// MaxPlugins.
type Controller struct {
	manager         PackageManager
	factory         Factory
	maxPlugins      int
	stopTimeout     time.Duration
	rollbackTimeout time.Duration
	hooks           Hooks

	ctx    context.Context
	cancel context.CancelFunc
	opMu   sync.Mutex
	mu     sync.RWMutex
	closed bool
	nextID uint64
	items  map[string]*entry
}

var (
	_ PackageManager           = (*pluginpackage.Manager)(nil)
	_ builtin.PluginController = (*Controller)(nil)
)

func New(config Config) (*Controller, error) {
	if config.Manager == nil {
		return nil, errors.New("plugin package manager is required")
	}
	if retained := config.Manager.RetainVersions(); retained < 2 {
		return nil, fmt.Errorf("plugin package manager must retain at least 2 versions for transactional rollback; configured %d", retained)
	}
	if config.Factory == nil {
		return nil, errors.New("plugin process factory is required")
	}
	if config.MaxPlugins == 0 {
		config.MaxPlugins = DefaultMaxPlugins
	}
	if config.MaxPlugins < 1 || config.MaxPlugins > 1024 {
		return nil, errors.New("maximum plugins must be between 1 and 1024")
	}
	if config.StopTimeout == 0 {
		config.StopTimeout = DefaultStopTimeout
	}
	if config.RollbackTimeout == 0 {
		config.RollbackTimeout = DefaultRollbackTimeout
	}
	if config.StopTimeout < time.Millisecond || config.StopTimeout > time.Hour {
		return nil, errors.New("plugin stop timeout must be between 1ms and 1h")
	}
	if config.RollbackTimeout < time.Millisecond || config.RollbackTimeout > time.Hour {
		return nil, errors.New("plugin rollback timeout must be between 1ms and 1h")
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Controller{
		manager: config.Manager, factory: config.Factory, maxPlugins: config.MaxPlugins,
		stopTimeout: config.StopTimeout, rollbackTimeout: config.RollbackTimeout,
		hooks: config.Hooks, ctx: ctx, cancel: cancel, items: make(map[string]*entry, config.MaxPlugins),
	}, nil
}
