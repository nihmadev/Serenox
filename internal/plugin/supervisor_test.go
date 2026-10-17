package plugin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	protocol "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
	sdk "github.com/nihmadev/Serenox/sdk/go"
)

const (
	helperEnabled = "SERENOX_SUPERVISOR_HELPER"
	helperMode    = "SERENOX_SUPERVISOR_HELPER_MODE"
	helperState   = "SERENOX_SUPERVISOR_HELPER_STATE"
)

func TestSupervisorLifecycleDispatchAndCleanup(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	supervisor := newHelperSupervisor(t, base, "normal", RestartPolicy{Disabled: true})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := supervisor.Start(ctx, []protocol.ConfigValue{{
		Key: "message", Type: protocol.ConfigString, Value: []byte(`"configured"`),
	}}); err != nil {
		t.Fatalf("Start: %v\nstdout=%s\nstderr=%s", err, supervisor.Stdout(), supervisor.Stderr())
	}

	status := supervisor.Status()
	if status.State != SupervisorReady || status.Circuit != CircuitClosed || status.PID <= 0 {
		t.Fatalf("ready status = %#v", status)
	}
	if status.Metadata.ID != "supervisor-helper" {
		t.Fatalf("metadata = %#v", status.Metadata)
	}
	if _, err := os.Stat(status.RuntimeDir); err != nil {
		t.Fatalf("runtime directory is missing while running: %v", err)
	}
	if _, err := os.Stat(status.Endpoint); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("listener socket remains after authenticated accept: %v", err)
	}

	request := helperDispatch("hello")
	response, err := supervisor.Dispatch(context.Background(), request)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if len(response.Actions) != 1 || response.Actions[0].Text != "hello" {
		t.Fatalf("dispatch response = %#v", response)
	}
	health, err := supervisor.Health(context.Background(), true)
	if err != nil || !health.Ready {
		t.Fatalf("Health = %#v, %v", health, err)
	}

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer stopCancel()
	if err := supervisor.Stop(stopCtx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	assertSupervisorClean(t, base, status.RuntimeDir, status.Endpoint, status.PID)
	if got := supervisor.Status().State; got != SupervisorStopped {
		t.Fatalf("state after Stop = %s", got)
	}
}

func TestSupervisorRestartsCrashedProcessWithoutReplayingDispatch(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	stateFile := filepath.Join(t.TempDir(), "starts")
	supervisor := newHelperSupervisorWithEnv(t, base, "crash-once", RestartPolicy{
		MaxRestarts: 3, Window: time.Minute, InitialBackoff: 10 * time.Millisecond,
		MaxBackoff: 20 * time.Millisecond, DisableJitter: true,
	}, []string{helperState + "=" + stateFile})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := supervisor.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	firstPID := supervisor.Status().PID
	if _, err := supervisor.Dispatch(context.Background(), helperDispatch("crash")); err == nil {
		t.Fatal("dispatch unexpectedly survived child crash")
	}

	waitFor(t, 4*time.Second, func() bool {
		status := supervisor.Status()
		return status.State == SupervisorReady && status.Generation >= 2 && status.PID != firstPID
	})
	status := supervisor.Status()
	if status.Restarts != 1 || status.Circuit != CircuitClosed {
		t.Fatalf("restart status = %#v", status)
	}
	response, err := supervisor.Dispatch(context.Background(), helperDispatch("after-restart"))
	if err != nil {
		t.Fatalf("Dispatch after restart: %v", err)
	}
	if len(response.Actions) != 1 || response.Actions[0].Text != "after-restart" {
		t.Fatalf("response after restart = %#v", response)
	}
	starts, err := os.ReadFile(stateFile)
	if err != nil || strings.TrimSpace(string(starts)) != "2" {
		t.Fatalf("generation fixture count = %q, %v", starts, err)
	}
	if err := supervisor.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertRuntimeBaseEmpty(t, base)
}

func TestSupervisorCircuitOpensAfterFiniteRestartWindow(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	supervisor := newHelperSupervisor(t, base, "exit-immediately", RestartPolicy{
		MaxRestarts: 2, Window: time.Minute, InitialBackoff: 5 * time.Millisecond,
		MaxBackoff: 10 * time.Millisecond, DisableJitter: true,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := supervisor.Start(ctx)
	if !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("Start error = %v, want ErrCircuitOpen", err)
	}
	status := supervisor.Status()
	if status.State != SupervisorCircuitOpen || status.Circuit != CircuitOpen || status.Restarts != 2 || status.Generation != 3 {
		t.Fatalf("circuit status = %#v", status)
	}
	if _, err := supervisor.Dispatch(context.Background(), helperDispatch("blocked")); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("Dispatch error = %v, want ErrCircuitOpen", err)
	}
	assertRuntimeBaseEmpty(t, base)
}

