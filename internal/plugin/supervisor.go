package plugin

import (
	"context"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

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

// RestartPolicy bounds both retained history and restart frequency.
type RestartPolicy struct {
	Disabled       bool
	MaxRestarts    int
	Window         time.Duration
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
	JitterFraction float64
	DisableJitter  bool
}

func (p RestartPolicy) normalized() (RestartPolicy, error) {
	if p.MaxRestarts == 0 {
		p.MaxRestarts = 4
	}
	if p.Window == 0 {
		p.Window = 5 * time.Minute
	}
	if p.InitialBackoff == 0 {
		p.InitialBackoff = 100 * time.Millisecond
	}
	if p.MaxBackoff == 0 {
		p.MaxBackoff = 5 * time.Second
	}
	if p.JitterFraction == 0 && !p.DisableJitter {
		p.JitterFraction = 0.1
	}
	if p.MaxRestarts < 1 || p.MaxRestarts > 1024 {
		return RestartPolicy{}, fmt.Errorf("restart max must be in range 1..1024")
	}
	if p.Window < time.Millisecond || p.Window > 30*24*time.Hour {
		return RestartPolicy{}, fmt.Errorf("restart window must be between 1ms and 30 days")
	}
	if p.InitialBackoff < time.Millisecond || p.InitialBackoff > time.Hour {
		return RestartPolicy{}, fmt.Errorf("initial restart backoff must be between 1ms and 1h")
	}
	if p.MaxBackoff < p.InitialBackoff || p.MaxBackoff > 24*time.Hour {
		return RestartPolicy{}, fmt.Errorf("maximum restart backoff must be between initial backoff and 24h")
	}
	if p.JitterFraction < 0 || p.JitterFraction > 1 {
		return RestartPolicy{}, fmt.Errorf("restart jitter fraction must be in range 0..1")
	}
	return p, nil
}

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

func (c SupervisorConfig) normalized() (SupervisorConfig, error) {
	if c.Command == "" {
		c.Command = c.Executable
	}
	if strings.TrimSpace(c.Command) == "" {
		return SupervisorConfig{}, fmt.Errorf("plugin command is required")
	}
	if len(c.Args) > protocol.MaxCollectionItems || len(c.Env) > protocol.MaxCollectionItems {
		return SupervisorConfig{}, fmt.Errorf("plugin arguments and environment are limited to %d entries", protocol.MaxCollectionItems)
	}
	if c.InstanceID == "" {
		random, err := randomHex(8)
		if err != nil {
			return SupervisorConfig{}, fmt.Errorf("generate plugin instance ID: %w", err)
		}
		c.InstanceID = "plugin-" + random
	}
	probe := protocol.HandshakeRequest{
		SupportedVersions: []string{protocol.Version}, AuthToken: "validation-token",
		InstanceID: c.InstanceID, Nonce: "validation-nonce", MaxFrameBytes: 1,
	}
	if err := probe.Validate(); err != nil {
		return SupervisorConfig{}, fmt.Errorf("invalid plugin instance ID: %w", err)
	}
	if c.ExpectedPluginID != "" && len(c.ExpectedPluginID) > 128 {
		return SupervisorConfig{}, fmt.Errorf("expected plugin ID exceeds 128 bytes")
	}
	if c.MaxFrameSize == 0 {
		c.MaxFrameSize = protocol.DefaultMaxFrameSize
	}
	if c.MaxFrameSize < 1024 || c.MaxFrameSize > 64<<20 {
		return SupervisorConfig{}, fmt.Errorf("maximum frame size must be between 1KiB and 64MiB")
	}
	if c.MaxInFlight == 0 {
		c.MaxInFlight = 8
	}
	if c.MaxInFlight < 1 || c.MaxInFlight > 1024 {
		return SupervisorConfig{}, fmt.Errorf("maximum in-flight RPCs must be in range 1..1024")
	}

	setDurationDefault(&c.HandshakeTimeout, 5*time.Second)
	setDurationDefault(&c.ConfigureTimeout, 10*time.Second)
	setDurationDefault(&c.StartTimeout, 10*time.Second)
	setDurationDefault(&c.HealthTimeout, 3*time.Second)
	setDurationDefault(&c.DispatchTimeout, 15*time.Second)
	setDurationDefault(&c.StopTimeout, 5*time.Second)
	setDurationDefault(&c.TerminateTimeout, 2*time.Second)
	setDurationDefault(&c.HealthInterval, 30*time.Second)
	for name, value := range map[string]time.Duration{
		"handshake timeout": c.HandshakeTimeout, "configure timeout": c.ConfigureTimeout,
		"start timeout": c.StartTimeout, "health timeout": c.HealthTimeout,
		"dispatch timeout": c.DispatchTimeout, "stop timeout": c.StopTimeout,
		"termination timeout": c.TerminateTimeout, "health interval": c.HealthInterval,
	} {
		if value < time.Millisecond || value > 24*time.Hour {
			return SupervisorConfig{}, fmt.Errorf("%s must be between 1ms and 24h", name)
		}
	}
	if c.OutputLimitBytes == 0 {
		c.OutputLimitBytes = 64 << 10
	}
	if c.OutputLimitBytes < 1024 || c.OutputLimitBytes > 16<<20 {
		return SupervisorConfig{}, fmt.Errorf("process output limit must be between 1KiB and 16MiB")
	}
	if c.Transport == nil {
		c.Transport = UnixTransport{}
	}
	if c.EndpointFactory == nil {
		c.EndpointFactory = func(runtimeDir, _ string) (string, error) {
			candidate := filepath.Join(runtimeDir, "plugin.sock")
			// sockaddr_un is commonly limited to 104-108 bytes. Tests, deeply
			// nested deployments, and long usernames can exceed that even with
			// a short basename. Keep the fallback deterministic per runtime dir.
			if len(candidate) <= 96 {
				return candidate, nil
			}
			digest := sha256.Sum256([]byte(runtimeDir))
			return filepath.Join(os.TempDir(), "srnx-"+hex.EncodeToString(digest[:12])+".sock"), nil
		}
	}
	if c.RuntimeBaseDir == "" {
		c.RuntimeBaseDir = os.TempDir()
	}
	restart, err := c.Restart.normalized()
	if err != nil {
		return SupervisorConfig{}, err
	}
	c.Restart = restart
	c.Args = append([]string(nil), c.Args...)
	c.Env = append([]string(nil), c.Env...)
	c.Values = cloneConfigValues(c.Values)
	return c, nil
}

func setDurationDefault(value *time.Duration, fallback time.Duration) {
	if *value == 0 {
		*value = fallback
	}
}

// SupervisorStatus is a race-free snapshot; all slices in Metadata are copied.
type SupervisorStatus struct {
	State      SupervisorState
	Circuit    CircuitState
	PID        int
	Generation uint64
	Restarts   uint64
	LastError  string
	RuntimeDir string
	Endpoint   string
	StartedAt  time.Time
	ReadyAt    time.Time
	Metadata   protocol.PluginMetadata
}

type managedProcess struct {
	command *exec.Cmd
	done    chan struct{}
	mu      sync.Mutex
	err     error
}

func startManagedProcess(command *exec.Cmd) (*managedProcess, error) {
	if err := command.Start(); err != nil {
		return nil, err
	}
	process := &managedProcess{command: command, done: make(chan struct{})}
	go func() {
		err := command.Wait()
		process.mu.Lock()
		process.err = err
		process.mu.Unlock()
		close(process.done)
	}()
	return process, nil
}

func (p *managedProcess) waitError() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
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

func NewSupervisor(config SupervisorConfig) (*Supervisor, error) {
	normalized, err := config.normalized()
	if err != nil {
		return nil, err
	}
	return &Supervisor{
		cfg: normalized, state: SupervisorNew, circuit: CircuitClosed,
		values: cloneConfigValues(normalized.Values), startResult: make(chan error, 1),
		done: make(chan struct{}), stdout: newCappedBuffer(normalized.OutputLimitBytes),
		stderr:         newCappedBuffer(normalized.OutputLimitBytes),
		restartHistory: make([]time.Time, 0, normalized.Restart.MaxRestarts),
	}, nil
}

// Start launches the child and waits until handshake, configure, start, and an
// initial health probe have all succeeded. An optional single values slice
// overrides SupervisorConfig.Values and is reused on supervised restarts.
func (s *Supervisor) Start(parent context.Context, values ...[]protocol.ConfigValue) error {
	if parent == nil {
		return fmt.Errorf("nil supervisor context")
	}
	if len(values) > 1 {
		return fmt.Errorf("Start accepts at most one configuration slice")
	}
	s.mu.Lock()
	if s.state != SupervisorNew {
		state := s.state
		s.mu.Unlock()
		if state == SupervisorStopped || state == SupervisorCircuitOpen {
			return ErrSupervisorStopped
		}
		return ErrSupervisorStarted
	}
	if len(values) == 1 {
		s.values = cloneConfigValues(values[0])
	}
	s.ctx, s.cancel = context.WithCancel(parent)
	s.state = SupervisorStarting
	s.startedAt = time.Now().UTC()
	s.mu.Unlock()

	go s.run()
	select {
	case err := <-s.startResult:
		return err
	case <-parent.Done():
		return parent.Err()
	}
}

func (s *Supervisor) run() {
	var finalErr error
	initialReported := false
	defer func() {
		if !initialReported {
			if s.wasStopRequested() {
				s.startResult <- ErrSupervisorStopped
			} else {
				s.startResult <- finalErr
			}
		}
		s.mu.Lock()
		s.terminalErr = finalErr
		if s.state != SupervisorCircuitOpen {
			s.state = SupervisorStopped
			s.circuit = CircuitClosed
		}
		s.current = nil
		s.mu.Unlock()
		s.doneOnce.Do(func() { close(s.done) })
	}()

	for {
		if err := s.ctx.Err(); err != nil {
			finalErr = err
			return
		}
		s.mu.Lock()
		s.generation++
		number := s.generation
		if number > 1 {
			s.state = SupervisorRestarting
			s.circuit = CircuitHalfOpen
		} else {
			s.state = SupervisorStarting
		}
		s.mu.Unlock()

		active, err := s.launchGeneration(s.ctx, number)
		if err == nil {
			s.publishReady(active)
			if !initialReported {
				s.startResult <- nil
				initialReported = true
			}
			err = s.monitor(active)
		}

		stopping := errors.Is(err, errSupervisorStopping) || s.ctx.Err() != nil
		if active != nil {
			if stopping {
				s.setStopping()
				cleanupErr := s.cleanupGeneration(active, true)
				finalErr = errors.Join(cleanupErr)
				return
			}
			s.unpublish(active, err)
			cleanupErr := s.cleanupGeneration(active, false)
			err = errors.Join(err, cleanupErr)
		}
		if stopping {
			if !initialReported && !s.wasStopRequested() {
				finalErr = s.ctx.Err()
			} else {
				finalErr = nil
			}
			return
		}
		if err == nil {
			err = fmt.Errorf("%w: lifecycle ended without an error", ErrProcessExited)
		}
		s.recordError(err)
		backoff, allowed := s.reserveRestart(time.Now())
		if !allowed {
			finalErr = fmt.Errorf("%w: %w", ErrCircuitOpen, err)
			s.mu.Lock()
			s.state = SupervisorCircuitOpen
			s.circuit = CircuitOpen
			s.terminalErr = finalErr
			s.mu.Unlock()
			if !initialReported {
				s.startResult <- finalErr
				initialReported = true
			}
			return
		}
		s.mu.Lock()
		s.state = SupervisorRestarting
		s.circuit = CircuitHalfOpen
		s.mu.Unlock()
		timer := time.NewTimer(backoff)
		select {
		case <-timer.C:
		case <-s.ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			if !initialReported && !s.wasStopRequested() {
				finalErr = s.ctx.Err()
			}
			return
		}
	}
}

var errSupervisorStopping = errors.New("plugin supervisor is stopping")

func (s *Supervisor) launchGeneration(parent context.Context, number uint64) (_ *generation, resultErr error) {
	active := &generation{number: number}
	defer func() {
		if resultErr != nil {
			_ = s.cleanupGeneration(active, false)
		}
	}()

	runtimeDir, err := os.MkdirTemp(s.cfg.RuntimeBaseDir, "serenox-plugin-")
	if err != nil {
		return nil, fmt.Errorf("create plugin runtime directory: %w", err)
	}
	active.runtimeDir = runtimeDir
	if err := os.Chmod(runtimeDir, 0o700); err != nil {
		return nil, fmt.Errorf("secure plugin runtime directory: %w", err)
	}
	endpoint, err := s.cfg.EndpointFactory(runtimeDir, s.cfg.InstanceID)
	if err != nil {
		return nil, fmt.Errorf("derive plugin transport endpoint: %w", err)
	}
	if endpoint == "" {
		return nil, fmt.Errorf("derive plugin transport endpoint: empty endpoint")
	}
	active.endpoint = endpoint

	handshakeCtx, cancelHandshake := context.WithTimeout(parent, s.cfg.HandshakeTimeout)
	defer cancelHandshake()
	listener, err := s.cfg.Transport.Listen(handshakeCtx, endpoint)
	if err != nil {
		return nil, fmt.Errorf("listen for plugin connection: %w", err)
	}
	active.listener = listener
	token, err := randomHex(32)
	if err != nil {
		return nil, fmt.Errorf("generate plugin authentication token: %w", err)
	}
	nonce, err := randomHex(24)
	if err != nil {
		return nil, fmt.Errorf("generate plugin handshake nonce: %w", err)
	}

	command := exec.Command(s.cfg.Command, s.cfg.Args...)
	command.Dir = s.cfg.Dir
	command.Env = childEnvironment(s.cfg.Env, map[string]string{
		envSocket: listener.Endpoint(), envToken: token, envInstanceID: s.cfg.InstanceID,
		envProtocol: protocol.Version, envMaxFrameSize: strconv.FormatUint(uint64(s.cfg.MaxFrameSize), 10),
	})
	command.Stdout = s.stdout
	command.Stderr = s.stderr
	configureProcessGroup(command)
	process, err := startManagedProcess(command)
	if err != nil {
		return nil, fmt.Errorf("start plugin process: %w", err)
	}
	active.process = process

	connection, err := acceptChild(handshakeCtx, listener, process)
	if closeErr := listener.Close(); err == nil && closeErr != nil {
		err = closeErr
	}
	active.listener = nil
	if err != nil {
		return nil, fmt.Errorf("accept plugin connection: %w", err)
	}
	client, err := NewRPCClient(connection, RPCClientConfig{
		MaxFrameSize: s.cfg.MaxFrameSize, MaxInFlight: s.cfg.MaxInFlight,
		DefaultTimeout: s.cfg.DispatchTimeout,
	})
	if err != nil {
		_ = connection.Close()
		return nil, fmt.Errorf("create plugin RPC client: %w", err)
	}
	active.client = client

	handshakeRequest := protocol.HandshakeRequest{
		SupportedVersions: []string{protocol.Version}, AuthToken: token,
		InstanceID: s.cfg.InstanceID, HostVersion: s.cfg.HostVersion, Nonce: nonce,
		MaxFrameBytes: s.cfg.MaxFrameSize,
	}
	var handshake protocol.HandshakeResponse
	if err := client.Call(handshakeCtx, protocol.MethodHandshake, handshakeRequest, &handshake); err != nil {
		return nil, fmt.Errorf("plugin handshake: %w", err)
	}
	if err := validateHandshake(handshake, nonce, s.cfg.MaxFrameSize); err != nil {
		client.terminate(err)
		return nil, fmt.Errorf("validate plugin handshake: %w", err)
	}
	if s.cfg.ExpectedPluginID != "" && handshake.Metadata.ID != s.cfg.ExpectedPluginID {
		return nil, fmt.Errorf("plugin metadata ID %q does not match expected %q", handshake.Metadata.ID, s.cfg.ExpectedPluginID)
	}
	s.mu.Lock()
	firstID := s.firstMetadataID
	if firstID == "" {
		s.firstMetadataID = handshake.Metadata.ID
	}
	s.mu.Unlock()
	if firstID != "" && handshake.Metadata.ID != firstID {
		return nil, fmt.Errorf("plugin metadata ID changed across restart from %q to %q", firstID, handshake.Metadata.ID)
	}
	if err := client.SetMaxFrameSize(handshake.MaxFrameBytes); err != nil {
		return nil, err
	}
	active.metadata = cloneMetadata(handshake.Metadata)

	configureCtx, cancelConfigure := context.WithTimeout(parent, s.cfg.ConfigureTimeout)
	var configured protocol.LifecycleResponse
	err = client.Call(configureCtx, protocol.MethodConfigure, protocol.LifecycleRequest{
		Phase: protocol.LifecycleConfigure, Config: cloneConfigValues(s.values),
	}, &configured)
	cancelConfigure()
	if err != nil {
		return nil, fmt.Errorf("configure plugin: %w", err)
	}
	if err := validateLifecycle(configured, "configured"); err != nil {
		client.terminate(err)
		return nil, fmt.Errorf("validate configure response: %w", err)
	}

	startCtx, cancelStart := context.WithTimeout(parent, s.cfg.StartTimeout)
	var started protocol.LifecycleResponse
	err = client.Call(startCtx, protocol.MethodStart, protocol.LifecycleRequest{Phase: protocol.LifecycleStart}, &started)
	cancelStart()
	if err != nil {
		return nil, fmt.Errorf("start plugin lifecycle: %w", err)
	}
	if err := validateLifecycle(started, "ready"); err != nil {
		client.terminate(err)
		return nil, fmt.Errorf("validate start response: %w", err)
	}
	active.ready = true
	if _, err := s.healthCall(parent, client, true); err != nil {
		return nil, fmt.Errorf("initial plugin health check: %w", err)
	}
	return active, nil
}

func acceptChild(ctx context.Context, listener Listener, process *managedProcess) (net.Conn, error) {
	result := make(chan acceptResult, 1)
	acceptCtx, cancel := context.WithCancel(ctx)
	go func() {
		connection, err := listener.Accept(acceptCtx)
		result <- acceptResult{connection: connection, err: err}
	}()
	defer func() {
		cancel()
	}()
	select {
	case accepted := <-result:
		return accepted.connection, accepted.err
	case <-process.done:
		cancel()
		_ = listener.Close()
		<-result
		return nil, unexpectedExit(process.waitError())
	case <-ctx.Done():
		cancel()
		_ = listener.Close()
		<-result
		return nil, ctx.Err()
	}
}

func (s *Supervisor) publishReady(active *generation) {
	s.mu.Lock()
	s.current = active
	s.metadata = cloneMetadata(active.metadata)
	s.state = SupervisorReady
	s.circuit = CircuitClosed
	s.readyAt = time.Now().UTC()
	s.mu.Unlock()
}

func (s *Supervisor) unpublish(active *generation, err error) {
	s.mu.Lock()
	if s.current == active {
		s.current = nil
	}
	if err != nil {
		s.lastErr = err
	}
	s.mu.Unlock()
}

func (s *Supervisor) monitor(active *generation) error {
	var ticker *time.Ticker
	var health <-chan time.Time
	if !s.cfg.DisableHealth {
		ticker = time.NewTicker(s.cfg.HealthInterval)
		health = ticker.C
		defer ticker.Stop()
	}
	for {
		select {
		case <-s.ctx.Done():
			return errSupervisorStopping
		case <-active.process.done:
			return unexpectedExit(active.process.waitError())
		case <-active.client.Done():
			return active.client.Err()
		case <-health:
			if _, err := s.healthCall(s.ctx, active.client, true); err != nil {
				active.client.terminate(err)
				return fmt.Errorf("plugin health check: %w", err)
			}
		}
	}
}

func (s *Supervisor) cleanupGeneration(active *generation, graceful bool) error {
	if active == nil {
		return nil
	}
	var joined error
	if graceful && active.ready && active.client != nil {
		ctx, cancel := context.WithTimeout(context.Background(), s.cfg.StopTimeout)
		var stopped protocol.LifecycleResponse
		err := active.client.Call(ctx, protocol.MethodStop, protocol.LifecycleRequest{Phase: protocol.LifecycleStop}, &stopped)
		cancel()
		if err == nil {
			err = validateLifecycle(stopped, "stopped")
		}
		if err != nil && !errors.Is(err, ioClosedError(active.client)) {
			joined = errors.Join(joined, fmt.Errorf("stop plugin lifecycle: %w", err))
		}
	}
	if active.client != nil {
		_ = active.client.Close()
	}
	if active.listener != nil {
		if err := active.listener.Close(); err != nil {
			joined = errors.Join(joined, fmt.Errorf("close plugin listener: %w", err))
		}
		active.listener = nil
	}
	if active.process != nil {
		if graceful {
			if waitProcess(active.process, s.cfg.TerminateTimeout) == nil {
				// The direct child may have left descendants in its process group.
				// A final group kill is harmless when the group is already empty.
				if err := signalProcessTree(active.process.command, true); err != nil {
					joined = errors.Join(joined, fmt.Errorf("kill remaining plugin process group: %w", err))
				}
				goto removeRuntime
			}
		}
		if err := signalProcessTree(active.process.command, false); err != nil {
			joined = errors.Join(joined, fmt.Errorf("terminate plugin process group: %w", err))
		}
		if waitProcess(active.process, s.cfg.TerminateTimeout) != nil {
			if err := signalProcessTree(active.process.command, true); err != nil {
				joined = errors.Join(joined, fmt.Errorf("kill plugin process group: %w", err))
			}
			if err := waitProcess(active.process, s.cfg.TerminateTimeout); err != nil {
				joined = errors.Join(joined, fmt.Errorf("wait for killed plugin process: %w", err))
			}
		} else if err := signalProcessTree(active.process.command, true); err != nil {
			// The direct process exited after SIGTERM; force-kill any descendants
			// that ignored the graceful process-group signal.
			joined = errors.Join(joined, fmt.Errorf("kill remaining plugin process group: %w", err))
		}
	}

removeRuntime:
	if active.runtimeDir != "" {
		if err := os.RemoveAll(active.runtimeDir); err != nil {
			joined = errors.Join(joined, fmt.Errorf("remove plugin runtime directory: %w", err))
		}
	}
	return joined
}

// ioClosedError returns the client's terminal value solely for errors.Is; it
// avoids suppressing independent stop errors while shutdown races with exit.
func ioClosedError(client *RPCClient) error {
	if client == nil || client.Err() == nil {
		return ErrClientClosed
	}
	return client.Err()
}

func waitProcess(process *managedProcess, timeout time.Duration) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-process.done:
		return nil
	case <-timer.C:
		return context.DeadlineExceeded
	}
}

