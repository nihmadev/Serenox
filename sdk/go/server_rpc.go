package plugin

import (
	"context"
	"encoding/json"
	"io"
	"sync"
	"sync/atomic"
	"time"

	protocol "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

type serverState uint8

const (
	stateConnected serverState = iota
	stateConfigured
	stateStarted
)

type dispatchJob struct {
	request protocol.Request
	payload protocol.DispatchRequest
}

type responseWriter struct {
	conn       io.Writer
	maxFrame   uint32
	timeout    time.Duration
	mu         sync.Mutex
	onWriteErr func(error)
}

func (w *responseWriter) write(response protocol.Response) error { return w.writeFrame(response) }

func (w *responseWriter) writeFrame(frame any) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if deadlineConn, ok := w.conn.(interface{ SetWriteDeadline(time.Time) error }); ok {
		_ = deadlineConn.SetWriteDeadline(time.Now().Add(w.timeout))
		defer deadlineConn.SetWriteDeadline(time.Time{}) //nolint:errcheck
	}
	err := protocol.WriteFrame(w.conn, frame, w.maxFrame)
	if err != nil && w.onWriteErr != nil {
		w.onWriteErr(err)
	}
	return err
}

type serverFrame struct {
	Protocol       string                `json:"protocol"`
	RequestID      uint64                `json:"request_id"`
	Kind           protocol.EnvelopeKind `json:"kind"`
	DeadlineUnixMS int64                 `json:"deadline_unix_ms"`
	Method         protocol.Method       `json:"method,omitempty"`
	Payload        json.RawMessage       `json:"payload,omitempty"`
	Result         json.RawMessage       `json:"result,omitempty"`
	Error          *protocol.Error       `json:"error,omitempty"`
}

type serverRPC struct {
	writer   *responseWriter
	maxFrame uint32
	nextID   uint64
	mu       sync.Mutex
	pending  map[uint64]chan protocol.Response
}

func newServerRPC(writer *responseWriter, maxFrame uint32) *serverRPC {
	return &serverRPC{writer: writer, maxFrame: maxFrame, pending: make(map[uint64]chan protocol.Response)}
}

func (r *serverRPC) Call(ctx context.Context, method protocol.Method, input, output any) error {
	if ctx == nil {
		ctx = context.Background()
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(10 * time.Second)
	}
	id := atomic.AddUint64(&r.nextID, 1)
	payload, err := protocol.MarshalPayload(input)
	if err != nil {
		return err
	}
	request := serverFrame{Protocol: protocol.Protocol, RequestID: id, Kind: protocol.KindRequest,
		DeadlineUnixMS: deadline.UnixMilli(), Method: method, Payload: payload}
	responseCh := make(chan protocol.Response, 1)
	r.mu.Lock()
	r.pending[id] = responseCh
	r.mu.Unlock()
	if err := r.writer.writeFrame(request); err != nil {
		r.remove(id)
		return err
	}
	select {
	case response := <-responseCh:
		if response.Error != nil {
			return response.Error
		}
		if output == nil {
			return nil
		}
		return protocol.DecodePayload(response.Result, output)
	case <-ctx.Done():
		r.remove(id)
		return ctx.Err()
	}
}

func (r *serverRPC) remove(id uint64) {
	r.mu.Lock()
	delete(r.pending, id)
	r.mu.Unlock()
}

func (r *serverRPC) deliver(frame serverFrame) error {
	response := protocol.Response{Protocol: frame.Protocol, RequestID: frame.RequestID, Kind: frame.Kind,
		DeadlineUnixMS: frame.DeadlineUnixMS, Result: frame.Result, Error: frame.Error}
	if err := protocol.ValidateResponse(response); err != nil {
		return err
	}
	r.mu.Lock()
	ch, ok := r.pending[response.RequestID]
	if ok {
		delete(r.pending, response.RequestID)
	}
	r.mu.Unlock()
	if ok {
		ch <- response
	}
	return nil
}

type mediaRPCControl struct{ rpc *serverRPC }

func (c mediaRPCControl) Media(ctx context.Context, request protocol.MediaRequest) (protocol.MediaResult, error) {
	var result protocol.MediaResult
	err := c.rpc.Call(ctx, protocol.MethodHostMedia, request, &result)
	return result, err
}
