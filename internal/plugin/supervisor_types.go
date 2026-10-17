package plugin

import (
	"context"
	"errors"
	"net"
	"os/exec"
	"sync"
	"time"

	"github.com/nihmadev/Serenox/internal/pluginpackage"
	protocol "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

var (
	ErrSupervisorStarted = errors.New("plugin supervisor is already started")
	ErrSupervisorStopped = errors.New("plugin supervisor is stopped")
	ErrCircuitOpen       = errors.New("plugin restart circuit is open")
	ErrProcessExited     = errors.New("plugin process exited unexpectedly")
	ErrPluginUnhealthy   = errors.New("plugin health check reported not ready")
)

const (
	envSocket       = "SERENOX_PLUGIN_SOCKET"
	envToken        = "SERENOX_PLUGIN_TOKEN"
	envInstanceID   = "SERENOX_PLUGIN_INSTANCE_ID"
	envProtocol     = "SERENOX_PLUGIN_PROTOCOL"
	envMaxFrameSize = "SERENOX_PLUGIN_MAX_FRAME_BYTES"
)

// SupervisorState is the externally observable lifecycle state of one process.
type SupervisorState string

const (
	SupervisorNew         SupervisorState = "new"
	SupervisorStarting    SupervisorState = "starting"
	SupervisorReady       SupervisorState = "ready"
	SupervisorRestarting  SupervisorState = "restarting"
	SupervisorStopping    SupervisorState = "stopping"
	SupervisorStopped     SupervisorState = "stopped"
	SupervisorCircuitOpen SupervisorState = "circuit_open"
)

type CircuitState string

const (
	CircuitClosed   CircuitState = "closed"
	CircuitHalfOpen CircuitState = "half_open"
	CircuitOpen     CircuitState = "open"
)

// EndpointFactory lets another operating-system transport derive its endpoint
// from the owned runtime directory without changing process supervision.
type EndpointFactory func(runtimeDir, instanceID string) (string, error)

// SupervisorConfig contains only finite limits. Command is preferred;
// Executable is retained as a descriptive alias for callers.
type SupervisorConfig struct {
	Command    string
	Executable string
	Args       []string
	Env        []string
	Dir        string

	InstanceID       string
	ExpectedPluginID string
	HostVersion      string
	Values           []protocol.ConfigValue
	// ActionHandler receives bounded reverse-RPC action batches emitted by a
	// duplex plugin while a dispatch handler is still running.
	ActionHandler func(context.Context, []protocol.Action) error
	// ActionResultsHandler is the typed variant. ActionHandler remains for
	// older hosts and receives synthetic success acknowledgements only.
	ActionResultsHandler func(context.Context, []protocol.Action) (protocol.ActionBatchResponse, error)
	StorageHandler       func(context.Context, string, int64, protocol.StorageRequest) (protocol.StorageResult, error)
	ClientHandler        func(context.Context, string, int64, protocol.ClientRequest) (protocol.ClientResult, error)
	ModerationHandler    func(context.Context, string, int64, string, protocol.ModerationRequest) (protocol.ModerationResult, error)
	MediaHandler         func(context.Context, string, int64, protocol.MediaRequest) (protocol.MediaResult, error)
	HTTPHandler          func(context.Context, string, int64, pluginpackage.NetworkPolicy, protocol.HTTPRequest) (protocol.HTTPResponse, error)
	JobsHandler          func(context.Context, string, int64, protocol.JobRPCRequest) (any, error)
	EventsHandler        func(context.Context, string, int64, protocol.EventPublishRequest) (protocol.EventPublishResult, error)
	NetworkPolicy        pluginpackage.NetworkPolicy
	PluginID             string
	AccountID            int64
	ObserveRPC           func(protocol.Method, string, time.Duration)

	Transport       Transport
	EndpointFactory EndpointFactory
	RuntimeBaseDir  string

	MaxFrameSize uint32
	MaxInFlight  int

	HandshakeTimeout time.Duration
	ConfigureTimeout time.Duration
	StartTimeout     time.Duration
	HealthTimeout    time.Duration
	DispatchTimeout  time.Duration
	StopTimeout      time.Duration
	TerminateTimeout time.Duration
	HealthInterval   time.Duration
	DisableHealth    bool

	OutputLimitBytes int
	Restart          RestartPolicy
}

// SupervisorStatus is a race-free snapshot; all slices in Metadata are copied.
type SupervisorStatus struct {
	State         SupervisorState
	Circuit       CircuitState
	PID           int
	Generation    uint64
	Restarts      uint64
	LastError     string
	RuntimeDir    string
	Endpoint      string
	StartedAt     time.Time
	ReadyAt       time.Time
	Metadata      protocol.PluginMetadata
	QueueDepth    int
	WorkerCount   int
	RSSBytes      uint64
	HealthMetrics map[string]uint64
}

type managedProcess struct {
	command *exec.Cmd
	done    chan struct{}
	mu      sync.Mutex
	err     error
}

type generation struct {
	number     uint64
	runtimeDir string
	endpoint   string
	listener   Listener
	process    *managedProcess
	client     *RPCClient
	metadata   protocol.PluginMetadata
	ready      bool
}

type acceptResult struct {
	connection net.Conn
	err        error
}

// Supervisor owns a single child at a time, one lifecycle goroutine, one RPC
// reader, and one os/exec waiter. Every goroutine is joined before Done closes.
type Supervisor struct {
	cfg SupervisorConfig

	mu              sync.RWMutex
	state           SupervisorState
	circuit         CircuitState
	current         *generation
	metadata        protocol.PluginMetadata
	healthMetrics   map[string]uint64
	firstMetadataID string
	lastErr         error
	terminalErr     error
	stopRequested   bool
	startedAt       time.Time
	readyAt         time.Time
	generation      uint64
	restarts        uint64
	restartHistory  []time.Time
	values          []protocol.ConfigValue
	ctx             context.Context
	cancel          context.CancelFunc
	startResult     chan error
	done            chan struct{}
	doneOnce        sync.Once

	stdout *cappedBuffer
	stderr *cappedBuffer
}
