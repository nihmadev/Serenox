package media

import (
	"time"

	"github.com/nihmadev/Serenox/internal/observability"
)

// Metrics records only a fixed vocabulary and bounded owner labels. Handles,
// filenames, captions, and content are intentionally never labels.
type Metrics struct{ registry *observability.Metrics }

func NewMetrics(registry *observability.Metrics) *Metrics { return &Metrics{registry: registry} }

func (m *Metrics) Add(name, pluginID string, value int64) {
	if m == nil || m.registry == nil {
		return
	}
	allowed := map[string]struct{}{"active_transfers": {}, "bytes_read": {}, "bytes_written": {}, "cancellations": {}, "timeouts": {}, "checksum_failures": {}, "rejected_bytes": {}, "quota_violations": {}, "adapter_failures": {}, "cleanup_failures": {}}
	if _, ok := allowed[name]; !ok {
		return
	}
	_ = m.registry.Add("media_"+name, map[string]string{"plugin": boundedLabel(pluginID)}, value)
}

func (m *Metrics) ObserveDuration(pluginID string, duration time.Duration, bytes int64) {
	if duration <= 0 || bytes < 0 {
		return
	}
	// Throughput is deliberately a bounded counter, not a label/cardinality
	// surface. Callers can derive rates from bytes and duration.
	m.Add("bytes_read", pluginID, bytes)
}

func boundedLabel(value string) string {
	if len(value) > 64 {
		return value[:64]
	}
	return value
}