func TestSupervisorRejectsMalformedHandshakeAndCleansRuntime(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	supervisor := newHelperSupervisor(t, base, "malformed", RestartPolicy{Disabled: true})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := supervisor.Start(ctx)
	if !errors.Is(err, ErrCircuitOpen) || !strings.Contains(err.Error(), "malformed") {
		t.Fatalf("Start error = %v", err)
	}
	assertRuntimeBaseEmpty(t, base)
}

func TestSupervisorBoundsLifecycleTimeoutAndCleansRuntime(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	supervisor := newHelperSupervisor(t, base, "configure-timeout", RestartPolicy{Disabled: true})
	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := supervisor.Start(ctx)
	if !errors.Is(err, ErrCircuitOpen) || !errors.Is(err, ErrRPCTimeout) {
		t.Fatalf("Start error = %v, want circuit RPC timeout", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("configured 80ms timeout took %s", elapsed)
	}
	assertRuntimeBaseEmpty(t, base)
}

func TestSupervisorStopDuringHandshakeJoinsOwnedWork(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	supervisor := newHelperSupervisor(t, base, "idle-handshake", RestartPolicy{Disabled: true})
	lifetime, cancel := context.WithCancel(context.Background())
	defer cancel()
	startResult := make(chan error, 1)
	go func() { startResult <- supervisor.Start(lifetime) }()
	waitFor(t, time.Second, func() bool {
		entries, err := os.ReadDir(base)
		return err == nil && len(entries) != 0
	})
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer stopCancel()
	if err := supervisor.Stop(stopCtx); err != nil {
		t.Fatalf("Stop during handshake: %v", err)
	}
	if err := <-startResult; !errors.Is(err, ErrSupervisorStopped) {
		t.Fatalf("concurrent Start error = %v, want ErrSupervisorStopped", err)
	}
	assertRuntimeBaseEmpty(t, base)
}

func TestSupervisorCapsProcessOutputAcrossLifecycle(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	supervisor := newHelperSupervisor(t, base, "large-output", RestartPolicy{Disabled: true})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := supervisor.Start(ctx); err != nil {
		t.Fatal(err)
	}
	output := supervisor.Output()
	if len(output.Stdout) > 1024 || len(output.Stderr) > 1024 {
		t.Fatalf("output exceeded cap: stdout=%d stderr=%d", len(output.Stdout), len(output.Stderr))
	}
	if output.StdoutDropped == 0 || output.StderrDropped == 0 {
		t.Fatalf("output did not report truncation: %#v", output)
	}
	if err := supervisor.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSupervisorKillsRemainingProcessGroup(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	childPIDFile := filepath.Join(t.TempDir(), "child-pid")
	supervisor := newHelperSupervisorWithEnv(t, base, "descendant", RestartPolicy{Disabled: true}, []string{
		helperState + "=" + childPIDFile,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := supervisor.Start(ctx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, time.Second, func() bool {
		_, err := os.Stat(childPIDFile)
		return err == nil
	})
	contents, err := os.ReadFile(childPIDFile)
	if err != nil {
		t.Fatal(err)
	}
	childPID, err := strconv.Atoi(strings.TrimSpace(string(contents)))
	if err != nil || childPID <= 0 {
		t.Fatalf("child PID = %q, %v", contents, err)
	}
	if err := supervisor.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, func() bool { return !testProcessExists(childPID) })
	assertRuntimeBaseEmpty(t, base)
}

func newHelperSupervisor(t *testing.T, base, mode string, restart RestartPolicy) *Supervisor {
	t.Helper()
	return newHelperSupervisorWithEnv(t, base, mode, restart, nil)
}

func newHelperSupervisorWithEnv(t *testing.T, base, mode string, restart RestartPolicy, extra []string) *Supervisor {
	t.Helper()
	requireUnixSockets(t)
	environment := []string{helperEnabled + "=1", helperMode + "=" + mode}
	environment = append(environment, extra...)
	supervisor, err := NewSupervisor(SupervisorConfig{
		Command: os.Args[0], Args: []string{"-test.run=^TestSupervisorHelperProcess$"}, Env: environment,
		InstanceID: "supervisor-test", ExpectedPluginID: "supervisor-helper", RuntimeBaseDir: base,
		HandshakeTimeout: 300 * time.Millisecond, ConfigureTimeout: 80 * time.Millisecond,
		StartTimeout: 300 * time.Millisecond, HealthTimeout: 150 * time.Millisecond,
		DispatchTimeout: 300 * time.Millisecond, StopTimeout: 300 * time.Millisecond,
		TerminateTimeout: 300 * time.Millisecond, DisableHealth: true,
		OutputLimitBytes: 1024, Restart: restart,
	})
	if err != nil {
		t.Fatal(err)
	}
	return supervisor
}

func helperDispatch(text string) protocol.DispatchRequest {
	return protocol.DispatchRequest{Update: protocol.Update{
		ID: "update-" + text, Kind: protocol.UpdateMessage, OccurredUnixMS: time.Now().UnixMilli(),
		Message: &protocol.Message{ID: 1, ChatID: 2, SenderID: 3, Text: text},
	}}
}

func assertSupervisorClean(t *testing.T, base, runtimeDir, endpoint string, pid int) {
	t.Helper()
	if _, err := os.Stat(runtimeDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("runtime directory remains: %v", err)
	}
	if _, err := os.Stat(endpoint); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket remains: %v", err)
	}
	waitFor(t, 2*time.Second, func() bool { return !testProcessExists(pid) })
	assertRuntimeBaseEmpty(t, base)
}

func assertRuntimeBaseEmpty(t *testing.T, base string) {
	t.Helper()
	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("runtime base contains %v", entries)
	}
}

func waitFor(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition was not met before timeout")
}

// TestSupervisorHelperProcess is re-executed as the supervised process. It is
// intentionally the only selected test in that child.
func TestSupervisorHelperProcess(t *testing.T) {
	if os.Getenv(helperEnabled) != "1" {
		return
	}
	mode := os.Getenv(helperMode)
	switch mode {
	case "exit-immediately":
		os.Exit(21)
	case "malformed":
		runMalformedHelper()
		return
	case "idle-handshake":
		runIdleHandshakeHelper()
		return
	case "large-output":
		_, _ = fmt.Fprint(os.Stdout, strings.Repeat("o", 4096))
		_, _ = fmt.Fprint(os.Stderr, strings.Repeat("e", 4096))
	}

	generation := incrementHelperGeneration(os.Getenv(helperState))
	handler := sdk.HandlerFuncs{
		MetadataFunc: func(context.Context) (protocol.PluginMetadata, error) {
			return protocol.PluginMetadata{
				ID: "supervisor-helper", Name: "Supervisor helper", Version: "1.0.0",
				ProtocolVersion: protocol.Version, Capabilities: []protocol.Capability{protocol.CapabilityCommands},
				Commands: []protocol.CommandSpec{{Name: "helper"}},
			}, nil
		},
		ConfigureFunc: func(ctx context.Context, _ []protocol.ConfigValue) error {
			if mode == "configure-timeout" {
				<-ctx.Done()
				return ctx.Err()
			}
			return nil
		},
		StartFunc: func(context.Context) error {
			if mode == "descendant" {
				command := exec.Command("sh", "-c", "trap '' TERM; while :; do sleep 1; done")
				if err := command.Start(); err != nil {
					return err
				}
				return os.WriteFile(os.Getenv(helperState), []byte(strconv.Itoa(command.Process.Pid)), 0o600)
			}
			return nil
		},
		DispatchFunc: func(_ context.Context, request protocol.DispatchRequest) (protocol.DispatchResponse, error) {
			if mode == "crash-once" && generation == 1 {
				os.Exit(22)
			}
			return protocol.DispatchResponse{Actions: []protocol.Action{{
				ID: "reply", Kind: protocol.ActionReply, ChatID: request.Update.Message.ChatID,
				Text: request.Update.Message.Text,
			}}}, nil
		},
		HealthFunc: func(context.Context, protocol.HealthRequest) (protocol.HealthResponse, error) {
			return protocol.HealthResponse{Ready: true, Status: "ok"}, nil
		},
	}
	if err := sdk.Run(context.Background(), handler, sdk.Config{Workers: 1, QueueSize: 4}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(23)
	}
}

func incrementHelperGeneration(path string) int {
	if path == "" {
		return 1
	}
	contents, _ := os.ReadFile(path)
	generation, _ := strconv.Atoi(strings.TrimSpace(string(contents)))
	generation++
	_ = os.WriteFile(path, []byte(strconv.Itoa(generation)), 0o600)
	return generation
}

func runMalformedHelper() {
	connection, err := net.DialTimeout("unix", os.Getenv(envSocket), time.Second)
	if err != nil {
		os.Exit(31)
	}
	defer connection.Close()
	var request protocol.Request
	if protocol.ReadFrame(connection, &request, protocol.DefaultMaxFrameSize) != nil {
		os.Exit(32)
	}
	_, _ = connection.Write([]byte{0, 0, 0, 1, '{'})
	_, _ = io.Copy(io.Discard, connection)
}

func runIdleHandshakeHelper() {
	connection, err := net.DialTimeout("unix", os.Getenv(envSocket), time.Second)
	if err != nil {
		os.Exit(33)
	}
	defer connection.Close()
	_, _ = io.Copy(io.Discard, connection)
}