func unexpectedExit(err error) error {
	if err == nil {
		return fmt.Errorf("%w: status 0", ErrProcessExited)
	}
	return fmt.Errorf("%w: %v", ErrProcessExited, err)
}

func (s *Supervisor) setStopping() {
	s.mu.Lock()
	s.state = SupervisorStopping
	s.mu.Unlock()
}

func (s *Supervisor) wasStopRequested() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.stopRequested
}

func (s *Supervisor) recordError(err error) {
	s.mu.Lock()
	s.lastErr = err
	s.mu.Unlock()
}

func (s *Supervisor) reserveRestart(now time.Time) (time.Duration, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	policy := s.cfg.Restart
	if policy.Disabled {
		return 0, false
	}
	cutoff := now.Add(-policy.Window)
	kept := s.restartHistory[:0]
	for _, at := range s.restartHistory {
		if at.After(cutoff) {
			kept = append(kept, at)
		}
	}
	for index := len(kept); index < len(s.restartHistory); index++ {
		s.restartHistory[index] = time.Time{}
	}
	s.restartHistory = kept
	if len(s.restartHistory) >= policy.MaxRestarts {
		return 0, false
	}
	s.restartHistory = append(s.restartHistory, now)
	s.restarts++
	attempt := len(s.restartHistory) - 1
	backoff := policy.InitialBackoff
	for index := 0; index < attempt && backoff < policy.MaxBackoff; index++ {
		if backoff > policy.MaxBackoff/2 {
			backoff = policy.MaxBackoff
			break
		}
		backoff *= 2
	}
	if backoff > policy.MaxBackoff {
		backoff = policy.MaxBackoff
	}
	return jitter(backoff, policy.JitterFraction), true
}

