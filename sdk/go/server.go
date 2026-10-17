// Package plugin provides the Go SDK for out-of-process Serenox plugins.
package plugin

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"

	protocol "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

const (
	EnvSocket       = "SERENOX_PLUGIN_SOCKET"
	EnvToken        = "SERENOX_PLUGIN_TOKEN"
	EnvInstanceID   = "SERENOX_PLUGIN_INSTANCE_ID"
	EnvProtocol     = "SERENOX_PLUGIN_PROTOCOL"
	EnvMaxFrameSize = "SERENOX_PLUGIN_MAX_FRAME_BYTES"
)

type Handler interface {
	Metadata(context.Context) (protocol.PluginMetadata, error)
	Configure(context.Context, []protocol.ConfigValue) error
	Start(context.Context) error
	Stop(context.Context) error
	Dispatch(context.Context, protocol.DispatchRequest) (protocol.DispatchResponse, error)
	Health(context.Context, protocol.HealthRequest) (protocol.HealthResponse, error)
}

type JobHandler interface {
	Job(context.Context, protocol.JobInvocation) (protocol.DispatchResponse, error)
}

type EventHandler interface {
	Event(context.Context, protocol.EventDelivery) (protocol.DispatchResponse, error)
}

// BackgroundHandler is an optional long-running hook for plugins that react to
// sources outside Telegram (D-Bus, sockets, file watchers). The SDK starts it
// once after lifecycle.start succeeds, with host services attached to its
// context, and cancels that context before Stop runs or when the connection
// closes. A non-nil error other than context cancellation closes the
// connection so the host restart policy applies.
type BackgroundHandler interface {
	Background(context.Context) error
}

// HandlerFuncs makes small plugins concise while retaining explicit lifecycle
// hooks. Nil optional callbacks use no-op defaults.
type HandlerFuncs struct {
	MetadataFunc  func(context.Context) (protocol.PluginMetadata, error)
	ConfigureFunc func(context.Context, []protocol.ConfigValue) error
	StartFunc     func(context.Context) error
	StopFunc      func(context.Context) error
	DispatchFunc  func(context.Context, protocol.DispatchRequest) (protocol.DispatchResponse, error)
	HealthFunc    func(context.Context, protocol.HealthRequest) (protocol.HealthResponse, error)
	JobFunc       func(context.Context, protocol.JobInvocation) (protocol.DispatchResponse, error)
	JobFuncs      map[string]func(context.Context, protocol.JobInvocation) (protocol.DispatchResponse, error)
	EventFuncs    map[string]func(context.Context, protocol.EventDelivery) (protocol.DispatchResponse, error)
}

func (h HandlerFuncs) Event(ctx context.Context, delivery protocol.EventDelivery) (protocol.DispatchResponse, error) {
	if function := h.EventFuncs[delivery.SubscriptionID]; function != nil {
		return function(ctx, delivery)
	}
	return protocol.DispatchResponse{EventResult: &protocol.EventHandlerResult{Outcome: protocol.EventTerminal, Code: "handler_unavailable", Message: "event handler is not registered"}}, nil
}

func (h HandlerFuncs) Job(ctx context.Context, invocation protocol.JobInvocation) (protocol.DispatchResponse, error) {
	if h.JobFunc == nil {
		if handler := h.JobFuncs[invocation.Handler]; handler != nil {
			return handler(ctx, invocation)
		}
		return protocol.DispatchResponse{}, &protocol.Error{Code: protocol.ErrorJobHandlerUnavailable, Message: "job handler is not implemented"}
	}
	return h.JobFunc(ctx, invocation)
}

func (h HandlerFuncs) Metadata(ctx context.Context) (protocol.PluginMetadata, error) {
	if h.MetadataFunc == nil {
		return protocol.PluginMetadata{}, fmt.Errorf("MetadataFunc is required")
	}
	return h.MetadataFunc(ctx)
}

func (h HandlerFuncs) Configure(ctx context.Context, values []protocol.ConfigValue) error {
	if h.ConfigureFunc == nil {
		return nil
	}
	return h.ConfigureFunc(ctx, values)
}

func (h HandlerFuncs) Start(ctx context.Context) error {
	if h.StartFunc == nil {
		return nil
	}
	return h.StartFunc(ctx)
}

func (h HandlerFuncs) Stop(ctx context.Context) error {
	if h.StopFunc == nil {
		return nil
	}
	return h.StopFunc(ctx)
}

