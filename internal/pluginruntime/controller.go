// Package pluginruntime connects verified .srnx package installation to an
// isolated plugin process. Package verification and activation are delegated
// to pluginpackage.Manager; this package never executes an archive directly.
package pluginruntime

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
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
// transactional runtime update. A successful Install must mean the archive was
// fully verified before it was extracted and made active.
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
	ID             string
	Name           string
	Version        string
	Category       pluginpackage.Category
	VersionRoot    string
	Entrypoint     string
	ManifestDigest string
	Capabilities   []string
	Permissions    []string
	Signed         bool
	Trusted        bool
}

// Process is an owned plugin process. Wait must return when its context is
// cancelled. Stop must synchronously finish its bounded shutdown attempt.
type Process interface {
	Metadata() pluginv1.PluginMetadata
	Dispatch(context.Context, pluginv1.DispatchRequest) (pluginv1.DispatchResponse, error)
	Wait(context.Context) error
	Stop(context.Context) error
}

// Factory starts either a native executable host or the Python sidecar for the
// selected installed package. The context owns the complete process lifetime.
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

// Load verifies and installs a .srnx through PackageManager, then replaces the
// running process. Failed updates restore both the previous active pointer and,
// when it was running, the previous process.
func (c *Controller) Load(parent context.Context, packagePath string) error {
	if !strings.EqualFold(filepath.Ext(strings.TrimSpace(packagePath)), ".srnx") {
		return ErrInvalidPackageExtension
	}
	packagePath = strings.TrimSpace(packagePath)

	opCtx, cancel := c.operationContext(parent)
	defer cancel()
	c.opMu.Lock()
	defer c.opMu.Unlock()
	if err := c.checkOpen(); err != nil {
		return err
	}

	before, err := c.manager.List(opCtx)
	if err != nil {
		return fmt.Errorf("list packages before install: %w", err)
	}
	receipt, err := c.manager.Install(opCtx, packagePath)
	if err != nil {
		return fmt.Errorf("verify and install %s: %w", filepath.Base(packagePath), err)
	}
	after, err := c.manager.List(opCtx)
	if err != nil {
		rollbackErr := c.restorePackage(before, receipt)
		return errors.Join(fmt.Errorf("list packages after install: %w", err), rollbackErr)
	}
	newSpec, err := resolveLaunchSpec(after, receipt)
	if err != nil {
		rollbackErr := c.restorePackage(before, receipt)
		return errors.Join(err, rollbackErr)
	}

	previousSpec, hadPreviousActive := resolveActiveSpec(before, receipt.ID)
	old := c.get(receipt.ID)
	if old == nil && c.size() >= c.maxPlugins {
		rollbackErr := c.restorePackage(before, receipt)
		return errors.Join(fmt.Errorf("%w: maximum is %d", ErrCapacity, c.maxPlugins), rollbackErr)
	}
	if old != nil && hadPreviousActive &&
		(old.spec.Version != previousSpec.Version || old.spec.ManifestDigest != previousSpec.ManifestDigest) {
		rollbackErr := c.restorePackage(before, receipt)
		return errors.Join(fmt.Errorf("%w: runtime %s@%s differs from active package %s@%s",
			ErrInconsistentInstall, old.spec.ID, old.spec.Version, previousSpec.ID, previousSpec.Version), rollbackErr)
	}
	if old != nil && c.isRunning(old) && samePackage(old.spec, newSpec) {
		return nil
	}
	if err := opCtx.Err(); err != nil {
		rollbackErr := c.restorePackage(before, receipt)
		return errors.Join(err, rollbackErr)
	}

	oldWasRunning := old != nil && c.isRunning(old)
	oldHadProcess := old != nil && c.hasProcess(old)
	if oldHadProcess {
		c.setStateIfCurrent(receipt.ID, old, stateStopping)
		if stopErr := c.stopEntry(opCtx, old); stopErr != nil {
			rollbackErr := c.restorePackage(before, receipt)
			c.setStateIfCurrent(receipt.ID, old, stateFailed)
			return errors.Join(fmt.Errorf("stop previous plugin process: %w", stopErr), rollbackErr)
		}
	}

	started, startErr := c.startAndStore(opCtx, newSpec)
	if startErr == nil {
		_ = started
		return nil
	}
	rollbackErr := c.restorePackage(before, receipt)
	if rollbackErr != nil {
		c.markFailed(receipt.ID, newSpec)
		return errors.Join(fmt.Errorf("start %s@%s: %w", receipt.ID, receipt.Version, startErr), rollbackErr)
	}

	if oldWasRunning {
		restartCtx, restartCancel := context.WithTimeout(context.Background(), c.rollbackTimeout)
		defer restartCancel()
		if _, restartErr := c.startAndStore(restartCtx, old.spec); restartErr != nil {
			c.markFailed(receipt.ID, old.spec)
			return errors.Join(
				fmt.Errorf("start %s@%s: %w", receipt.ID, receipt.Version, startErr),
				fmt.Errorf("restart previous %s@%s: %w", old.spec.ID, old.spec.Version, restartErr),
			)
		}
	} else if old != nil {
		c.put(receipt.ID, old)
	} else {
		c.delete(receipt.ID)
	}
	return fmt.Errorf("start %s@%s: %w", receipt.ID, receipt.Version, startErr)
}