func jitter(base time.Duration, fraction float64) time.Duration {
	if fraction <= 0 || base <= 0 {
		return base
	}
	var raw [8]byte
	if _, err := cryptorand.Read(raw[:]); err != nil {
		return base
	}
	unit := float64(binary.BigEndian.Uint64(raw[:])) / float64(^uint64(0))
	factor := 1 + ((unit*2)-1)*fraction
	return time.Duration(float64(base) * factor)
}

// Dispatch performs one at-most-once call. Transport failures and malformed
// results terminate only this generation; the owner loop applies restart policy.
func (s *Supervisor) Dispatch(ctx context.Context, request protocol.DispatchRequest) (protocol.DispatchResponse, error) {
	if err := request.Validate(); err != nil {
		return protocol.DispatchResponse{}, err
	}
	client, err := s.readyClient()
	if err != nil {
		return protocol.DispatchResponse{}, err
	}
	var response protocol.DispatchResponse
	err = client.Call(ctx, protocol.MethodDispatch, request, &response)
	if err == nil {
		err = response.Validate()
	}
	if err != nil && terminalRPCError(err) {
		client.terminate(fmt.Errorf("dispatch failed: %w", err))
	}
	return response, err
}

func (s *Supervisor) Health(ctx context.Context, detailed bool) (protocol.HealthResponse, error) {
	client, err := s.readyClient()
	if err != nil {
		return protocol.HealthResponse{}, err
	}
	response, err := s.healthCall(ctx, client, detailed)
	if err != nil && terminalRPCError(err) {
		client.terminate(fmt.Errorf("health call failed: %w", err))
	}
	return response, err
}

