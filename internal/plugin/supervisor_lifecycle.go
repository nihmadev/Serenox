package plugin

import (
	"errors"
	"fmt"
	"time"
)

var errSupervisorStopping = errors.New("plugin supervisor is stopping")

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