// Restore starts every deterministically ordered active installed version. It
// never changes package activation: one failed process is recorded as failed
// while the remaining plugins continue. Calling Restore again leaves matching
// running processes untouched and retries only non-running entries.
func (c *Controller) Restore(parent context.Context) error {
	opCtx, cancel := c.operationContext(parent)
	defer cancel()
	c.opMu.Lock()
	defer c.opMu.Unlock()
	if err := c.checkOpen(); err != nil {
		return err
	}
	installed, err := c.manager.List(opCtx)
	if err != nil {
		return fmt.Errorf("list installed plugins for restore: %w", err)
	}
	sort.Slice(installed, func(i, j int) bool { return installed[i].ID < installed[j].ID })
	var joined error
	for _, plugin := range installed {
		if err := opCtx.Err(); err != nil {
			return errors.Join(joined, err)
		}
		if plugin.ActiveVersion == "" {
			continue
		}
		receipt, found := activeReceipt(plugin)
		if !found {
			spec := LaunchSpec{ID: plugin.ID, Name: plugin.Name, Version: plugin.ActiveVersion, Category: plugin.Category}
			if c.get(plugin.ID) == nil && c.size() >= c.maxPlugins {
				joined = errors.Join(joined, fmt.Errorf("restore %s: %w", plugin.ID, ErrCapacity))
				continue
			}
			c.markFailed(plugin.ID, spec)
			joined = errors.Join(joined, fmt.Errorf("restore %s: %w: active receipt is missing", plugin.ID, ErrInconsistentInstall))
			continue
		}
		spec, resolveErr := resolveLaunchSpec(installed, receipt)
		if resolveErr != nil {
			if c.get(plugin.ID) == nil && c.size() >= c.maxPlugins {
				joined = errors.Join(joined, fmt.Errorf("restore %s: %w", plugin.ID, ErrCapacity))
				continue
			}
			c.markFailed(plugin.ID, LaunchSpec{
				ID: plugin.ID, Name: plugin.Name, Version: plugin.ActiveVersion, Category: plugin.Category,
				ManifestDigest: receipt.ManifestDigest,
			})
			joined = errors.Join(joined, fmt.Errorf("restore %s: %w", plugin.ID, resolveErr))
			continue
		}
		old := c.get(plugin.ID)
		if old != nil && c.isRunning(old) && samePackage(old.spec, spec) {
			continue
		}
		if old == nil && c.size() >= c.maxPlugins {
			joined = errors.Join(joined, fmt.Errorf("restore %s: %w", plugin.ID, ErrCapacity))
			continue
		}
		if old != nil && c.hasProcess(old) {
			c.setStateIfCurrent(plugin.ID, old, stateStopping)
			if stopErr := c.stopEntry(opCtx, old); stopErr != nil {
				c.setStateIfCurrent(plugin.ID, old, stateFailed)
				joined = errors.Join(joined, fmt.Errorf("restore %s: stop previous process: %w", plugin.ID, stopErr))
				continue
			}
		}
		if _, startErr := c.startAndStore(opCtx, spec); startErr != nil {
			c.markFailed(plugin.ID, spec)
			joined = errors.Join(joined, fmt.Errorf("restore %s@%s: %w", plugin.ID, spec.Version, startErr))
		}
	}
	return joined
}

