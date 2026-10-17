package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	protocol "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

var (
	ErrClientClosed = errors.New("plugin RPC client is closed")
	ErrNotReady     = errors.New("plugin is not ready")
	ErrRPCTimeout   = errors.New("plugin RPC deadline exceeded")
)

type RPCClientConfig struct {
	MaxFrameSize       uint32
	MaxInFlight        int
	DefaultTimeout     time.Duration
	MaxReverseInFlight int
	Observe            func(method protocol.Method, outcome string, latency time.Duration)
	RequestHandler     func(context.Context, protocol.Request) (any, error)
}

type rpcFrame struct {
	Protocol       string                `json:"protocol"`
	RequestID      uint64                `json:"request_id"`
	Kind           protocol.EnvelopeKind `json:"kind"`
	DeadlineUnixMS int64                 `json:"deadline_unix_ms"`
	Method         protocol.Method       `json:"method,omitempty"`
	Payload        json.RawMessage       `json:"payload,omitempty"`
	Result         json.RawMessage       `json:"result,omitempty"`
	Error          *protocol.Error       `json:"error,omitempty"`
}

// RPCClient multiplexes a bounded number of calls over one framed connection.
// It owns exactly one reader goroutine; callers execute in their own goroutines
// and writes are serialized by writeMu.
type RPCClient struct {
	conn io.ReadWriteCloser
	cfg  RPCClientConfig
	// maxFrame is negotiated during the handshake. It is atomic because the
	// reader starts before negotiation completes and may already be waiting for
	// the next frame when the supervisor applies the peer's lower limit.
	maxFrame atomic.Uint32

	ctx       context.Context
	cancel    context.CancelFunc
	done      chan struct{}
	wg        sync.WaitGroup
	reverseWG sync.WaitGroup
	once      sync.Once

	nextID     atomic.Uint64
	sem        chan struct{}
	reverseSem chan struct{}

	writeMu sync.Mutex
	mu      sync.Mutex
	pending map[uint64]chan protocol.Response
	reverse map[uint64]struct{}
	err     error
}

func NewRPCClient(conn io.ReadWriteCloser, cfg RPCClientConfig) (*RPCClient, error) {
	if conn == nil {
		return nil, fmt.Errorf("nil RPC connection")
	}
	if cfg.MaxFrameSize == 0 {
		cfg.MaxFrameSize = protocol.DefaultMaxFrameSize
	}
	if cfg.MaxInFlight <= 0 {
		return nil, fmt.Errorf("max in-flight RPCs must be positive")
	}
	if cfg.DefaultTimeout <= 0 {
		cfg.DefaultTimeout = 10 * time.Second
	}
	if cfg.MaxReverseInFlight <= 0 {
		cfg.MaxReverseInFlight = cfg.MaxInFlight
	}
	if cfg.MaxReverseInFlight > cfg.MaxInFlight {
		cfg.MaxReverseInFlight = cfg.MaxInFlight
	}
	ctx, cancel := context.WithCancel(context.Background())
	c := &RPCClient{
		conn: conn, cfg: cfg, ctx: ctx, cancel: cancel,
		done: make(chan struct{}), sem: make(chan struct{}, cfg.MaxInFlight),
		reverseSem: make(chan struct{}, cfg.MaxReverseInFlight),
		pending:    make(map[uint64]chan protocol.Response, cfg.MaxInFlight),
		reverse:    make(map[uint64]struct{}, cfg.MaxReverseInFlight),
	}
	c.maxFrame.Store(cfg.MaxFrameSize)
	c.wg.Add(1)
	go c.readLoop()
	return c, nil
}