func (s *Supervisor) healthCall(parent context.Context, client *RPCClient, detailed bool) (protocol.HealthResponse, error) {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, s.cfg.HealthTimeout)
	defer cancel()
	var response protocol.HealthResponse
	if err := client.Call(ctx, protocol.MethodHealth, protocol.HealthRequest{Detailed: detailed}, &response); err != nil {
		return protocol.HealthResponse{}, err
	}
	if !response.Ready {
		return response, fmt.Errorf("%w: %s", ErrPluginUnhealthy, response.Status)
	}
	if !utf8.ValidString(response.Status) || len(response.Status) > protocol.MaxStringBytes {
		return response, fmt.Errorf("%w: invalid health status", protocol.ErrInvalidDTO)
	}
	if len(response.Metrics) > protocol.MaxCollectionItems {
		return response, fmt.Errorf("%w: health metrics exceed %d entries", protocol.ErrInvalidDTO, protocol.MaxCollectionItems)
	}
	return response, nil
}

func terminalRPCError(err error) bool {
	var wireError *protocol.Error
	if errors.As(err, &wireError) {
		return wireError.Code == protocol.ErrorDeadlineExceeded || wireError.Code == protocol.ErrorInvalidResponse
	}
	return true
}

func (s *Supervisor) readyClient() (*RPCClient, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.state == SupervisorCircuitOpen {
		return nil, ErrCircuitOpen
	}
	if s.state != SupervisorReady || s.current == nil || s.current.client == nil {
		return nil, ErrNotReady
	}
	return s.current.client, nil
}

