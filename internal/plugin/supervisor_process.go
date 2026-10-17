package plugin

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	protocol "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

func processRSS(pid int) uint64 {
	if pid <= 0 {
		return 0
	}
	file, err := os.Open(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return 0
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 2 && fields[0] == "VmRSS:" {
			value, parseErr := strconv.ParseUint(fields[1], 10, 64)
			if parseErr == nil {
				return value * 1024
			}
		}
	}
	return 0
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
		Observe:        s.cfg.ObserveRPC,
		RequestHandler: s.handleRequest,
	})
	if err != nil {
		_ = connection.Close()
		return nil, fmt.Errorf("create plugin RPC client: %w", err)
	}
	active.client = client

	handshakeRequest := protocol.HandshakeRequest{
		SupportedVersions: []string{protocol.Version}, AuthToken: token,
		Features:   []string{"streaming_actions", "duplex_host_rpc", "media_binary_stream"},
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
	defer cancel()
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
