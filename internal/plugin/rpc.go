package plugin

import (
	"context"
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
	MaxFrameSize   uint32
	MaxInFlight    int
	DefaultTimeout time.Duration
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

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	wg     sync.WaitGroup
	once   sync.Once

	nextID atomic.Uint64
	sem    chan struct{}

	writeMu sync.Mutex
	mu      sync.Mutex
	pending map[uint64]chan protocol.Response
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
	ctx, cancel := context.WithCancel(context.Background())
	c := &RPCClient{
		conn: conn, cfg: cfg, ctx: ctx, cancel: cancel,
		done: make(chan struct{}), sem: make(chan struct{}, cfg.MaxInFlight),
		pending: make(map[uint64]chan protocol.Response, cfg.MaxInFlight),
	}
	c.maxFrame.Store(cfg.MaxFrameSize)
	c.wg.Add(1)
	go c.readLoop()
	return c, nil
}

func (c *RPCClient) Call(ctx context.Context, method protocol.Method, input, output any) error {
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
	c.wg.Wait()
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
		var response protocol.Response
		if err := protocol.ReadFrame(c.conn, &response, c.maxFrame.Load()); err != nil {
			if c.ctx.Err() != nil {
				return
			}
			c.terminate(fmt.Errorf("read RPC response: %w", err))
			return
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

func (c *RPCClient) removePending(id uint64) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

func (c *RPCClient) terminate(err error) {
	c.once.Do(func() {
		c.mu.Lock()
		c.err = err
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
