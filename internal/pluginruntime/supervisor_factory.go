package pluginruntime

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/nihmadev/Serenox/internal/plugin"
	"github.com/nihmadev/Serenox/internal/pluginpackage"
	pluginv1 "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

// SupervisedProcess is the adapter boundary around plugin.Supervisor. It is
// public so configuration translation can be unit-tested without a subprocess
// or local socket.
type SupervisedProcess interface {
	Start(context.Context, ...[]pluginv1.ConfigValue) error
	Metadata() (pluginv1.PluginMetadata, bool)
	Dispatch(context.Context, pluginv1.DispatchRequest) (pluginv1.DispatchResponse, error)
	Wait(context.Context) error
	Stop(context.Context) error
}

type SupervisorConstructor func(plugin.SupervisorConfig) (SupervisedProcess, error)

type SupervisorFactoryConfig struct {
	// Template supplies finite RPC, timeout, restart, environment, transport,
	// and resource limits. Command, Executable, Dir, ExpectedPluginID, and
	// InstanceID are always replaced from the verified package.
	Template plugin.SupervisorConfig

	// Template.Args are passed to native Go executables. PythonSidecarArgs are
	// passed to the Python host before "--module <verified-entrypoint>".
	EnablePython         bool
	PythonExecutable     string
	PythonSidecarArgs    []string
	ActionHandler        func(context.Context, []pluginv1.Action) error
	ActionResultsHandler func(context.Context, []pluginv1.Action) (pluginv1.ActionBatchResponse, error)
	StorageHandler       func(context.Context, string, int64, pluginv1.StorageRequest) (pluginv1.StorageResult, error)
	ClientHandler        func(context.Context, string, int64, pluginv1.ClientRequest) (pluginv1.ClientResult, error)
	ModerationHandler    func(context.Context, string, int64, string, pluginv1.ModerationRequest) (pluginv1.ModerationResult, error)
	MediaHandler         func(context.Context, string, int64, pluginv1.MediaRequest) (pluginv1.MediaResult, error)
	HTTPHandler          func(context.Context, string, int64, pluginpackage.NetworkPolicy, pluginv1.HTTPRequest) (pluginv1.HTTPResponse, error)
	JobsHandler          func(context.Context, string, int64, pluginv1.JobRPCRequest) (any, error)
	EventsHandler        func(context.Context, string, int64, pluginv1.EventPublishRequest) (pluginv1.EventPublishResult, error)
	// ValidateRuntime runs again during activation, including restore, so an
	// interpreter or sidecar cannot become active after it was removed or
	// downgraded since installation.
	ValidateRuntime    func(LaunchSpec) error
	InterpreterPath    string
	InterpreterVersion string
	SidecarVersion     string
	SDKVersion         string
	ProtocolVersion    string
	PythonLimits       pluginpackage.ResourceLimits

	Constructor SupervisorConstructor
}

type SupervisorFactory struct {
	template             plugin.SupervisorConfig
	pythonEnabled        bool
	pythonExecutable     string
	pythonSidecarArgs    []string
	actionHandler        func(context.Context, []pluginv1.Action) error
	actionResultsHandler func(context.Context, []pluginv1.Action) (pluginv1.ActionBatchResponse, error)
	storageHandler       func(context.Context, string, int64, pluginv1.StorageRequest) (pluginv1.StorageResult, error)
	clientHandler        func(context.Context, string, int64, pluginv1.ClientRequest) (pluginv1.ClientResult, error)
	moderationHandler    func(context.Context, string, int64, string, pluginv1.ModerationRequest) (pluginv1.ModerationResult, error)
	mediaHandler         func(context.Context, string, int64, pluginv1.MediaRequest) (pluginv1.MediaResult, error)
	httpHandler          func(context.Context, string, int64, pluginpackage.NetworkPolicy, pluginv1.HTTPRequest) (pluginv1.HTTPResponse, error)
	jobsHandler          func(context.Context, string, int64, pluginv1.JobRPCRequest) (any, error)
	eventsHandler        func(context.Context, string, int64, pluginv1.EventPublishRequest) (pluginv1.EventPublishResult, error)
	validateRuntime      func(LaunchSpec) error
	interpreterPath      string
	interpreterVersion   string
	sidecarVersion       string
	sdkVersion           string
	protocolVersion      string
	pythonLimits         pluginpackage.ResourceLimits
	constructor          SupervisorConstructor
}