func (h HandlerFuncs) Dispatch(ctx context.Context, request protocol.DispatchRequest) (protocol.DispatchResponse, error) {
	if h.DispatchFunc == nil {
		return protocol.DispatchResponse{}, &protocol.Error{
			Code: protocol.ErrorUnsupported, Message: "plugin does not implement dispatch",
		}
	}
	return h.DispatchFunc(ctx, request)
}

func (h HandlerFuncs) Health(ctx context.Context, request protocol.HealthRequest) (protocol.HealthResponse, error) {
	if h.HealthFunc == nil {
		return protocol.HealthResponse{Ready: true, Status: "ok"}, nil
	}
	return h.HealthFunc(ctx, request)
}

type Connector interface {
	Dial(context.Context, string) (io.ReadWriteCloser, error)
}

type UnixConnector struct{}

func (UnixConnector) Dial(ctx context.Context, endpoint string) (io.ReadWriteCloser, error) {
	var dialer net.Dialer
	return dialer.DialContext(ctx, "unix", endpoint)
}

type Config struct {
	Endpoint      string
	ExpectedToken string
	MaxFrameSize  uint32
	Workers       int
	QueueSize     int
	WriteTimeout  time.Duration
	Connector     Connector
}

type Server struct {
	cfg Config
}

func NewServer(cfg Config) (*Server, error) {
	if cfg.MaxFrameSize == 0 {
		cfg.MaxFrameSize = protocol.DefaultMaxFrameSize
	}
	if cfg.Workers == 0 {
		cfg.Workers = 2
	}
	if cfg.Workers < 1 || cfg.Workers > 64 {
		return nil, fmt.Errorf("workers must be in range 1..64")
	}
	if cfg.QueueSize == 0 {
		cfg.QueueSize = 64
	}
	if cfg.QueueSize < 1 || cfg.QueueSize > 4096 {
		return nil, fmt.Errorf("queue size must be in range 1..4096")
	}
	if cfg.WriteTimeout <= 0 {
		cfg.WriteTimeout = 5 * time.Second
	}
	if cfg.Connector == nil {
		cfg.Connector = UnixConnector{}
	}
	return &Server{cfg: cfg}, nil
}

// Run connects to the core-created local endpoint and serves until the context
// is canceled or the lifecycle.stop request completes.
func Run(ctx context.Context, handler Handler, cfg Config) error {
	server, err := NewServer(cfg)
	if err != nil {
		return err
	}
	return server.Run(ctx, handler)
}

func (s *Server) Run(ctx context.Context, handler Handler) error {
	endpoint := s.cfg.Endpoint
	if endpoint == "" {
		endpoint = os.Getenv(EnvSocket)
	}
	if endpoint == "" {
		return fmt.Errorf("plugin endpoint is empty; set %s", EnvSocket)
	}
	if s.cfg.ExpectedToken == "" {
		s.cfg.ExpectedToken = os.Getenv(EnvToken)
	}
	conn, err := s.cfg.Connector.Dial(ctx, endpoint)
	if err != nil {
		return fmt.Errorf("connect to Serenox core: %w", err)
	}
	return s.Serve(ctx, conn, handler)
}

