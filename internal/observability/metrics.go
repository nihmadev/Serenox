// Package observability provides bounded metrics, health, logging, and process
// resource sampling without exporting unbounded label cardinality.
package observability

import (
	"errors"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

var (
	ErrInvalidMetric = errors.New("invalid metric name or labels")
	ErrSeriesLimit   = errors.New("metric series limit reached")
)

// Point is an immutable metric snapshot entry.
type Point struct {
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels,omitempty"`
	Value  int64             `json:"value"`
}

type series struct {
	name   string
	labels map[string]string
	value  atomic.Int64
}

// Metrics is a concurrency-safe registry with a hard series count limit.
type Metrics struct {
	mu       sync.RWMutex
	max      int
	series   map[string]*series
	rejected atomic.Uint64
}

func NewMetrics(maxSeries int) (*Metrics, error) {
	if maxSeries <= 0 {
		return nil, errors.New("max metric series must be positive")
	}
	return &Metrics{max: maxSeries, series: make(map[string]*series, maxSeries)}, nil
}

// Add adds delta to a counter/gauge series. It refuses new series after the
// configured cap rather than allowing attacker-controlled label growth.
func (m *Metrics) Add(name string, labels map[string]string, delta int64) error {
	key, copied, err := metricKey(name, labels)
	if err != nil {
		return err
	}

	m.mu.RLock()
	s := m.series[key]
	m.mu.RUnlock()
	if s != nil {
		s.value.Add(delta)
		return nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if s = m.series[key]; s != nil {
		s.value.Add(delta)
		return nil
	}
	if len(m.series) >= m.max {
		m.rejected.Add(1)
		return ErrSeriesLimit
	}
	s = &series{name: name, labels: copied}
	s.value.Store(delta)
	m.series[key] = s
	return nil
}

func (m *Metrics) Set(name string, labels map[string]string, value int64) error {
	key, copied, err := metricKey(name, labels)
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if s := m.series[key]; s != nil {
		s.value.Store(value)
		return nil
	}
	if len(m.series) >= m.max {
		m.rejected.Add(1)
		return ErrSeriesLimit
	}
	s := &series{name: name, labels: copied}
	s.value.Store(value)
	m.series[key] = s
	return nil
}

func (m *Metrics) Snapshot() []Point {
	m.mu.RLock()
	points := make([]Point, 0, len(m.series))
	for _, s := range m.series {
		labels := make(map[string]string, len(s.labels))
		for key, value := range s.labels {
			labels[key] = value
		}
		points = append(points, Point{Name: s.name, Labels: labels, Value: s.value.Load()})
	}
	m.mu.RUnlock()
	sort.Slice(points, func(i, j int) bool {
		if points[i].Name == points[j].Name {
			return labelsKey(points[i].Labels) < labelsKey(points[j].Labels)
		}
		return points[i].Name < points[j].Name
	})
	return points
}

func (m *Metrics) RejectedSeries() uint64 { return m.rejected.Load() }

func metricKey(name string, labels map[string]string) (string, map[string]string, error) {
	if len(name) == 0 || len(name) > 128 || strings.ContainsAny(name, "\x00\n=") || len(labels) > 12 {
		return "", nil, ErrInvalidMetric
	}
	copied := make(map[string]string, len(labels))
	for key, value := range labels {
		if len(key) == 0 || len(key) > 64 || len(value) > 128 || strings.ContainsAny(key+value, "\x00\n=") {
			return "", nil, ErrInvalidMetric
		}
		copied[key] = value
	}
	return name + "\x00" + labelsKey(copied), copied, nil
}

func labelsKey(labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var out strings.Builder
	for _, key := range keys {
		out.WriteString(key)
		out.WriteByte('=')
		out.WriteString(labels[key])
		out.WriteByte(0)
	}
	return out.String()
}