var (
	_ SupervisedProcess = (*plugin.Supervisor)(nil)
	_ Factory           = (*SupervisorFactory)(nil)
	_ Process           = (*supervisorProcess)(nil)
)

func NewSupervisorFactory(config SupervisorFactoryConfig) (*SupervisorFactory, error) {
	if len(config.PythonSidecarArgs)+2 > pluginv1.MaxCollectionItems {
		return nil, fmt.Errorf("Python sidecar arguments exceed protocol limit %d", pluginv1.MaxCollectionItems)
	}
	pythonExecutable := strings.TrimSpace(config.PythonExecutable)
	if config.EnablePython && pythonExecutable == "" {
		pythonExecutable = "python3"
	}
	constructor := config.Constructor
	if constructor == nil {
		constructor = func(supervisorConfig plugin.SupervisorConfig) (SupervisedProcess, error) {
			return plugin.NewSupervisor(supervisorConfig)
		}
	}
	template := config.Template
	template.Args = append([]string(nil), config.Template.Args...)
	template.Env = append([]string(nil), config.Template.Env...)
	template.Values = cloneConfigValues(config.Template.Values)
	return &SupervisorFactory{
		template: template, pythonEnabled: config.EnablePython, pythonExecutable: pythonExecutable,
		pythonSidecarArgs: append([]string(nil), config.PythonSidecarArgs...),
		actionHandler:     config.ActionHandler, actionResultsHandler: config.ActionResultsHandler, storageHandler: config.StorageHandler, clientHandler: config.ClientHandler, moderationHandler: config.ModerationHandler, mediaHandler: config.MediaHandler, httpHandler: config.HTTPHandler, jobsHandler: config.JobsHandler, eventsHandler: config.EventsHandler, constructor: constructor,
		validateRuntime: config.ValidateRuntime,
		interpreterPath: config.InterpreterPath, interpreterVersion: config.InterpreterVersion,
		sidecarVersion: config.SidecarVersion, sdkVersion: config.SDKVersion,
		protocolVersion: config.ProtocolVersion,
		pythonLimits:    config.PythonLimits,
	}, nil
}

func (f *SupervisorFactory) Start(ctx context.Context, spec LaunchSpec) (Process, error) {
	if ctx == nil {
		return nil, errors.New("nil supervisor lifetime context")
	}
	config, err := f.configFor(spec)
	if err != nil {
		return nil, err
	}
	supervisor, err := f.constructor(config)
	if err != nil {
		return nil, fmt.Errorf("construct plugin supervisor: %w", err)
	}
	if supervisor == nil {
		return nil, errors.New("supervisor constructor returned nil")
	}
	if err := supervisor.Start(ctx); err != nil {
		f.stopAfterFailedStart(supervisor)
		return nil, fmt.Errorf("start plugin supervisor: %w", err)
	}
	metadata, ready := supervisor.Metadata()
	if !ready {
		f.stopAfterFailedStart(supervisor)
		return nil, errors.New("plugin supervisor started without metadata")
	}
	if !slices.Equal(metadata.EventPublications, spec.EventPublications) || !slices.Equal(metadata.EventSubscriptions, spec.EventSubscriptions) {
		f.stopAfterFailedStart(supervisor)
		return nil, errors.New("plugin event declarations exceed or differ from signed package policy")
	}
	return &supervisorProcess{SupervisedProcess: supervisor, metadata: metadata, factory: f}, nil
}

