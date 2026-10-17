package plugin

import (
	"context"
	"errors"
	"fmt"
	"time"

	protocol "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

// NewSupervisor creates a supervisor with validated, immutable runtime
// configuration.
func NewSupervisor(config SupervisorConfig) (*Supervisor, error) {
	normalized, err := config.normalized()
	if err != nil {
		return nil, err
	}
	return &Supervisor{
		cfg: normalized, state: SupervisorNew, circuit: CircuitClosed,
		values: cloneConfigValues(normalized.Values), startResult: make(chan error, 1),
		done: make(chan struct{}), stdout: newCappedBuffer(normalized.OutputLimitBytes),
		stderr: newCappedBuffer(normalized.OutputLimitBytes), healthMetrics: make(map[string]uint64),
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
