package plugin

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	protocol "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

func TestServerLifecycleAndDispatch(t *testing.T) {
	t.Parallel()
	clientConn, serverConn := net.Pipe()
	server, err := NewServer(Config{
		ExpectedToken: "secret", Workers: 2, QueueSize: 4,
		WriteTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	var configured, started, stopped atomic.Bool
	handler := HandlerFuncs{
		MetadataFunc: func(context.Context) (protocol.PluginMetadata, error) {
			return testMetadata(), nil
		},
		ConfigureFunc: func(context.Context, []protocol.ConfigValue) error {
			configured.Store(true)
			return nil
		},
		StartFunc: func(context.Context) error {
			started.Store(true)
			return nil
		},
		StopFunc: func(context.Context) error {
			stopped.Store(true)
			return nil
		},
		DispatchFunc: func(_ context.Context, request protocol.DispatchRequest) (protocol.DispatchResponse, error) {
			return protocol.DispatchResponse{Actions: []protocol.Action{{
				ID: "reply-1", Kind: protocol.ActionReply,
				ChatID: request.Update.Message.ChatID, Text: request.Update.Message.Text,
			}}}, nil
		},
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(context.Background(), serverConn, handler) }()
	peer := newTestPeer(t, clientConn)
	peer.handshake("secret")
	peer.lifecycle(protocol.MethodConfigure, protocol.LifecycleConfigure)
	peer.lifecycle(protocol.MethodStart, protocol.LifecycleStart)

	response := peer.call(protocol.MethodDispatch, protocol.DispatchRequest{Update: protocol.Update{
		ID: "u1", Kind: protocol.UpdateMessage, OccurredUnixMS: time.Now().UnixMilli(),
		Message: &protocol.Message{ID: 5, ChatID: 9, Text: "hello"},
	}})
	if response.Error != nil {
		t.Fatalf("dispatch error: %v", response.Error)
	}
	var dispatch protocol.DispatchResponse
	if err := protocol.DecodePayload(response.Result, &dispatch); err != nil {
		t.Fatal(err)
	}
	if len(dispatch.Actions) != 1 || dispatch.Actions[0].Text != "hello" {
		t.Fatalf("dispatch result = %#v", dispatch)
	}
	peer.lifecycle(protocol.MethodStop, protocol.LifecycleStop)
	if err := <-serveDone; err != nil {
		t.Fatalf("Serve: %v", err)
	}
	if !configured.Load() || !started.Load() || !stopped.Load() {
		t.Fatalf("lifecycle calls: configure=%v start=%v stop=%v", configured.Load(), started.Load(), stopped.Load())
	}
}

func TestServerRejectsAuthentication(t *testing.T) {
	t.Parallel()
	clientConn, serverConn := net.Pipe()
	server, _ := NewServer(Config{ExpectedToken: "right", Workers: 1, QueueSize: 1})
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- server.Serve(context.Background(), serverConn, HandlerFuncs{
			MetadataFunc: func(context.Context) (protocol.PluginMetadata, error) { return testMetadata(), nil },
		})
	}()
	peer := newTestPeer(t, clientConn)
	response := peer.rawCall(protocol.MethodHandshake, protocol.HandshakeRequest{
		SupportedVersions: []string{protocol.Version}, AuthToken: "wrong",
		InstanceID: "test", Nonce: "nonce", MaxFrameBytes: protocol.DefaultMaxFrameSize,
	})
	if response.Error == nil || response.Error.Code != protocol.ErrorAuthentication {
		t.Fatalf("response error = %#v", response.Error)
	}
	if err := <-serveDone; err == nil {
		t.Fatal("Serve succeeded with wrong token")
	}
}

func TestServerBoundedQueueReturnsOverload(t *testing.T) {
	t.Parallel()
	clientConn, serverConn := net.Pipe()
	server, _ := NewServer(Config{ExpectedToken: "token", Workers: 1, QueueSize: 1, WriteTimeout: time.Second})
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	handler := HandlerFuncs{
		MetadataFunc: func(context.Context) (protocol.PluginMetadata, error) { return testMetadata(), nil },
		DispatchFunc: func(context.Context, protocol.DispatchRequest) (protocol.DispatchResponse, error) {
			entered <- struct{}{}
			<-release
			return protocol.DispatchResponse{}, nil
		},
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(context.Background(), serverConn, handler) }()
	peer := newTestPeer(t, clientConn)
	peer.handshake("token")
	peer.lifecycle(protocol.MethodConfigure, protocol.LifecycleConfigure)
	peer.lifecycle(protocol.MethodStart, protocol.LifecycleStart)

	dispatch := protocol.DispatchRequest{Update: protocol.Update{
		ID: "u", Kind: protocol.UpdateMessage,
		Message: &protocol.Message{ID: 1, ChatID: 1, Text: "x"},
	}}
	peer.write(protocol.MethodDispatch, dispatch)
	<-entered
	peer.write(protocol.MethodDispatch, dispatch)
	peer.write(protocol.MethodDispatch, dispatch)
	response := peer.read()
	if response.Error == nil || response.Error.Code != protocol.ErrorOverloaded {
		t.Fatalf("first available response = %#v, want overload", response)
	}
	close(release)
	_ = peer.read()
	_ = peer.read()
	peer.lifecycle(protocol.MethodStop, protocol.LifecycleStop)
	if err := <-serveDone; err != nil {
		t.Fatal(err)
	}
}

func TestServerRecoversHandlerPanic(t *testing.T) {
	t.Parallel()
	clientConn, serverConn := net.Pipe()
	server, _ := NewServer(Config{ExpectedToken: "token", Workers: 1, QueueSize: 1})
	handler := HandlerFuncs{
		MetadataFunc: func(context.Context) (protocol.PluginMetadata, error) { return testMetadata(), nil },
		DispatchFunc: func(context.Context, protocol.DispatchRequest) (protocol.DispatchResponse, error) {
			panic("fixture panic")
		},
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(context.Background(), serverConn, handler) }()
	peer := newTestPeer(t, clientConn)
	peer.handshake("token")
	peer.lifecycle(protocol.MethodConfigure, protocol.LifecycleConfigure)
	peer.lifecycle(protocol.MethodStart, protocol.LifecycleStart)
	response := peer.call(protocol.MethodDispatch, protocol.DispatchRequest{Update: protocol.Update{
		ID: "panic", Kind: protocol.UpdateMessage, Message: &protocol.Message{ID: 1, ChatID: 1},
	}})
	if response.Error == nil || response.Error.Code != protocol.ErrorPlugin {
		t.Fatalf("panic response = %#v", response)
	}
	health := peer.call(protocol.MethodHealth, protocol.HealthRequest{})
	if health.Error != nil {
		t.Fatalf("server did not recover: %v", health.Error)
	}
	peer.lifecycle(protocol.MethodStop, protocol.LifecycleStop)
	if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

type testPeer struct {
	t      *testing.T
	conn   net.Conn
	nextID uint64
}

func newTestPeer(t *testing.T, conn net.Conn) *testPeer {
	t.Helper()
	t.Cleanup(func() { _ = conn.Close() })
	return &testPeer{t: t, conn: conn}
}

func (p *testPeer) handshake(token string) {
	p.t.Helper()
	response := p.rawCall(protocol.MethodHandshake, protocol.HandshakeRequest{
		SupportedVersions: []string{protocol.Version}, AuthToken: token,
		InstanceID: "test", Nonce: "nonce", MaxFrameBytes: protocol.DefaultMaxFrameSize,
	})
	if response.Error != nil {
		p.t.Fatalf("handshake: %v", response.Error)
	}
	var handshake protocol.HandshakeResponse
	if err := protocol.DecodePayload(response.Result, &handshake); err != nil {
		p.t.Fatal(err)
	}
	if handshake.Nonce != "nonce" || handshake.SelectedVersion != protocol.Version {
		p.t.Fatalf("handshake response = %#v", handshake)
	}
}

func (p *testPeer) lifecycle(method protocol.Method, phase protocol.LifecyclePhase) {
	p.t.Helper()
	response := p.rawCall(method, protocol.LifecycleRequest{Phase: phase})
	if response.Error != nil {
		p.t.Fatalf("%s: %v", method, response.Error)
	}
}

func (p *testPeer) call(method protocol.Method, input any) protocol.Response {
	p.t.Helper()
	p.write(method, input)
	return p.read()
}

func (p *testPeer) rawCall(method protocol.Method, input any) protocol.Response {
	p.t.Helper()
	return p.call(method, input)
}

func (p *testPeer) write(method protocol.Method, input any) {
	p.t.Helper()
	p.nextID++
	payload, err := protocol.MarshalPayload(input)
	if err != nil {
		p.t.Fatal(err)
	}
	request := protocol.Request{
		Protocol: protocol.Protocol, RequestID: p.nextID, Kind: protocol.KindRequest,
		DeadlineUnixMS: time.Now().Add(2 * time.Second).UnixMilli(), Method: method, Payload: payload,
	}
	if err := protocol.WriteFrame(p.conn, request, protocol.DefaultMaxFrameSize); err != nil {
		p.t.Fatal(err)
	}
}

func (p *testPeer) read() protocol.Response {
	p.t.Helper()
	var response protocol.Response
	if err := protocol.ReadFrame(p.conn, &response, protocol.DefaultMaxFrameSize); err != nil {
		p.t.Fatal(err)
	}
	return response
}

func testMetadata() protocol.PluginMetadata {
	return protocol.PluginMetadata{
		ID: "test", Name: "Test", Version: "1.0.0", ProtocolVersion: protocol.Version,
		Capabilities: []protocol.Capability{protocol.CapabilityCommands},
		Commands:     []protocol.CommandSpec{{Name: "test"}},
	}
}
