package plugin

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	protocol "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

type backgroundTestHandler struct {
	HandlerFuncs
	run func(context.Context) error
}

func (h backgroundTestHandler) Background(ctx context.Context) error { return h.run(ctx) }

func TestServerBackgroundRunsWithServicesUntilStop(t *testing.T) {
	t.Parallel()
	clientConn, serverConn := net.Pipe()
	server, err := NewServer(Config{ExpectedToken: "secret", Workers: 1, QueueSize: 1, WriteTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan bool, 1)
	cancelled := make(chan struct{})
	handler := backgroundTestHandler{
		HandlerFuncs: HandlerFuncs{MetadataFunc: func(context.Context) (protocol.PluginMetadata, error) { return testMetadata(), nil }},
		run: func(ctx context.Context) error {
			services, ok := ServicesFromContext(ctx)
			started <- ok && services.Actions != nil && services.Storage != nil
			<-ctx.Done()
			close(cancelled)
			return ctx.Err()
		},
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(context.Background(), serverConn, handler) }()
	peer := newTestPeer(t, clientConn)
	peer.handshake("secret")
	peer.lifecycle(protocol.MethodConfigure, protocol.LifecycleConfigure)
	peer.lifecycle(protocol.MethodStart, protocol.LifecycleStart)
	select {
	case ok := <-started:
		if !ok {
			t.Fatal("background context is missing host services")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("background handler did not start")
	}
	peer.lifecycle(protocol.MethodStop, protocol.LifecycleStop)
	select {
	case <-cancelled:
	default:
		t.Fatal("background context was not cancelled before Stop returned")
	}
	if err := <-serveDone; err != nil {
		t.Fatalf("Serve: %v", err)
	}
}

func TestServerBackgroundFailureClosesConnection(t *testing.T) {
	t.Parallel()
	clientConn, serverConn := net.Pipe()
	server, _ := NewServer(Config{ExpectedToken: "secret", Workers: 1, QueueSize: 1, WriteTimeout: time.Second})
	handler := backgroundTestHandler{
		HandlerFuncs: HandlerFuncs{MetadataFunc: func(context.Context) (protocol.PluginMetadata, error) { return testMetadata(), nil }},
		run:          func(context.Context) error { return errors.New("bus lost") },
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(context.Background(), serverConn, handler) }()
	peer := newTestPeer(t, clientConn)
	peer.handshake("secret")
	peer.lifecycle(protocol.MethodConfigure, protocol.LifecycleConfigure)
	peer.lifecycle(protocol.MethodStart, protocol.LifecycleStart)
	select {
	case err := <-serveDone:
		if err == nil || !strings.Contains(err.Error(), "bus lost") {
			t.Fatalf("Serve error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("background failure did not close the connection")
	}
}