// Unload stops a process and forgets only its runtime state. Installed package
// versions and their active pointer remain untouched.
func (c *Controller) Unload(parent context.Context, pluginID string) error {
	opCtx, cancel := c.operationContext(parent)
	defer cancel()
	c.opMu.Lock()
	defer c.opMu.Unlock()
	if err := c.checkOpen(); err != nil {
		return err
	}
	pluginID = strings.TrimSpace(pluginID)
	current := c.get(pluginID)
	if current == nil {
		return fmt.Errorf("%w: %s", ErrNotLoaded, pluginID)
	}
	if !c.hasProcess(current) {
		c.delete(pluginID)
		return nil
	}
	c.setStateIfCurrent(pluginID, current, stateStopping)
	if err := c.stopEntry(opCtx, current); err != nil {
		c.setStateIfCurrent(pluginID, current, stateFailed)
		return fmt.Errorf("stop plugin %s: %w", pluginID, err)
	}
	c.deleteIfCurrent(pluginID, current)
	return nil
}

// List returns a stable snapshot ordered by plugin ID.
func (c *Controller) List() []builtin.PluginStatus {
	c.mu.RLock()
	result := make([]builtin.PluginStatus, 0, len(c.items))
	for _, current := range c.items {
		result = append(result, builtin.PluginStatus{
			ID: current.spec.ID, Version: current.spec.Version,
			State: string(current.state), External: true,
		})
	}
	c.mu.RUnlock()
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

// Close permanently prevents new work, cancels every owned process lifetime,
// stops each process sequentially, and waits for every bounded monitor.
func (c *Controller) Close(parent context.Context) error {
	if parent == nil {
		parent = context.Background()
	}
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	c.cancel()

	c.opMu.Lock()
	defer c.opMu.Unlock()
	c.mu.RLock()
	ids := make([]string, 0, len(c.items))
	for id := range c.items {
		ids = append(ids, id)
	}
	c.mu.RUnlock()
	sort.Strings(ids)

	var joined error
	for _, id := range ids {
		current := c.get(id)
		if current == nil {
			continue
		}
		if c.hasProcess(current) {
			c.setStateIfCurrent(id, current, stateStopping)
			if err := c.stopEntry(parent, current); err != nil {
				c.setStateIfCurrent(id, current, stateFailed)
				joined = errors.Join(joined, fmt.Errorf("stop plugin %s: %w", id, err))
				continue
			}
		}
		c.deleteIfCurrent(id, current)
	}
	return joined
}

func (c *Controller) startAndStore(startCtx context.Context, spec LaunchSpec) (*entry, error) {
	if err := c.checkOpen(); err != nil {
		return nil, err
	}
	current := &entry{spec: cloneLaunchSpec(spec), state: stateStarting, generation: c.generation()}
	c.put(spec.ID, current)

	runCtx, runCancel := context.WithCancel(c.ctx)
	stopForward := context.AfterFunc(startCtx, runCancel)
	process, err := c.factory.Start(runCtx, cloneLaunchSpec(current.spec))
	forwardRemoved := stopForward()
	if err == nil && (!forwardRemoved || startCtx.Err() != nil) {
		err = startCtx.Err()
		if err == nil {
			err = context.Canceled
		}
	}
	if err == nil && process == nil {
		err = errors.New("plugin process factory returned a nil process")
	}
	if err != nil {
		runCancel()
		if process != nil {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), c.stopTimeout)
			_ = process.Stop(cleanupCtx)
			cleanupCancel()
		}
		c.deleteIfCurrent(spec.ID, current)
		return nil, err
	}
	metadata := clonePluginMetadata(process.Metadata())
	hookReady := false
	if metadataErr := metadata.Validate(); metadataErr != nil {
		err = fmt.Errorf("validate process metadata: %w", metadataErr)
	} else if metadata.ID != current.spec.ID || metadata.Version != current.spec.Version {
		err = fmt.Errorf("process metadata %s@%s does not match package %s@%s",
			metadata.ID, metadata.Version, current.spec.ID, current.spec.Version)
	} else if !capabilitiesMatch(current.spec.Capabilities, metadata.Capabilities) {
		err = fmt.Errorf("process capabilities %v do not match package declaration %v", metadata.Capabilities, current.spec.Capabilities)
	} else if c.hooks.OnReady != nil {
		err = c.hooks.OnReady(metadata, permissionCheckedDispatch(process, current.spec.Permissions))
		if err != nil {
			err = fmt.Errorf("register ready plugin: %w", err)
		} else {
			hookReady = true
		}
	}
	if err == nil {
		err = startCtx.Err()
	}
	if err != nil {
		runCancel()
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), c.stopTimeout)
		_ = process.Stop(cleanupCtx)
		cleanupCancel()
		if hookReady && c.hooks.OnStopped != nil {
			c.hooks.OnStopped(current.spec.ID)
		}
		c.deleteIfCurrent(spec.ID, current)
		return nil, err
	}

	c.mu.Lock()
	if c.items[spec.ID] != current {
		c.mu.Unlock()
		runCancel()
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), c.stopTimeout)
		_ = process.Stop(cleanupCtx)
		cleanupCancel()
		return nil, errors.New("plugin runtime state changed during startup")
	}
	current.process = process
	current.cancel = runCancel
	current.monitorDone = make(chan struct{})
	current.state = stateRunning
	current.registered = true
	c.mu.Unlock()
	go c.monitor(runCtx, current)
	return current, nil
}

