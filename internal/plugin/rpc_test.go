package plugin

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	protocol "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

func TestRPCClientRoundTrip(t *testing.T) {
	t.Parallel()
	clientConn, serverConn := net.Pipe()
	client, err := NewRPCClient(clientConn, RPCClientConfig{MaxInFlight: 2, DefaultTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	defer serverConn.Close()

	serverDone := make(chan error, 1)
	go func() {
		var request protocol.Request
		if err := protocol.ReadFrame(serverConn, &request, protocol.DefaultMaxFrameSize); err != nil {
			serverDone <- err
			return
		}
		payload, _ := protocol.MarshalPayload(protocol.HealthResponse{Ready: true, Status: "ok"})
		serverDone <- protocol.WriteFrame(serverConn, protocol.Response{
			Protocol: protocol.Protocol, RequestID: request.RequestID,
			Kind: protocol.KindResponse, DeadlineUnixMS: request.DeadlineUnixMS, Result: payload,
		}, protocol.DefaultMaxFrameSize)
	}()

	var response protocol.HealthResponse
	if err := client.Call(context.Background(), protocol.MethodHealth, protocol.HealthRequest{}, &response); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if !response.Ready || response.Status != "ok" {
		t.Fatalf("response = %#v", response)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("server: %v", err)
	}
}

func TestRPCClientTimeoutRemovesPending(t *testing.T) {
	t.Parallel()
	clientConn, serverConn := net.Pipe()
	client, err := NewRPCClient(clientConn, RPCClientConfig{MaxInFlight: 1, DefaultTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	defer serverConn.Close()

	read := make(chan struct{})
	go func() {
		var request protocol.Request
		_ = protocol.ReadFrame(serverConn, &request, protocol.DefaultMaxFrameSize)
		close(read)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	err = client.Call(ctx, protocol.MethodHealth, protocol.HealthRequest{}, nil)
	if !errors.Is(err, ErrRPCTimeout) {
		t.Fatalf("Call error = %v, want ErrRPCTimeout", err)
	}
	<-read
	if got := client.Pending(); got != 0 {
		t.Fatalf("pending calls = %d, want 0", got)
	}
}

func TestRPCClientBoundsInFlight(t *testing.T) {
	t.Parallel()
	clientConn, serverConn := net.Pipe()
	client, err := NewRPCClient(clientConn, RPCClientConfig{MaxInFlight: 1, DefaultTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	defer serverConn.Close()

	firstRead := make(chan protocol.Request, 1)
	go func() {
		var request protocol.Request
		if protocol.ReadFrame(serverConn, &request, protocol.DefaultMaxFrameSize) == nil {
			firstRead <- request
		}
	}()

	var wg sync.WaitGroup
	wg.Add(1)
	firstErr := make(chan error, 1)
	go func() {
		defer wg.Done()
		firstErr <- client.Call(context.Background(), protocol.MethodHealth, protocol.HealthRequest{}, nil)
	}()
	request := <-firstRead
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	err = client.Call(ctx, protocol.MethodHealth, protocol.HealthRequest{}, nil)
	cancel()
	if !errors.Is(err, ErrRPCTimeout) {
		t.Fatalf("second Call error = %v, want timeout", err)
	}

	payload, _ := protocol.MarshalPayload(protocol.HealthResponse{Ready: true})
	if err := protocol.WriteFrame(serverConn, protocol.Response{
		Protocol: protocol.Protocol, RequestID: request.RequestID, Kind: protocol.KindResponse,
		DeadlineUnixMS: request.DeadlineUnixMS, Result: payload,
	}, protocol.DefaultMaxFrameSize); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if err := <-firstErr; err != nil {
		t.Fatalf("first Call: %v", err)
	}
}

func TestRPCClientMalformedResponseClosesConnection(t *testing.T) {
	t.Parallel()
	clientConn, serverConn := net.Pipe()
	client, err := NewRPCClient(clientConn, RPCClientConfig{MaxInFlight: 1, DefaultTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	go func() {
		defer serverConn.Close()
		var request protocol.Request
		_ = protocol.ReadFrame(serverConn, &request, protocol.DefaultMaxFrameSize)
		_, _ = serverConn.Write([]byte{0, 0, 0, 1, '{'})
	}()
	err = client.Call(context.Background(), protocol.MethodHealth, protocol.HealthRequest{}, nil)
	if err == nil {
		t.Fatal("Call succeeded after malformed response")
	}
	select {
	case <-client.Done():
	case <-time.After(time.Second):
		t.Fatal("client did not close")
	}
}
