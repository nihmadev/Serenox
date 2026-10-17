package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

type recordingLoader struct {
	mu       sync.Mutex
	packages []string
}

func (l *recordingLoader) Load(_ context.Context, packagePath string) error {
	l.mu.Lock()
	l.packages = append(l.packages, packagePath)
	l.mu.Unlock()
	return nil
}

func (l *recordingLoader) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.packages...)
}

func TestControlServerHotLoadsThroughAuthenticatedSocket(t *testing.T) {
	root := t.TempDir()
	loader := &recordingLoader{}
	ready := make(chan struct{})
	server, err := NewControlServer(root, loader, ready)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close(context.Background()) })

	loadDone := make(chan error, 1)
	go func() { loadDone <- LoadPlugin(context.Background(), root, "/tmp/example.srnx") }()
	select {
	case <-time.After(100 * time.Millisecond):
		if got := loader.snapshot(); len(got) != 0 {
			t.Fatalf("plugin loaded before daemon readiness: %v", got)
		}
	case err := <-loadDone:
		t.Fatalf("hot-load returned before readiness: %v", err)
	}
	close(ready)
	select {
	case err := <-loadDone:
		if err != nil {
			t.Fatalf("hot-load: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for hot-load")
	}
	if got := loader.snapshot(); len(got) != 1 || got[0] != "/tmp/example.srnx" {
		t.Fatalf("loaded packages = %v", got)
	}
}

func TestControlServerRejectsInvalidToken(t *testing.T) {
	root := t.TempDir()
	server, err := NewControlServer(root, &recordingLoader{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close(context.Background()) })

	connection, err := net.Dial("unix", ControlSocketPath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if err := json.NewEncoder(connection).Encode(controlRequest{
		Version: controlVersion, Operation: "load", Token: strings.Repeat("0", 64), PackagePath: "/tmp/example.srnx",
	}); err != nil {
		t.Fatal(err)
	}
	var response controlResponse
	if err := json.NewDecoder(bufio.NewReader(connection)).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.OK || !strings.Contains(response.Error, "not authorized") {
		t.Fatalf("invalid-token response = %#v", response)
	}
}

func TestControlBotRuntimeStatusUsesAuthenticatedSocket(t *testing.T) {
	root := t.TempDir()
	server, err := NewControlServer(root, &recordingLoader{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	server.SetControlBotStatus(func() ControlBotStatus {
		return ControlBotStatus{Attached: true, Running: true, BotID: 42, Username: "control_bot", SavedOffset: 17, LastError: "update", RestartCount: 2}
	})
	if err := server.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close(context.Background()) })
	status, err := ControlBotRuntimeStatus(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Attached || !status.Running || status.BotID != 42 || status.SavedOffset != 17 || status.LastError != "update" {
		t.Fatalf("status = %#v", status)
	}
}

func TestControlServerProtectsEndpointAndRotatesToken(t *testing.T) {
	root := t.TempDir()
	server, err := NewControlServer(root, &recordingLoader{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	mode, err := os.Stat(ControlSocketPath(root))
	if err != nil {
		t.Fatal(err)
	}
	if mode.Mode().Perm() != 0o600 {
		t.Fatalf("control socket mode = %o, want 600", mode.Mode().Perm())
	}
	token, err := os.ReadFile(ControlTokenPath(root))
	if err != nil {
		t.Fatal(err)
	}
	if len(strings.TrimSpace(string(token))) != 64 {
		t.Fatalf("unexpected token length: %q", token)
	}
	if err := server.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(ControlSocketPath(root)); !os.IsNotExist(err) {
		t.Fatalf("control socket remains after close: %v", err)
	}
	if _, err := os.Stat(ControlTokenPath(root)); !os.IsNotExist(err) {
		t.Fatalf("control token remains after close: %v", err)
	}
}