func (c *RPCClient) Call(ctx context.Context, method protocol.Method, input, output any) (resultErr error) {
	started := time.Now()
	defer func() {
		if c.cfg.Observe == nil {
			return
		}
		outcome := "ok"
		if resultErr != nil {
			outcome = "error"
			if errors.Is(resultErr, ErrRPCTimeout) {
				outcome = "timeout"
			}
		}
		c.cfg.Observe(method, outcome, time.Since(started))
	}()
	if ctx == nil {
		ctx = context.Background()
	}
	callCtx, cancel := withDefaultTimeout(ctx, c.cfg.DefaultTimeout)
	defer cancel()

	select {
	case c.sem <- struct{}{}:
		defer func() { <-c.sem }()
	case <-callCtx.Done():
		return callContextError(callCtx.Err())
	case <-c.done:
		return c.closedError()
	}

	payload, err := protocol.MarshalPayload(input)
	if err != nil {
		return err
	}
	id := c.nextID.Add(1)
	deadline, _ := callCtx.Deadline()
	deadlineUnixMS := deadline.UnixMilli()
	// Wire precision is milliseconds. Round up so a peer never observes the
	// deadline before the authoritative local context has actually expired.
	if deadline.Nanosecond()%int(time.Millisecond) != 0 {
		deadlineUnixMS++
	}
	request := protocol.Request{
		Protocol: protocol.Protocol, RequestID: id, Kind: protocol.KindRequest,
		DeadlineUnixMS: deadlineUnixMS, Method: method, Payload: payload,
	}
	if err := protocol.ValidateRequest(request); err != nil {
		return err
	}

	responseCh := make(chan protocol.Response, 1)
	c.mu.Lock()
	select {
	case <-c.done:
		c.mu.Unlock()
		return c.closedError()
	default:
	}
	// The semaphore and map have the same fixed capacity. Keep the explicit
	// check as an invariant guard if this implementation changes later.
	if len(c.pending) >= cap(c.sem) {
		c.mu.Unlock()
		return fmt.Errorf("%w: pending map reached configured bound", protocol.ErrInvalidDTO)
	}
	c.pending[id] = responseCh
	c.mu.Unlock()

	c.writeMu.Lock()
	err = protocol.WriteFrame(c.conn, request, c.maxFrame.Load())
	c.writeMu.Unlock()
	if err != nil {
		c.removePending(id)
		c.terminate(fmt.Errorf("write RPC request: %w", err))
		return c.closedError()
	}

	select {
	case response := <-responseCh:
		// A peer can race the local deadline by returning its own timeout
		// response. The caller's context remains authoritative and receives a
		// stable ErrRPCTimeout classification in either select ordering.
		if err := callCtx.Err(); err != nil {
			return callContextError(err)
		}
		if response.RequestID != id {
			return fmt.Errorf("response request ID %d does not match %d", response.RequestID, id)
		}
		if response.DeadlineUnixMS != request.DeadlineUnixMS {
			err := fmt.Errorf("response deadline %d does not match request deadline %d", response.DeadlineUnixMS, request.DeadlineUnixMS)
			c.terminate(err)
			return err
		}
		if response.Error != nil {
			if response.Error.Code == protocol.ErrorDeadlineExceeded ||
				(response.Error.Code == protocol.ErrorPlugin && response.Error.Message == context.DeadlineExceeded.Error()) {
				return callContextError(context.DeadlineExceeded)
			}
			return response.Error
		}
		if output == nil {
			return nil
		}
		return protocol.DecodePayload(response.Result, output)
	case <-callCtx.Done():
		c.removePending(id)
		return callContextError(callCtx.Err())
	case <-c.done:
		c.removePending(id)
		return c.closedError()
	}
}

func (c *RPCClient) Close() error {
	c.terminate(ErrClientClosed)
	c.mu.Lock()
	c.mu.Unlock()
	c.wg.Wait()
	c.reverseWG.Wait()
	return nil
}

func (c *RPCClient) Done() <-chan struct{} { return c.done }

func (c *RPCClient) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

func (c *RPCClient) Pending() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.pending)
}

// SetMaxFrameSize lowers the live frame limit after protocol negotiation. The
// limit can never be raised above the bound validated by NewRPCClient.
func (c *RPCClient) SetMaxFrameSize(size uint32) error {
	if size == 0 || size > c.cfg.MaxFrameSize {
		return fmt.Errorf("negotiated maximum frame size %d is outside 1..%d", size, c.cfg.MaxFrameSize)
	}
	c.maxFrame.Store(size)
	return nil
}

func (c *RPCClient) MaxFrameSize() uint32 { return c.maxFrame.Load() }

func (c *RPCClient) readLoop() {
	defer c.wg.Done()
	for {
		var frame rpcFrame
		if err := protocol.ReadFrame(c.conn, &frame, c.maxFrame.Load()); err != nil {
			if c.ctx.Err() != nil {
				return
			}
			c.terminate(fmt.Errorf("read RPC frame: %w", err))
			return
		}
		if frame.Kind == protocol.KindRequest {
			if err := c.admitRequest(frame); err != nil {
				c.terminate(fmt.Errorf("handle RPC request: %w", err))
				return
			}
			continue
		}
		response := protocol.Response{
			Protocol: frame.Protocol, RequestID: frame.RequestID, Kind: frame.Kind,
			DeadlineUnixMS: frame.DeadlineUnixMS, Result: frame.Result, Error: frame.Error,
		}
		if err := protocol.ValidateResponse(response); err != nil {
			c.terminate(fmt.Errorf("validate RPC response: %w", err))
			return
		}

		c.mu.Lock()
		responseCh, ok := c.pending[response.RequestID]
		if ok {
			delete(c.pending, response.RequestID)
		}
		c.mu.Unlock()
		// Responses for expired calls are intentionally discarded. The buffered
		// channel makes delivery independent of the calling goroutine's schedule.
		if ok {
			responseCh <- response
		}
	}
}