// Stop is idempotent. Its context bounds how long the caller waits; the owner
// continues its configured finite cleanup if the caller leaves early.
func (s *Supervisor) Stop(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	if s.state == SupervisorNew {
		s.state = SupervisorStopped
		s.mu.Unlock()
		s.doneOnce.Do(func() { close(s.done) })
		return nil
	}
	if s.state != SupervisorStopped && s.state != SupervisorCircuitOpen {
		s.state = SupervisorStopping
		s.stopRequested = true
	}
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	select {
	case <-s.done:
		s.mu.RLock()
		err := s.terminalErr
		state := s.state
		s.mu.RUnlock()
		if state == SupervisorCircuitOpen {
			return nil
		}
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Supervisor) Done() <-chan struct{} { return s.done }

func (s *Supervisor) Wait(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-s.done:
		s.mu.RLock()
		defer s.mu.RUnlock()
		return s.terminalErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Supervisor) Status() SupervisorStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	status := SupervisorStatus{
		State: s.state, Circuit: s.circuit, Generation: s.generation, Restarts: s.restarts,
		StartedAt: s.startedAt, ReadyAt: s.readyAt, Metadata: cloneMetadata(s.metadata),
	}
	if s.lastErr != nil {
		status.LastError = s.lastErr.Error()
	}
	if s.current != nil {
		status.RuntimeDir = s.current.runtimeDir
		status.Endpoint = s.current.endpoint
		if s.current.process != nil && s.current.process.command.Process != nil {
			status.PID = s.current.process.command.Process.Pid
		}
	}
	return status
}

