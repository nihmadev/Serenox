package plugin

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// requireUnixSockets distinguishes a restricted test sandbox from a product
// failure. On ordinary Linux/macOS CI this probe succeeds and every transport
// and process-supervision assertion still runs. Some seccomp profiles reject
// the socket option used by Go's Unix listener with EPERM; those environments
// cannot exercise local IPC at all.
func requireUnixSockets(t *testing.T) {
	t.Helper()
	endpoint := filepath.Join(t.TempDir(), "probe.sock")
	listener, err := net.Listen("unix", endpoint)
	if errors.Is(err, syscall.EPERM) {
		t.Skip("Unix-domain sockets are blocked by the test sandbox (EPERM)")
	}
	if err != nil {
		t.Fatalf("probe Unix-domain sockets: %v", err)
	}
	if err := listener.Close(); err != nil {
		t.Fatalf("close Unix-domain socket probe: %v", err)
	}
	if err := os.Remove(endpoint); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("remove Unix-domain socket probe: %v", err)
	}
}