func (c *Controller) monitor(runCtx context.Context, watched *entry) {
	err := watched.process.Wait(runCtx)
	notify := false
	c.mu.Lock()
	current := c.items[watched.spec.ID]
	if current == watched && current.generation == watched.generation {
		wasStopping := current.state == stateStopping
		if wasStopping || errors.Is(err, context.Canceled) {
			current.state = stateStopped
		} else if err != nil {
			current.state = stateFailed
		} else {
			current.state = stateStopped
		}
		if !wasStopping && current.registered {
			current.registered = false
			notify = true
		}
	}
	c.mu.Unlock()
	if notify && c.hooks.OnStopped != nil {
		c.hooks.OnStopped(watched.spec.ID)
	}
	close(watched.monitorDone)
}

func (c *Controller) stopEntry(parent context.Context, current *entry) error {
	c.mu.RLock()
	process := current.process
	runCancel := current.cancel
	monitorDone := current.monitorDone
	c.mu.RUnlock()
	if process == nil {
		c.notifyStopped(current)
		return nil
	}
	if runCancel != nil {
		runCancel()
	}
	stopCtx, cancel := boundedContext(parent, c.stopTimeout)
	defer cancel()
	stopErr := process.Stop(stopCtx)
	if monitorDone != nil {
		select {
		case <-monitorDone:
		case <-stopCtx.Done():
			return errors.Join(stopErr, stopCtx.Err())
		}
	}
	c.notifyStopped(current)
	return stopErr
}

func (c *Controller) notifyStopped(current *entry) {
	notify := false
	c.mu.Lock()
	if current.registered {
		current.registered = false
		notify = true
	}
	c.mu.Unlock()
	if notify && c.hooks.OnStopped != nil {
		c.hooks.OnStopped(current.spec.ID)
	}
}

func (c *Controller) restorePackage(before []pluginpackage.InstalledPlugin, installed pluginpackage.Receipt) error {
	rollbackCtx, cancel := context.WithTimeout(context.Background(), c.rollbackTimeout)
	defer cancel()
	previous, exists := findInstalled(before, installed.ID)
	if !exists || previous.ActiveVersion == "" {
		if err := c.manager.Uninstall(rollbackCtx, installed.ID, installed.Version); err != nil {
			return fmt.Errorf("remove failed installation %s@%s: %w", installed.ID, installed.Version, err)
		}
		return nil
	}
	if previous.ActiveVersion == installed.Version {
		return nil
	}
	if _, err := c.manager.Rollback(rollbackCtx, installed.ID, previous.ActiveVersion); err != nil {
		return fmt.Errorf("restore active version %s@%s: %w", installed.ID, previous.ActiveVersion, err)
	}
	return nil
}

func resolveLaunchSpec(installed []pluginpackage.InstalledPlugin, receipt pluginpackage.Receipt) (LaunchSpec, error) {
	plugin, exists := findInstalled(installed, receipt.ID)
	if !exists || plugin.ActiveVersion != receipt.Version {
		return LaunchSpec{}, fmt.Errorf("%w: %s@%s is not active after installation", ErrInconsistentInstall, receipt.ID, receipt.Version)
	}
	for _, version := range plugin.Versions {
		if version.Receipt.Version != receipt.Version || !version.Active {
			continue
		}
		if version.Receipt.ManifestDigest != receipt.ManifestDigest {
			return LaunchSpec{}, fmt.Errorf("%w: receipt digest changed for %s@%s", ErrInconsistentInstall, receipt.ID, receipt.Version)
		}
		entrypoint := filepath.Join(version.Path, filepath.FromSlash(receipt.Entrypoint.Path))
		relative, err := filepath.Rel(version.Path, entrypoint)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
			return LaunchSpec{}, fmt.Errorf("%w: entrypoint escapes installed version", ErrInconsistentInstall)
		}
		return LaunchSpec{
			ID: receipt.ID, Name: receipt.Name, Version: receipt.Version, Category: receipt.Category,
			VersionRoot: version.Path, Entrypoint: entrypoint, ManifestDigest: receipt.ManifestDigest,
			Capabilities: append([]string(nil), receipt.Capabilities...),
			Permissions:  append([]string(nil), receipt.Permissions...),
			Signed:       receipt.Signed, Trusted: receipt.Trusted,
		}, nil
	}
	return LaunchSpec{}, fmt.Errorf("%w: active version directory is missing for %s@%s", ErrInconsistentInstall, receipt.ID, receipt.Version)
}