func (f *SupervisorFactory) configFor(spec LaunchSpec) (plugin.SupervisorConfig, error) {
	if f.validateRuntime != nil {
		if err := f.validateRuntime(spec); err != nil {
			return plugin.SupervisorConfig{}, fmt.Errorf("validate %s runtime: %w", spec.Category, err)
		}
	}
	if strings.TrimSpace(spec.ID) == "" || strings.TrimSpace(spec.Version) == "" {
		return plugin.SupervisorConfig{}, errors.New("launch specification requires plugin ID and version")
	}
	if spec.VersionRoot == "" || spec.Entrypoint == "" {
		return plugin.SupervisorConfig{}, errors.New("launch specification requires installed root and entrypoint")
	}
	relative, err := filepath.Rel(spec.VersionRoot, spec.Entrypoint)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return plugin.SupervisorConfig{}, errors.New("launch entrypoint escapes installed version root")
	}

	config := f.template
	config.Env = append([]string(nil), f.template.Env...)
	config.Values = cloneConfigValues(f.template.Values)
	config.Command = ""
	config.Executable = ""
	config.Dir = spec.VersionRoot
	config.ExpectedPluginID = spec.ID
	config.PluginID = spec.ID
	// A fresh random instance ID is generated by each Supervisor. Reusing a
	// template value across several plugins would couple authentication scopes.
	config.InstanceID = ""
	switch spec.Category {
	case pluginpackage.CategoryGoExecutable:
		config.Command = spec.Entrypoint
		config.Args = append([]string(nil), f.template.Args...)
	case pluginpackage.CategoryPythonSidecar:
		if !f.pythonEnabled {
			return plugin.SupervisorConfig{}, errors.New("Python sidecar support is disabled; configure a Python sidecar command to load this package")
		}
		if err := enforcePythonLimits(spec.Resources, f.pythonLimits); err != nil {
			return plugin.SupervisorConfig{}, err
		}
		config.Command = f.pythonExecutable
		config.Args = make([]string, 0, len(f.pythonSidecarArgs)+20)
		config.Args = append(config.Args, f.pythonSidecarArgs...)
		if spec.Resources.QueueCapacity > 0 {
			config.Args = append(config.Args, "--queue-capacity", strconv.Itoa(spec.Resources.QueueCapacity))
		}
		if spec.Resources.Workers > 0 {
			config.Args = append(config.Args, "--workers", strconv.Itoa(spec.Resources.Workers))
		}
		if spec.Resources.ActionBatch > 0 {
			config.Args = append(config.Args, "--action-batch", strconv.Itoa(spec.Resources.ActionBatch))
		}
		if spec.Resources.ActionBytes > 0 {
			config.Args = append(config.Args, "--action-bytes", strconv.Itoa(spec.Resources.ActionBytes))
		}
		if spec.Resources.StorageEntries > 0 {
			config.Args = append(config.Args, "--storage-entries", strconv.Itoa(spec.Resources.StorageEntries))
		}
		if spec.Resources.StorageValueBytes > 0 {
			config.Args = append(config.Args, "--storage-value-bytes", strconv.Itoa(spec.Resources.StorageValueBytes))
		}
		if spec.Resources.CallbackCapacity > 0 {
			config.Args = append(config.Args, "--callback-capacity", strconv.Itoa(spec.Resources.CallbackCapacity))
		}
		if spec.Resources.MaxFrameBytes > 0 {
			config.MaxFrameSize = uint32(spec.Resources.MaxFrameBytes)
			config.Args = append(config.Args, "--max-frame-bytes", strconv.Itoa(spec.Resources.MaxFrameBytes))
		}
		if spec.Resources.HandlerTimeoutMS > 0 {
			config.Args = append(config.Args, "--handler-timeout-ms", strconv.Itoa(spec.Resources.HandlerTimeoutMS))
		}
		if spec.Resources.ShutdownTimeoutMS > 0 {
			config.Args = append(config.Args, "--shutdown-timeout-ms", strconv.Itoa(spec.Resources.ShutdownTimeoutMS))
		}
		if spec.Resources.MediaMaxSizeBytes > 0 {
			config.Args = append(config.Args, "--media-max-size-bytes", strconv.FormatInt(spec.Resources.MediaMaxSizeBytes, 10))
		}
		if spec.Resources.MediaChunkSizeBytes > 0 {
			config.Args = append(config.Args, "--media-chunk-size-bytes", strconv.Itoa(spec.Resources.MediaChunkSizeBytes))
		}
		if spec.Resources.MediaMaxConcurrent > 0 {
			config.Args = append(config.Args, "--media-max-concurrent", strconv.Itoa(spec.Resources.MediaMaxConcurrent))
		}
		if spec.Resources.MediaQuotaBytes > 0 {
			config.Args = append(config.Args, "--media-quota-bytes", strconv.FormatInt(spec.Resources.MediaQuotaBytes, 10))
		}
		if spec.Resources.MediaTransferTimeoutMS > 0 {
			config.Args = append(config.Args, "--media-transfer-timeout-ms", strconv.Itoa(spec.Resources.MediaTransferTimeoutMS))
		}
		if spec.Resources.MediaIdleTimeoutMS > 0 {
			config.Args = append(config.Args, "--media-idle-timeout-ms", strconv.Itoa(spec.Resources.MediaIdleTimeoutMS))
		}
		if len(spec.Capabilities) > 0 {
			config.Args = append(config.Args, "--capabilities", strings.Join(spec.Capabilities, ","))
		}
		module := spec.Module
		if module == "" {
			module = spec.Entrypoint
		}
		config.Args = append(config.Args, "--module", module)
		if spec.Class != "" {
			config.Args = append(config.Args, "--class", spec.Class)
		}
	default:
		return plugin.SupervisorConfig{}, fmt.Errorf("unsupported plugin category %q", spec.Category)
	}
	// Host reverse-RPC handlers belong to the supervisor regardless of whether
	// the child is a native Go executable or a Python sidecar. Keeping these
	// assignments inside the Python branch silently disabled host.media for Go
	// plugins.
	config.ActionHandler = permissionCheckedActions(f.actionHandler, spec.Permissions)
	if f.actionResultsHandler != nil {
		config.ActionResultsHandler = permissionCheckedActionResults(f.actionResultsHandler, spec.Permissions)
	}
	config.StorageHandler = permissionCheckedStorage(f.storageHandler, spec.Permissions)
	config.ClientHandler = permissionCheckedClient(f.clientHandler, spec.Permissions)
	config.ModerationHandler = permissionCheckedModeration(f.moderationHandler, spec.Permissions, spec.Capabilities)
	config.MediaHandler = permissionCheckedMedia(f.mediaHandler, spec.Permissions)
	config.NetworkPolicy = spec.Network
	config.HTTPHandler = permissionCheckedHTTP(f.httpHandler, spec.Permissions, spec.Network, spec.Capabilities)
	config.JobsHandler = permissionCheckedJobs(f.jobsHandler, spec.Permissions, spec.Capabilities)
	config.EventsHandler = permissionCheckedEvents(f.eventsHandler, spec)
	return config, nil
}

