package plugin

import (
	"context"

	protocol "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

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
		HealthMetrics: make(map[string]uint64, len(s.healthMetrics)),
	}
	for key, value := range s.healthMetrics {
		status.HealthMetrics[key] = value
	}
	if s.lastErr != nil {
		status.LastError = s.lastErr.Error()
	}
	if s.current != nil {
		status.RuntimeDir = s.current.runtimeDir
		status.Endpoint = s.current.endpoint
		if s.current.process != nil && s.current.process.command.Process != nil {
			status.PID = s.current.process.command.Process.Pid
			status.RSSBytes = processRSS(status.PID)
		}
	}
	if value, ok := status.HealthMetrics["queue_depth"]; ok {
		status.QueueDepth = int(value)
	}
	if value, ok := status.HealthMetrics["worker_count"]; ok {
		status.WorkerCount = int(value)
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