func (c *RPCClient) admitRequest(frame rpcFrame) error {
	request := protocol.Request{Protocol: frame.Protocol, RequestID: frame.RequestID, Kind: frame.Kind,
		DeadlineUnixMS: frame.DeadlineUnixMS, Method: frame.Method, Payload: frame.Payload}
	if err := protocol.ValidateRequest(request); err != nil {
		return err
	}
	select {
	case c.reverseSem <- struct{}{}:
	default:
		return fmt.Errorf("%w: reverse RPC limit reached", protocol.ErrInvalidDTO)
	}
	c.mu.Lock()
	if _, exists := c.reverse[request.RequestID]; exists {
		c.mu.Unlock()
		<-c.reverseSem
		return fmt.Errorf("%w: duplicate reverse request ID %d", protocol.ErrInvalidDTO, request.RequestID)
	}
	select {
	case <-c.done:
		c.mu.Unlock()
		<-c.reverseSem
		return ErrClientClosed
	default:
	}
	c.reverseWG.Add(1)
	c.reverse[request.RequestID] = struct{}{}
	c.mu.Unlock()
	go func() {
		defer c.reverseWG.Done()
		defer func() {
			c.mu.Lock()
			delete(c.reverse, request.RequestID)
			c.mu.Unlock()
			<-c.reverseSem
		}()
		if err := c.handleRequest(request); err != nil {
			c.terminate(fmt.Errorf("reverse RPC response: %w", err))
		}
	}()
	return nil
}

func (c *RPCClient) handleRequest(request protocol.Request) error {
	started := time.Now()
	defer func() {
		if c.cfg.Observe == nil {
			return
		}
		outcome := "ok"
		if c.Err() != nil && c.ctx.Err() != nil {
			outcome = "error"
		}
		c.cfg.Observe(request.Method, outcome, time.Since(started))
	}()
	response := protocol.Response{
		Protocol: protocol.Protocol, RequestID: request.RequestID,
		Kind: protocol.KindResponse, DeadlineUnixMS: request.DeadlineUnixMS,
	}
	if c.cfg.RequestHandler == nil {
		response.Error = &protocol.Error{
			Code: protocol.ErrorUnsupported, Message: "reverse RPC is not enabled",
		}
	} else {
		requestContext, cancel := context.WithDeadline(
			c.ctx, time.UnixMilli(request.DeadlineUnixMS),
		)
		result, err := c.cfg.RequestHandler(requestContext, request)
		cancel()
		if err != nil {
			var wireError *protocol.Error
			if errors.As(err, &wireError) {
				response.Error = wireError
			} else {
				code := protocol.ErrorPlugin
				if errors.Is(err, context.DeadlineExceeded) {
					code = protocol.ErrorDeadlineExceeded
				}
				response.Error = &protocol.Error{Code: code, Message: err.Error()}
			}
		} else if result != nil {
			payload, marshalErr := protocol.MarshalPayload(result)
			if marshalErr != nil {
				return marshalErr
			}
			response.Result = payload
		}
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return protocol.WriteFrame(c.conn, response, c.maxFrame.Load())
}

func (c *RPCClient) removePending(id uint64) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

func (c *RPCClient) terminate(err error) {
	c.once.Do(func() {
		c.mu.Lock()
		c.err = err
		c.pending = make(map[uint64]chan protocol.Response, cap(c.sem))
		c.reverse = make(map[uint64]struct{}, cap(c.reverseSem))
		c.mu.Unlock()
		c.cancel()
		_ = c.conn.Close()
		close(c.done)
	})
}

func (c *RPCClient) closedError() error {
	err := c.Err()
	if err == nil {
		return ErrClientClosed
	}
	return err
}

func withDefaultTimeout(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if deadline, ok := parent.Deadline(); ok && time.Until(deadline) <= timeout {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, timeout)
}

func callContextError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w: %v", ErrRPCTimeout, err)
	}
	return err
}