// Serve serves a single authenticated connection. The server owns the
// connection for the duration of the call and closes it on every return path.
func (s *Server) Serve(parent context.Context, conn io.ReadWriteCloser, handler Handler) error {
	if conn == nil || handler == nil {
		return fmt.Errorf("connection and handler are required")
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	defer conn.Close() //nolint:errcheck

	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		<-ctx.Done()
		_ = conn.Close()
	}()
	defer func() {
		cancel()
		<-watchDone
	}()

	writer := &responseWriter{
		conn: conn, maxFrame: s.cfg.MaxFrameSize, timeout: s.cfg.WriteTimeout,
		onWriteErr: func(error) { cancel() },
	}

	maxFrame, err := s.handshake(ctx, conn, writer, handler)
	if err != nil {
		return err
	}
	rpc := newServerRPC(writer, maxFrame)
	services := Services{}
	services.Actions = &ActionClient{rpc: rpc}
	services.Storage = newStorageClient(rpc)
	mediaClient, mediaErr := NewMediaClient(mediaRPCControl{rpc: rpc}, nil, nil)
	if mediaErr == nil {
		services.Media = mediaClient
	}
	services.HTTP = newHTTPClient(rpc)
	services.Jobs = newJobsClient(rpc)
	services.Client = newClient(rpc)
	services.Moderation = newModerationClient(rpc)
	services.Events = &EventClient{rpc: rpc}

	jobs := make(chan dispatchJob, s.cfg.QueueSize)
	var workers sync.WaitGroup
	workers.Add(s.cfg.Workers)
	for i := 0; i < s.cfg.Workers; i++ {
		go func() {
			defer workers.Done()
			s.dispatchWorker(ctx, jobs, writer, handler, services)
		}()
	}
	closeWorkers := sync.OnceFunc(func() {
		close(jobs)
		workers.Wait()
	})
	defer func() {
		cancel()
		closeWorkers()
	}()

	background := newBackgroundRunner(ctx, cancel)
	defer background.stop()

	state := stateConnected
	for {
		var frame serverFrame
		if err := protocol.ReadFrame(conn, &frame, maxFrame); err != nil {
			if failure := background.failure(); failure != nil {
				return failure
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("read plugin request: %w", err)
		}
		if frame.Kind == protocol.KindResponse {
			if err := rpc.deliver(frame); err != nil {
				return fmt.Errorf("read reverse RPC response: %w", err)
			}
			continue
		}
		request := protocol.Request{Protocol: frame.Protocol, RequestID: frame.RequestID, Kind: frame.Kind,
			DeadlineUnixMS: frame.DeadlineUnixMS, Method: frame.Method, Payload: frame.Payload}
		if err := protocol.ValidateRequest(request); err != nil {
			_ = writer.write(errorResponse(request, protocol.ErrorInvalidRequest, err.Error(), false))
			continue
		}
		if time.Now().UnixMilli() > request.DeadlineUnixMS {
			_ = writer.write(errorResponse(request, protocol.ErrorDeadlineExceeded, "request deadline has elapsed", true))
			continue
		}

		switch request.Method {
		case protocol.MethodConfigure:
			if state != stateConnected {
				_ = writer.write(errorResponse(request, protocol.ErrorInvalidState, "configure requires connected state", false))
				continue
			}
			var lifecycle protocol.LifecycleRequest
			if err := protocol.DecodePayload(request.Payload, &lifecycle); err != nil || lifecycle.Phase != protocol.LifecycleConfigure {
				_ = writer.write(errorResponse(request, protocol.ErrorInvalidRequest, "invalid configure payload", false))
				continue
			}
			err := invokeRequest(ctx, request, func(callCtx context.Context) error {
				return handler.Configure(callCtx, lifecycle.Config)
			})
			if err != nil {
				_ = writer.write(handlerErrorResponse(request, err))
				continue
			}
			state = stateConfigured
			if err := writer.write(resultResponse(request, protocol.LifecycleResponse{State: "configured"})); err != nil {
				return err
			}

		case protocol.MethodStart:
			if state != stateConfigured {
				_ = writer.write(errorResponse(request, protocol.ErrorInvalidState, "start requires configured state", false))
				continue
			}
			var lifecycle protocol.LifecycleRequest
			if err := protocol.DecodePayload(request.Payload, &lifecycle); err != nil || lifecycle.Phase != protocol.LifecycleStart {
				_ = writer.write(errorResponse(request, protocol.ErrorInvalidRequest, "invalid start payload", false))
				continue
			}
			err := invokeRequest(ctx, request, handler.Start)
			if err != nil {
				_ = writer.write(handlerErrorResponse(request, err))
				continue
			}
			state = stateStarted
			if err := writer.write(resultResponse(request, protocol.LifecycleResponse{State: "ready"})); err != nil {
				return err
			}
			if backgroundHandler, ok := handler.(BackgroundHandler); ok {
				background.start(backgroundHandler, services)
			}

		case protocol.MethodDispatch:
			if state != stateStarted {
				_ = writer.write(errorResponse(request, protocol.ErrorInvalidState, "dispatch requires started state", false))
				continue
			}
			var payload protocol.DispatchRequest
			if err := protocol.DecodePayload(request.Payload, &payload); err != nil {
				_ = writer.write(errorResponse(request, protocol.ErrorInvalidRequest, err.Error(), false))
				continue
			}
			if err := payload.Validate(); err != nil {
				_ = writer.write(errorResponse(request, protocol.ErrorInvalidRequest, err.Error(), false))
				continue
			}
			select {
			case jobs <- dispatchJob{request: request, payload: payload}:
			default:
				_ = writer.write(errorResponse(request, protocol.ErrorOverloaded, "plugin dispatch queue is full", true))
			}

		case protocol.MethodHealth:
			var healthRequest protocol.HealthRequest
			if err := protocol.DecodePayload(request.Payload, &healthRequest); err != nil {
				_ = writer.write(errorResponse(request, protocol.ErrorInvalidRequest, err.Error(), false))
				continue
			}
			var health protocol.HealthResponse
			err := invokeRequest(ctx, request, func(callCtx context.Context) error {
				var callErr error
				health, callErr = handler.Health(callCtx, healthRequest)
				return callErr
			})
			if err != nil {
				_ = writer.write(handlerErrorResponse(request, err))
				continue
			}
			if err := writer.write(resultResponse(request, health)); err != nil {
				return err
			}

		case protocol.MethodStop:
			if state != stateConfigured && state != stateStarted {
				_ = writer.write(errorResponse(request, protocol.ErrorInvalidState, "stop requires configured or started state", false))
				continue
			}
			var lifecycle protocol.LifecycleRequest
			if err := protocol.DecodePayload(request.Payload, &lifecycle); err != nil || lifecycle.Phase != protocol.LifecycleStop {
				_ = writer.write(errorResponse(request, protocol.ErrorInvalidRequest, "invalid stop payload", false))
				continue
			}
			background.stop()
			closeWorkers()
			err := invokeRequest(ctx, request, handler.Stop)
			if err != nil {
				_ = writer.write(handlerErrorResponse(request, err))
				return err
			}
			if err := writer.write(resultResponse(request, protocol.LifecycleResponse{State: "stopped"})); err != nil {
				return err
			}
			return nil

		case protocol.MethodHandshake:
			_ = writer.write(errorResponse(request, protocol.ErrorInvalidState, "handshake is already complete", false))
		default:
			_ = writer.write(errorResponse(request, protocol.ErrorUnsupported, "reverse RPC method is unsupported by plugin server", false))
		}
	}
}

func (s *Server) handshake(ctx context.Context, reader io.Reader, writer *responseWriter, handler Handler) (uint32, error) {
	var request protocol.Request
	if err := protocol.ReadFrame(reader, &request, s.cfg.MaxFrameSize); err != nil {
		return 0, fmt.Errorf("read handshake: %w", err)
	}
	if err := protocol.ValidateRequest(request); err != nil {
		_ = writer.write(errorResponse(request, protocol.ErrorInvalidRequest, err.Error(), false))
		return 0, err
	}
	if request.Method != protocol.MethodHandshake {
		err := fmt.Errorf("first request must be handshake")
		_ = writer.write(errorResponse(request, protocol.ErrorInvalidState, err.Error(), false))
		return 0, err
	}
	var handshake protocol.HandshakeRequest
	if err := protocol.DecodePayload(request.Payload, &handshake); err != nil {
		_ = writer.write(errorResponse(request, protocol.ErrorInvalidRequest, err.Error(), false))
		return 0, err
	}
	if err := handshake.Validate(); err != nil {
		_ = writer.write(errorResponse(request, protocol.ErrorInvalidRequest, err.Error(), false))
		return 0, err
	}
	if !contains(handshake.SupportedVersions, protocol.Version) {
		err := fmt.Errorf("peer does not support protocol %s", protocol.Version)
		_ = writer.write(errorResponse(request, protocol.ErrorIncompatibleProtocol, err.Error(), false))
		return 0, err
	}
	if !constantTimeEqual(handshake.AuthToken, s.cfg.ExpectedToken) {
		err := fmt.Errorf("plugin authentication token did not match")
		_ = writer.write(errorResponse(request, protocol.ErrorAuthentication, err.Error(), false))
		return 0, err
	}
	var metadata protocol.PluginMetadata
	if err := invokeRequest(ctx, request, func(callCtx context.Context) error {
		var err error
		metadata, err = handler.Metadata(callCtx)
		return err
	}); err != nil {
		_ = writer.write(handlerErrorResponse(request, err))
		return 0, err
	}
	if err := metadata.Validate(); err != nil {
		_ = writer.write(errorResponse(request, protocol.ErrorInvalidResponse, err.Error(), false))
		return 0, err
	}
	negotiatedFrame := min(handshake.MaxFrameBytes, s.cfg.MaxFrameSize)
	response := protocol.HandshakeResponse{
		SelectedVersion: protocol.Version, Nonce: handshake.Nonce,
		MaxFrameBytes: negotiatedFrame, Metadata: metadata,
	}
	if err := response.Validate(); err != nil {
		return 0, err
	}
	writer.maxFrame = negotiatedFrame
	if err := writer.write(resultResponse(request, response)); err != nil {
		return 0, err
	}
	return negotiatedFrame, nil
}