func (s *Supervisor) Metadata() (protocol.PluginMetadata, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.metadata.ID == "" {
		return protocol.PluginMetadata{}, false
	}
	return cloneMetadata(s.metadata), true
}

func (s *Supervisor) Output() OutputSnapshot {
	stdout, stdoutDropped := s.stdout.snapshot()
	stderr, stderrDropped := s.stderr.snapshot()
	return OutputSnapshot{
		Stdout: stdout, Stderr: stderr, StdoutDropped: stdoutDropped, StderrDropped: stderrDropped,
	}
}

func (s *Supervisor) Stdout() string {
	value, _ := s.stdout.snapshot()
	return string(value)
}

func (s *Supervisor) Stderr() string {
	value, _ := s.stderr.snapshot()
	return string(value)
}

func validateHandshake(response protocol.HandshakeResponse, nonce string, requestedMax uint32) error {
	if err := response.Validate(); err != nil {
		return err
	}
	if response.Nonce != nonce {
		return fmt.Errorf("%w: handshake nonce did not match", protocol.ErrInvalidDTO)
	}
	if response.MaxFrameBytes > requestedMax {
		return fmt.Errorf("%w: peer frame maximum %d exceeds offered %d", protocol.ErrInvalidDTO, response.MaxFrameBytes, requestedMax)
	}
	return nil
}