func permissionCheckedEvents(handle func(context.Context, string, int64, pluginv1.EventPublishRequest) (pluginv1.EventPublishResult, error), spec LaunchSpec) func(context.Context, string, int64, pluginv1.EventPublishRequest) (pluginv1.EventPublishResult, error) {
	permissions := permissionSet(spec.Permissions)
	capable := slices.Contains(spec.Capabilities, string(pluginv1.CapabilityEventsPublish))
	declared := make(map[string]struct{}, len(spec.EventPublications))
	for _, publication := range spec.EventPublications {
		declared[fmt.Sprintf("%s/v%d", publication.Type, publication.Version)] = struct{}{}
	}
	return func(ctx context.Context, pluginID string, accountID int64, request pluginv1.EventPublishRequest) (pluginv1.EventPublishResult, error) {
		if !capable {
			return pluginv1.EventPublishResult{}, fmt.Errorf("%w: events.publish capability is not declared", ErrPermissionDenied)
		}
		key := fmt.Sprintf("%s/v%d", request.Type, request.Version)
		if _, ok := declared[key]; !ok {
			return pluginv1.EventPublishResult{}, fmt.Errorf("%w: event %s is not declared", ErrPermissionDenied, key)
		}
		if _, ok := permissions[pluginv1.EventPermission("publish", request.Type, request.Version)]; !ok {
			return pluginv1.EventPublishResult{}, fmt.Errorf("%w: event publish permission is missing", ErrPermissionDenied)
		}
		if handle == nil {
			return pluginv1.EventPublishResult{}, errors.New("event handler is not configured")
		}
		return handle(ctx, pluginID, accountID, request)
	}
}

