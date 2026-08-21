package plugin

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Listener is a context-aware local transport listener.
type Listener interface {
	Accept(context.Context) (net.Conn, error)
	Endpoint() string
	Close() error
}

// Transport abstracts the local IPC mechanism. A named-pipe implementation can
// replace UnixTransport without changing supervision or RPC code.
type Transport interface {
	Listen(context.Context, string) (Listener, error)
	Dial(context.Context, string) (net.Conn, error)
}

type UnixTransport struct {
	SocketMode os.FileMode
	PollPeriod time.Duration
}

func (t UnixTransport) Listen(ctx context.Context, endpoint string) (Listener, error) {
	if endpoint == "" {
		return nil, fmt.Errorf("empty Unix socket endpoint")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := ensureSocketEndpointAvailable(endpoint); err != nil {
		return nil, err
	}
	listener, err := net.Listen("unix", endpoint)
	if err != nil {
		return nil, fmt.Errorf("listen on Unix socket %q: %w", endpoint, err)
	}
	mode := t.SocketMode
	if mode == 0 {
		mode = 0o600
	}
	if err := os.Chmod(endpoint, mode); err != nil {
		_ = listener.Close()
		_ = os.Remove(endpoint)
		return nil, fmt.Errorf("set Unix socket permissions: %w", err)
	}
	poll := t.PollPeriod
	if poll <= 0 {
		poll = 100 * time.Millisecond
	}
	return &unixListener{listener: listener, endpoint: endpoint, poll: poll}, nil
}

func (UnixTransport) Dial(ctx context.Context, endpoint string) (net.Conn, error) {
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", endpoint)
	if err != nil {
		return nil, fmt.Errorf("dial Unix socket %q: %w", endpoint, err)
	}
	return conn, nil
}

type unixListener struct {
	listener net.Listener
	endpoint string
	poll     time.Duration
	once     sync.Once
	err      error
}

func (l *unixListener) Endpoint() string { return l.endpoint }

func (l *unixListener) Accept(ctx context.Context) (net.Conn, error) {
	unix, ok := l.listener.(*net.UnixListener)
	if !ok {
		return nil, fmt.Errorf("listener is not a Unix listener")
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := unix.SetDeadline(time.Now().Add(l.poll)); err != nil {
			return nil, fmt.Errorf("set Unix accept deadline: %w", err)
		}
		conn, err := unix.Accept()
		if err == nil {
			_ = unix.SetDeadline(time.Time{})
			return conn, nil
		}
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			continue
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("accept Unix connection: %w", err)
	}
}

func (l *unixListener) Close() error {
	l.once.Do(func() {
		closeErr := l.listener.Close()
		removeErr := os.Remove(l.endpoint)
		if removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			l.err = removeErr
		} else {
			l.err = closeErr
		}
	})
	return l.err
}

func ensureSocketEndpointAvailable(endpoint string) error {
	if err := os.MkdirAll(filepath.Dir(endpoint), 0o700); err != nil {
		return fmt.Errorf("create Unix socket directory: %w", err)
	}
	info, err := os.Lstat(endpoint)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect Unix socket endpoint: %w", err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("Unix socket endpoint exists and is not a socket: %q", endpoint)
	}
	if err := os.Remove(endpoint); err != nil {
		return fmt.Errorf("remove stale Unix socket: %w", err)
	}
	return nil
}
