package plugin

import (
	"bytes"
	"fmt"
	"sync"
	"testing"
)

func TestCappedBufferKeepsNewestBytes(t *testing.T) {
	t.Parallel()
	buffer := newCappedBuffer(8)
	for _, value := range []string{"abc", "defg", "hijkl"} {
		if written, err := buffer.Write([]byte(value)); err != nil || written != len(value) {
			t.Fatalf("Write(%q) = %d, %v", value, written, err)
		}
	}
	got, dropped := buffer.snapshot()
	if string(got) != "efghijkl" || dropped != 4 {
		t.Fatalf("snapshot = %q dropped=%d, want %q dropped=4", got, dropped, "efghijkl")
	}
}

func TestCappedBufferConcurrentWritesRemainBounded(t *testing.T) {
	t.Parallel()
	const limit = 1024
	buffer := newCappedBuffer(limit)
	var workers sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		workers.Add(1)
		go func(id int) {
			defer workers.Done()
			payload := bytes.Repeat([]byte(fmt.Sprintf("%d", id)), 64)
			for iteration := 0; iteration < 100; iteration++ {
				_, _ = buffer.Write(payload)
			}
		}(worker)
	}
	workers.Wait()
	got, dropped := buffer.snapshot()
	if len(got) != limit {
		t.Fatalf("retained bytes = %d, want %d", len(got), limit)
	}
	if dropped != 8*100*64-limit {
		t.Fatalf("dropped bytes = %d", dropped)
	}
}