func resolveActiveSpec(installed []pluginpackage.InstalledPlugin, pluginID string) (LaunchSpec, bool) {
	plugin, exists := findInstalled(installed, pluginID)
	if !exists || plugin.ActiveVersion == "" {
		return LaunchSpec{}, false
	}
	for _, version := range plugin.Versions {
		if version.Receipt.Version == plugin.ActiveVersion && version.Active {
			spec, err := resolveLaunchSpec(installed, version.Receipt)
			return spec, err == nil
		}
	}
	return LaunchSpec{}, false
}

func findInstalled(installed []pluginpackage.InstalledPlugin, pluginID string) (pluginpackage.InstalledPlugin, bool) {
	for _, plugin := range installed {
		if plugin.ID == pluginID {
			return plugin, true
		}
	}
	return pluginpackage.InstalledPlugin{}, false
}

func activeReceipt(plugin pluginpackage.InstalledPlugin) (pluginpackage.Receipt, bool) {
	for _, version := range plugin.Versions {
		if version.Active && version.Receipt.Version == plugin.ActiveVersion {
			return version.Receipt, true
		}
	}
	return pluginpackage.Receipt{}, false
}

func samePackage(left, right LaunchSpec) bool {
	return left.ID == right.ID && left.Version == right.Version && left.ManifestDigest == right.ManifestDigest
}

func cloneLaunchSpec(spec LaunchSpec) LaunchSpec {
	result := spec
	result.Capabilities = append([]string(nil), spec.Capabilities...)
	result.Permissions = append([]string(nil), spec.Permissions...)
	return result
}

func clonePluginMetadata(metadata pluginv1.PluginMetadata) pluginv1.PluginMetadata {
	result := metadata
	result.Capabilities = append([]pluginv1.Capability(nil), metadata.Capabilities...)
	result.Commands = append([]pluginv1.CommandSpec(nil), metadata.Commands...)
	for index := range result.Commands {
		result.Commands[index].Aliases = append([]string(nil), metadata.Commands[index].Aliases...)
	}
	result.Watchers = append([]pluginv1.WatcherSpec(nil), metadata.Watchers...)
	for index := range result.Watchers {
		result.Watchers[index].Filter.Kinds = append([]pluginv1.UpdateKind(nil), metadata.Watchers[index].Filter.Kinds...)
		result.Watchers[index].Filter.ChatIDs = append([]int64(nil), metadata.Watchers[index].Filter.ChatIDs...)
		result.Watchers[index].Filter.SenderIDs = append([]int64(nil), metadata.Watchers[index].Filter.SenderIDs...)
	}
	result.Config = append([]pluginv1.ConfigSpec(nil), metadata.Config...)
	for index := range result.Config {
		result.Config[index].Default = append([]byte(nil), metadata.Config[index].Default...)
	}
	return result
}

func permissionCheckedDispatch(process Process, declared []string) DispatchFunc {
	permissions := make(map[string]struct{}, len(declared))
	for _, permission := range declared {
		permissions[permission] = struct{}{}
	}
	return func(ctx context.Context, request pluginv1.DispatchRequest) (pluginv1.DispatchResponse, error) {
		response, err := process.Dispatch(ctx, request)
		if err != nil {
			return pluginv1.DispatchResponse{}, err
		}
		response = cloneDispatchResponse(response)
		if err := response.Validate(); err != nil {
			return pluginv1.DispatchResponse{}, fmt.Errorf("validate plugin dispatch response: %w", err)
		}
		for index, action := range response.Actions {
			required := actionPermission(action)
			if _, allowed := permissions[required]; !allowed {
				return pluginv1.DispatchResponse{}, fmt.Errorf(
					"%w: action %d of kind %q requires %q", ErrPermissionDenied, index, action.Kind, required,
				)
			}
		}
		return response, nil
	}
}

