package plugin

import (
	cryptorand "crypto/rand"
	"encoding/binary"
	"time"
)

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
