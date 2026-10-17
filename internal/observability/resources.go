package observability

import (
	"bufio"
	"context"
	"errors"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Resources struct {
	CapturedAt time.Time `json:"captured_at"`
	Goroutines int       `json:"goroutines"`
	HeapBytes  uint64    `json:"heap_bytes"`
	RSSBytes   uint64    `json:"rss_bytes"`
	PeakRSS    uint64    `json:"peak_rss_bytes"`
}

// Sampler owns exactly one sampling goroutine and one ticker.
type Sampler struct {
	interval time.Duration
	current  atomic.Pointer[Resources]
	cancel   context.CancelFunc
	done     chan struct{}
	mu       sync.Mutex
}

func NewSampler(interval time.Duration) (*Sampler, error) {
	if interval <= 0 {
		return nil, errors.New("resource sample interval must be positive")
	}
	return &Sampler{interval: interval}, nil
}

func (s *Sampler) Start(parent context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		return errors.New("resource sampler already started")
	}
	ctx, cancel := context.WithCancel(parent)
	s.cancel = cancel
	s.done = make(chan struct{})
	s.capture()
	go s.run(ctx, s.done)
	return nil
}

func (s *Sampler) run(ctx context.Context, done chan struct{}) {
	defer close(done)
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.capture()
		}
	}
}

func (s *Sampler) capture() {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	rss, peak := procRSS()
	previous := s.current.Load()
	if previous != nil && previous.PeakRSS > peak {
		peak = previous.PeakRSS
	}
	if rss > peak {
		peak = rss
	}
	s.current.Store(&Resources{
		CapturedAt: time.Now().UTC(),
		Goroutines: runtime.NumGoroutine(),
		HeapBytes:  mem.HeapAlloc,
		RSSBytes:   rss,
		PeakRSS:    peak,
	})
}

func (s *Sampler) Snapshot() Resources {
	current := s.current.Load()
	if current == nil {
		return Resources{}
	}
	return *current
}

func (s *Sampler) Close(ctx context.Context) error {
	s.mu.Lock()
	cancel, done := s.cancel, s.done
	s.cancel = nil
	s.done = nil
	s.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func procRSS() (rss uint64, peak uint64) {
	file, err := os.Open("/proc/self/status")
	if err != nil {
		return 0, 0
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		value, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			continue
		}
		switch fields[0] {
		case "VmRSS:":
			rss = value * 1024
		case "VmHWM:":
			peak = value * 1024
		}
	}
	return rss, peak
}