func enforcePythonLimits(requested, maximum pluginpackage.ResourceLimits) error {
	checks := []struct {
		name  string
		value int
		limit int
	}{
		{"queue_capacity", requested.QueueCapacity, maximum.QueueCapacity},
		{"workers", requested.Workers, maximum.Workers},
		{"action_batch", requested.ActionBatch, maximum.ActionBatch},
		{"action_bytes", requested.ActionBytes, maximum.ActionBytes},
		{"storage_entries", requested.StorageEntries, maximum.StorageEntries},
		{"storage_value_bytes", requested.StorageValueBytes, maximum.StorageValueBytes},
		{"callback_capacity", requested.CallbackCapacity, maximum.CallbackCapacity},
		{"max_frame_bytes", requested.MaxFrameBytes, maximum.MaxFrameBytes},
		{"handler_timeout_ms", requested.HandlerTimeoutMS, maximum.HandlerTimeoutMS},
		{"shutdown_timeout_ms", requested.ShutdownTimeoutMS, maximum.ShutdownTimeoutMS},
	}
	for _, check := range checks {
		if check.value > 0 && check.limit > 0 && check.value > check.limit {
			return fmt.Errorf("Python resource %s=%d exceeds configured maximum %d", check.name, check.value, check.limit)
		}
	}
	int64Checks := []struct {
		name         string
		value, limit int64
	}{
		{"media_max_size_bytes", requested.MediaMaxSizeBytes, maximum.MediaMaxSizeBytes},
		{"media_quota_bytes", requested.MediaQuotaBytes, maximum.MediaQuotaBytes},
	}
	for _, check := range int64Checks {
		if check.value > 0 && check.limit > 0 && check.value > check.limit {
			return fmt.Errorf("Python resource %s=%d exceeds configured maximum %d", check.name, check.value, check.limit)
		}
	}
	mediaIntChecks := []struct {
		name         string
		value, limit int
	}{
		{"media_chunk_size_bytes", requested.MediaChunkSizeBytes, maximum.MediaChunkSizeBytes},
		{"media_max_concurrent", requested.MediaMaxConcurrent, maximum.MediaMaxConcurrent},
		{"media_transfer_timeout_ms", requested.MediaTransferTimeoutMS, maximum.MediaTransferTimeoutMS},
		{"media_idle_timeout_ms", requested.MediaIdleTimeoutMS, maximum.MediaIdleTimeoutMS},
	}
	for _, check := range mediaIntChecks {
		if check.value > 0 && check.limit > 0 && check.value > check.limit {
			return fmt.Errorf("Python resource %s=%d exceeds configured maximum %d", check.name, check.value, check.limit)
		}
	}
	return nil
}

func (f *SupervisorFactory) stopAfterFailedStart(supervisor SupervisedProcess) {
	timeout := f.template.StopTimeout
	if timeout <= 0 {
		timeout = DefaultStopTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	_ = supervisor.Stop(ctx)
	cancel()
}

type supervisorProcess struct {
	SupervisedProcess
	metadata pluginv1.PluginMetadata
	factory  *SupervisorFactory
}

func (p *supervisorProcess) Metadata() pluginv1.PluginMetadata {
	return clonePluginMetadata(p.metadata)
}

func (p *supervisorProcess) RuntimeStatus() RuntimeStatus {
	status := RuntimeStatus{InterpreterPath: p.factory.interpreterPath, InterpreterVersion: p.factory.interpreterVersion, SidecarVersion: p.factory.sidecarVersion, SDKVersion: p.factory.sdkVersion, ProtocolVersion: p.factory.protocolVersion}
	if supervisor, ok := p.SupervisedProcess.(*plugin.Supervisor); ok {
		snapshot := supervisor.Status()
		status.ProcessState = string(snapshot.State)
		status.RestartCount = snapshot.Restarts
		status.CircuitState = string(snapshot.Circuit)
		status.QueueDepth = snapshot.QueueDepth
		status.WorkerCount = snapshot.WorkerCount
		status.RSSBytes = snapshot.RSSBytes
		status.LastError = snapshot.LastError
	}
	return status
}

func cloneConfigValues(values []pluginv1.ConfigValue) []pluginv1.ConfigValue {
	result := make([]pluginv1.ConfigValue, len(values))
	for index, value := range values {
		result[index] = value
		result[index].Value = append([]byte(nil), value.Value...)
	}
	return result
}