func validateLifecycle(response protocol.LifecycleResponse, expected string) error {
	if response.State != expected {
		return fmt.Errorf("%w: lifecycle state %q, expected %q", protocol.ErrInvalidDTO, response.State, expected)
	}
	if len(response.Actions) > protocol.MaxActions {
		return fmt.Errorf("%w: lifecycle actions exceed %d", protocol.ErrInvalidDTO, protocol.MaxActions)
	}
	for index := range response.Actions {
		if err := response.Actions[index].Validate(); err != nil {
			return fmt.Errorf("%w: lifecycle action %d: %v", protocol.ErrInvalidDTO, index, err)
		}
	}
	return nil
}

func cloneMetadata(metadata protocol.PluginMetadata) protocol.PluginMetadata {
	copy := metadata
	copy.Capabilities = append([]protocol.Capability(nil), metadata.Capabilities...)
	copy.Commands = append([]protocol.CommandSpec(nil), metadata.Commands...)
	for index := range copy.Commands {
		copy.Commands[index].Aliases = append([]string(nil), metadata.Commands[index].Aliases...)
	}
	copy.Watchers = append([]protocol.WatcherSpec(nil), metadata.Watchers...)
	for index := range copy.Watchers {
		filter := metadata.Watchers[index].Filter
		filter.Kinds = append([]protocol.UpdateKind(nil), filter.Kinds...)
		filter.ChatIDs = append([]int64(nil), filter.ChatIDs...)
		filter.SenderIDs = append([]int64(nil), filter.SenderIDs...)
		copy.Watchers[index].Filter = filter
	}
	copy.Config = append([]protocol.ConfigSpec(nil), metadata.Config...)
	for index := range copy.Config {
		copy.Config[index].Default = append([]byte(nil), metadata.Config[index].Default...)
	}
	return copy
}

func cloneConfigValues(values []protocol.ConfigValue) []protocol.ConfigValue {
	result := append([]protocol.ConfigValue(nil), values...)
	for index := range result {
		result[index].Value = append([]byte(nil), values[index].Value...)
	}
	return result
}

func childEnvironment(extra []string, injected map[string]string) []string {
	blocked := make(map[string]struct{}, len(injected))
	for key := range injected {
		blocked[key] = struct{}{}
	}
	base := append(os.Environ(), extra...)
	result := make([]string, 0, len(base)+len(injected))
	positions := make(map[string]int, len(base))
	for _, entry := range base {
		key, _, ok := strings.Cut(entry, "=")
		if !ok || key == "" {
			continue
		}
		if _, omit := blocked[key]; omit {
			continue
		}
		if position, exists := positions[key]; exists {
			result[position] = entry
			continue
		}
		positions[key] = len(result)
		result = append(result, entry)
	}
	for _, key := range []string{envSocket, envToken, envInstanceID, envProtocol, envMaxFrameSize} {
		result = append(result, key+"="+injected[key])
	}
	return result
}

func randomHex(byteCount int) (string, error) {
	value := make([]byte, byteCount)
	if _, err := cryptorand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}
