package plugin

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"time"

	protocol "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

func (s *Server) dispatchWorker(ctx context.Context, jobs <-chan dispatchJob, writer *responseWriter, handler Handler, services Services) {
	for {
		select {
		case <-ctx.Done():
			return
		case job, ok := <-jobs:
			if !ok {
				return
			}
			if time.Now().UnixMilli() > job.request.DeadlineUnixMS {
				_ = writer.write(errorResponse(job.request, protocol.ErrorDeadlineExceeded, "dispatch deadline elapsed in queue", true))
				continue
			}
			var result protocol.DispatchResponse
			err := invokeRequest(ctx, job.request, func(callCtx context.Context) error {
				var callErr error
				if job.payload.Event != nil {
					eventHandler, ok := handler.(EventHandler)
					if !ok {
						return &protocol.Error{Code: protocol.ErrorUnsupported, Message: "event handler is not implemented"}
					}
					result, callErr = eventHandler.Event(WithServices(callCtx, services), *job.payload.Event)
				} else if job.payload.Job != nil {
					jobHandler, ok := handler.(JobHandler)
					if !ok {
						return &protocol.Error{Code: protocol.ErrorJobHandlerUnavailable, Message: "job handler is not implemented"}
					}
					result, callErr = jobHandler.Job(WithServices(callCtx, services), *job.payload.Job)
				} else {
					result, callErr = handler.Dispatch(WithServices(callCtx, services), job.payload)
				}
				return callErr
			})
			if err != nil {
				_ = writer.write(handlerErrorResponse(job.request, err))
				continue
			}
			if err := result.Validate(); err != nil {
				_ = writer.write(errorResponse(job.request, protocol.ErrorInvalidResponse, err.Error(), false))
				continue
			}
			_ = writer.write(resultResponse(job.request, result))
		}
	}
}

func resultResponse(request protocol.Request, value any) protocol.Response {
	payload, err := protocol.MarshalPayload(value)
	if err != nil {
		return errorResponse(request, protocol.ErrorInternal, err.Error(), false)
	}
	return protocol.Response{Protocol: protocol.Protocol, RequestID: request.RequestID, Kind: protocol.KindResponse,
		DeadlineUnixMS: request.DeadlineUnixMS, Result: payload}
}

func errorResponse(request protocol.Request, code protocol.ErrorCode, message string, retryable bool) protocol.Response {
	if len(message) > protocol.MaxStringBytes {
		message = message[:protocol.MaxStringBytes]
	}
	return protocol.Response{Protocol: protocol.Protocol, RequestID: request.RequestID, Kind: protocol.KindResponse,
		DeadlineUnixMS: request.DeadlineUnixMS,
		Error:          &protocol.Error{Code: code, Message: message, Retryable: retryable}}
}

func handlerErrorResponse(request protocol.Request, err error) protocol.Response {
	var rpcErr *protocol.Error
	if errors.As(err, &rpcErr) {
		return protocol.Response{Protocol: protocol.Protocol, RequestID: request.RequestID, Kind: protocol.KindResponse,
			DeadlineUnixMS: request.DeadlineUnixMS, Error: rpcErr}
	}
	return errorResponse(request, protocol.ErrorPlugin, err.Error(), false)
}

func invokeRequest(parent context.Context, request protocol.Request, fn func(context.Context) error) error {
	ctx, cancel := context.WithDeadline(parent, time.UnixMilli(request.DeadlineUnixMS))
	defer cancel()
	return invokeValue(ctx, fn)
}

func invokeValue(ctx context.Context, fn func(context.Context) error) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("plugin panic: %v", recovered)
		}
	}()
	return fn(ctx)
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func constantTimeEqual(left, right string) bool {
	if len(left) != len(right) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}
