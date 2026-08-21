package plugin

import "sync"

// cappedBuffer is a concurrency-safe byte ring. It implements io.Writer so it
// can be attached directly to exec.Cmd without creating a package-owned copy
// goroutine. Only the newest limit bytes are retained.
type cappedBuffer struct {
	mu      sync.Mutex
	buf     []byte
	start   int
	size    int
	dropped uint64
}

func newCappedBuffer(limit int) *cappedBuffer {
	return &cappedBuffer{buf: make([]byte, limit)}
}

func (b *cappedBuffer) Write(payload []byte) (int, error) {
	written := len(payload)
	if written == 0 {
		return 0, nil
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	capacity := len(b.buf)
	if written >= capacity {
		b.dropped += uint64(b.size + written - capacity)
		copy(b.buf, payload[written-capacity:])
		b.start = 0
		b.size = capacity
		return written, nil
	}

	overflow := b.size + written - capacity
	if overflow > 0 {
		b.start = (b.start + overflow) % capacity
		b.size -= overflow
		b.dropped += uint64(overflow)
	}

	end := (b.start + b.size) % capacity
	first := min(written, capacity-end)
	copy(b.buf[end:end+first], payload[:first])
	copy(b.buf[:written-first], payload[first:])
	b.size += written
	return written, nil
}

func (b *cappedBuffer) snapshot() ([]byte, uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()

	result := make([]byte, b.size)
	if b.size == 0 {
		return result, b.dropped
	}
	first := min(b.size, len(b.buf)-b.start)
	copy(result, b.buf[b.start:b.start+first])
	copy(result[first:], b.buf[:b.size-first])
	return result, b.dropped
}

// OutputSnapshot is a bounded copy of the most recent child-process output.
// Dropped counters are cumulative across supervised restarts.
type OutputSnapshot struct {
	Stdout        []byte
	Stderr        []byte
	StdoutDropped uint64
	StderrDropped uint64
}