func actionPermission(action pluginv1.Action) string {
	switch action.Kind {
	case pluginv1.ActionReply:
		return "telegram.reply"
	case pluginv1.ActionEdit:
		return "telegram.edit"
	case pluginv1.ActionDelete:
		return "telegram.delete"
	case pluginv1.ActionSend:
		return "telegram.send"
	case pluginv1.ActionInlineAnswer:
		return "telegram.inline"
	case pluginv1.ActionClient:
		if action.ClientOperation != nil {
			return "client." + action.ClientOperation.Name
		}
	}
	return ""
}

func cloneDispatchResponse(response pluginv1.DispatchResponse) pluginv1.DispatchResponse {
	result := response
	result.Actions = append([]pluginv1.Action(nil), response.Actions...)
	for index := range result.Actions {
		result.Actions[index].Buttons = cloneInlineButtons(response.Actions[index].Buttons)
		result.Actions[index].InlineResults = append([]pluginv1.InlineResult(nil), response.Actions[index].InlineResults...)
		for resultIndex := range result.Actions[index].InlineResults {
			result.Actions[index].InlineResults[resultIndex].Buttons = cloneInlineButtons(
				response.Actions[index].InlineResults[resultIndex].Buttons,
			)
		}
		if response.Actions[index].ClientOperation != nil {
			operation := *response.Actions[index].ClientOperation
			result.Actions[index].ClientOperation = &operation
		}
	}
	return result
}

func cloneInlineButtons(buttons [][]pluginv1.InlineButton) [][]pluginv1.InlineButton {
	result := make([][]pluginv1.InlineButton, len(buttons))
	for index := range buttons {
		result[index] = append([]pluginv1.InlineButton(nil), buttons[index]...)
	}
	return result
}

func capabilitiesMatch(declared []string, reported []pluginv1.Capability) bool {
	left := append([]string(nil), declared...)
	right := make([]string, len(reported))
	for index, capability := range reported {
		right[index] = string(capability)
	}
	sort.Strings(left)
	sort.Strings(right)
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func (c *Controller) operationContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	stop := context.AfterFunc(c.ctx, cancel)
	return ctx, func() {
		stop()
		cancel()
	}
}

func boundedContext(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(parent, timeout)
}

func (c *Controller) checkOpen() error {
	c.mu.RLock()
	closed := c.closed
	c.mu.RUnlock()
	if closed {
		return ErrClosed
	}
	return nil
}

func (c *Controller) generation() uint64 {
	c.mu.Lock()
	c.nextID++
	result := c.nextID
	c.mu.Unlock()
	return result
}

func (c *Controller) size() int {
	c.mu.RLock()
	result := len(c.items)
	c.mu.RUnlock()
	return result
}

func (c *Controller) get(id string) *entry {
	c.mu.RLock()
	result := c.items[id]
	c.mu.RUnlock()
	return result
}

func (c *Controller) isRunning(expected *entry) bool {
	c.mu.RLock()
	running := c.items[expected.spec.ID] == expected && expected.state == stateRunning && expected.process != nil
	c.mu.RUnlock()
	return running
}

func (c *Controller) hasProcess(expected *entry) bool {
	c.mu.RLock()
	hasProcess := expected.process != nil
	c.mu.RUnlock()
	return hasProcess
}

func (c *Controller) put(id string, current *entry) {
	c.mu.Lock()
	c.items[id] = current
	c.mu.Unlock()
}

func (c *Controller) delete(id string) {
	c.mu.Lock()
	delete(c.items, id)
	c.mu.Unlock()
}

func (c *Controller) deleteIfCurrent(id string, expected *entry) {
	c.mu.Lock()
	if c.items[id] == expected {
		delete(c.items, id)
	}
	c.mu.Unlock()
}

func (c *Controller) setStateIfCurrent(id string, expected *entry, state runtimeState) {
	c.mu.Lock()
	if c.items[id] == expected {
		expected.state = state
	}
	c.mu.Unlock()
}

func (c *Controller) markFailed(id string, spec LaunchSpec) {
	c.put(id, &entry{spec: cloneLaunchSpec(spec), state: stateFailed, generation: c.generation()})
}
