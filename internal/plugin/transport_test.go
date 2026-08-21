package plugin

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestUnixTransportRoundTripAndCleanup(t *testing.T) {
	t.Parallel()
	requireUnixSockets(t)
	dir := t.TempDir()
	endpoint := filepath.Join(dir, "plugin.sock")
	transport := UnixTransport{PollPeriod: 5 * time.Millisecond}
	listener, err := transport.Listen(context.Background(), endpoint)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	acceptCh := make(chan error, 1)
	go func() {
		conn, err := listener.Accept(context.Background())
		if err == nil {
			_ = conn.Close()
		}
		acceptCh <- err
	}()
	conn, err := transport.Dial(context.Background(), endpoint)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	_ = conn.Close()
	if err := <-acceptCh; err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if err := listener.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(endpoint); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket remains after close: %v", err)
	}
}

func TestUnixTransportAcceptCancellationCleansSocket(t *testing.T) {
	t.Parallel()
	requireUnixSockets(t)
	endpoint := filepath.Join(t.TempDir(), "cancel.sock")
	transport := UnixTransport{PollPeriod: 5 * time.Millisecond}
	listener, err := transport.Listen(context.Background(), endpoint)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Millisecond)
	defer cancel()
	if _, err := listener.Accept(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Accept error = %v", err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(endpoint); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket remains: %v", err)
	}
}

func TestUnixTransportDoesNotReplaceRegularFile(t *testing.T) {
	t.Parallel()
	endpoint := filepath.Join(t.TempDir(), "not-a-socket")
	if err := os.WriteFile(endpoint, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (UnixTransport{}).Listen(context.Background(), endpoint); err == nil {
		t.Fatal("Listen replaced a regular file")
	}
	got, err := os.ReadFile(endpoint)
	if err != nil || string(got) != "keep" {
		t.Fatalf("regular file changed: %q, %v", got, err)
	}
}
