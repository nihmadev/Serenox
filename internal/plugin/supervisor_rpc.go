package plugin

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/nihmadev/Serenox/internal/httpservice"
	"github.com/nihmadev/Serenox/internal/media"
	"github.com/nihmadev/Serenox/internal/storage"
	protocol "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

// Dispatch performs one at-most-once call. Transport failures and malformed
// results terminate only this generation; the owner loop applies restart policy.
func (s *Supervisor) Dispatch(ctx context.Context, request protocol.DispatchRequest) (protocol.DispatchResponse, error) {
	if err := request.Validate(); err != nil {
		return protocol.DispatchResponse{}, err
	}
	client, err := s.readyClient()
	if err != nil {
		return protocol.DispatchResponse{}, err
	}
	var response protocol.DispatchResponse
	err = client.Call(ctx, protocol.MethodDispatch, request, &response)
	if err == nil {
		err = response.Validate()
	}
	if err != nil && terminalRPCError(err) {
		client.terminate(fmt.Errorf("dispatch failed: %w", err))
	}
	return response, err
}

func (s *Supervisor) handleRequest(ctx context.Context, request protocol.Request) (any, error) {
	switch request.Method {
	case protocol.MethodHostActions:
		var batch protocol.ActionBatchRequest
		if err := protocol.DecodePayload(request.Payload, &batch); err != nil {
			return nil, err
		}
		if err := batch.Validate(); err != nil {
			return nil, err
		}
		actionCtx := withPluginIdentity(ctx, s.cfg.PluginID)
		if s.cfg.ActionResultsHandler != nil {
			response, err := s.cfg.ActionResultsHandler(actionCtx, batch.Actions)
			if err != nil {
				return nil, hostActionError(err)
			}
			if err := response.Validate(); err != nil {
				return nil, err
			}
			return response, nil
		}
		if s.cfg.ActionHandler == nil {
			return nil, &protocol.Error{Code: protocol.ErrorUnsupported, Message: "reverse action handling is disabled"}
		}
		if err := s.cfg.ActionHandler(actionCtx, batch.Actions); err != nil {
			return nil, hostActionError(err)
		}
		results := make([]protocol.ActionResult, len(batch.Actions))
		for index, action := range batch.Actions {
			results[index] = protocol.ActionResult{ActionID: action.ID, Operation: action.OperationName(), Status: protocol.ActionResultSuccess, Success: true}
		}
		return protocol.ActionBatchResponse{Results: results}, nil
	case protocol.MethodHostStorage:
		if s.cfg.StorageHandler == nil {
			return nil, &protocol.Error{Code: protocol.ErrorUnsupported, Message: "host.storage is not enabled"}
		}
		var payload protocol.StorageRequest
		if err := protocol.DecodePayload(request.Payload, &payload); err != nil {
			return nil, err
		}
		if err := payload.Validate(); err != nil {
			return nil, err
		}
		result, err := s.cfg.StorageHandler(ctx, s.cfg.PluginID, s.cfg.AccountID, payload)
		if err != nil {
			return nil, hostStorageError(payload.Operation, err)
		}
		if err := result.Validate(); err != nil {
			return nil, err
		}
		return result, nil
	case protocol.MethodHostClient:
		if s.cfg.ClientHandler == nil {
			return nil, &protocol.Error{Code: protocol.ErrorUnsupported, Message: "host.client is not enabled"}
		}
		var payload protocol.ClientRequest
		if err := protocol.DecodePayload(request.Payload, &payload); err != nil {
			return nil, err
		}
		if err := payload.Validate(); err != nil {
			return nil, err
		}
		result, err := s.cfg.ClientHandler(ctx, s.cfg.PluginID, s.cfg.AccountID, payload)
		if err != nil {
			return nil, err
		}
		if err := result.Validate(); err != nil {
			return nil, err
		}
		return result, nil
	case protocol.MethodHostModeration:
		if s.cfg.ModerationHandler == nil {
			return nil, &protocol.Error{Code: protocol.ErrorUnsupported, Message: "host.moderation is not enabled"}
		}
		var payload protocol.ModerationRequest
		if err := protocol.DecodePayload(request.Payload, &payload); err != nil {
			return nil, err
		}
		if err := payload.Validate(); err != nil {
			return nil, err
		}
		result, err := s.cfg.ModerationHandler(ctx, s.cfg.PluginID, s.cfg.AccountID, protocol.ModerationPermission(payload.Operation), payload)
		if err != nil {
			return nil, err
		}
		if err := result.Validate(); err != nil {
			return nil, err
		}
		return result, nil
	case protocol.MethodHostMedia:
		if s.cfg.MediaHandler == nil {
			return nil, &protocol.Error{Code: protocol.ErrorUnsupported, Message: "host.media is not enabled"}
		}
		var payload protocol.MediaRequest
		if err := protocol.DecodePayload(request.Payload, &payload); err != nil {
			return nil, err
		}
		if err := payload.Validate(); err != nil {
			return nil, err
		}
		result, err := s.cfg.MediaHandler(ctx, s.cfg.PluginID, s.cfg.AccountID, payload)
		if err != nil {
			return nil, hostMediaError(err)
		}
		if err := result.Validate(); err != nil {
			return nil, err
		}
		return result, nil
	case protocol.MethodHostHTTP:
		if s.cfg.HTTPHandler == nil {
			return nil, &protocol.Error{Code: protocol.ErrorUnsupported, Message: "host.http is not enabled"}
		}
		var payload protocol.HTTPRequest
		if err := protocol.DecodePayload(request.Payload, &payload); err != nil {
			return nil, err
		}
		if err := payload.Validate(); err != nil {
			return nil, err
		}
		result, err := s.cfg.HTTPHandler(ctx, s.cfg.PluginID, s.cfg.AccountID, s.cfg.NetworkPolicy, payload)
		if err != nil {
			return nil, hostHTTPError(err)
		}
		if err := result.Validate(); err != nil {
			return nil, err
		}
		return result, nil
	case protocol.MethodHostJobs:
		if s.cfg.JobsHandler == nil {
			return nil, &protocol.Error{Code: protocol.ErrorUnsupported, Message: "host.jobs is not enabled"}
		}
		var payload protocol.JobRPCRequest
		if err := protocol.DecodePayload(request.Payload, &payload); err != nil {
			return nil, err
		}
		if payload.Operation == "" || len(payload.Payload) == 0 {
			return nil, &protocol.Error{Code: protocol.ErrorInvalidRequest, Message: "job operation and payload are required"}
		}
		result, err := s.cfg.JobsHandler(ctx, s.cfg.PluginID, s.cfg.AccountID, payload)
		if err != nil {
			return nil, hostJobError(err)
		}
		return result, nil
	case protocol.MethodHostEvents:
		if s.cfg.EventsHandler == nil {
			return nil, &protocol.Error{Code: protocol.ErrorUnsupported, Message: "host.events is not enabled"}
		}
		var payload protocol.EventPublishRequest
		if err := protocol.DecodePayload(request.Payload, &payload); err != nil {
			return nil, err
		}
		if err := payload.Validate(); err != nil {
			return nil, err
		}
		result, err := s.cfg.EventsHandler(ctx, s.cfg.PluginID, s.cfg.AccountID, payload)
		if err != nil {
			return nil, hostEventError(err)
		}
		return result, nil
	default:
		return nil, fmt.Errorf("%w: reverse RPC method %q", protocol.ErrInvalidDTO, request.Method)
	}
}

func hostEventError(err error) error {
	var protocolErr *protocol.Error
	if errors.As(err, &protocolErr) {
		return err
	}
	if errors.Is(err, context.Canceled) {
		return &protocol.Error{Code: protocol.ErrorInvalidState, Message: "event publication canceled"}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &protocol.Error{Code: protocol.ErrorDeadlineExceeded, Message: "event publication deadline exceeded", Retryable: true}
	}
	message := err.Error()
	code := protocol.ErrorInvalidRequest
	if strings.Contains(message, "permission") || strings.Contains(message, "declared") {
		code = protocol.ErrorPermissionDenied
	}
	if strings.Contains(message, "quota") || strings.Contains(message, "fan-out") {
		code = protocol.ErrorOverloaded
	}
	return &protocol.Error{Code: code, Message: message, Retryable: code == protocol.ErrorOverloaded}
}

func hostJobError(err error) error {
	var protocolErr *protocol.Error
	if errors.As(err, &protocolErr) {
		return err
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &protocol.Error{Code: protocol.ErrorDeadlineExceeded, Message: "job operation deadline exceeded", Retryable: true}
	}
	if errors.Is(err, context.Canceled) {
		return &protocol.Error{Code: protocol.ErrorJobInterrupted, Message: "job operation canceled"}
	}
	return &protocol.Error{Code: protocol.ErrorJobInvalid, Message: err.Error()}
}

func hostActionError(err error) error {
	var protocolErr *protocol.Error
	if errors.As(err, &protocolErr) {
		return err
	}
	message := err.Error()
	if strings.Contains(strings.ToLower(message), "permission denied") {
		return &protocol.Error{Code: protocol.ErrorPermissionDenied, Message: message}
	}
	return err
}

func hostHTTPError(err error) error {
	if errors.Is(err, context.Canceled) {
		return &protocol.Error{Code: protocol.ErrorHTTPCancellation, Message: "HTTP request canceled"}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &protocol.Error{Code: protocol.ErrorHTTPTimeout, Message: "HTTP request timed out", Retryable: true}
	}
	var httpErr *httpservice.HTTPError
	if errors.As(err, &httpErr) {
		code := protocol.ErrorInternal
		switch httpErr.Category {
		case httpservice.CategoryInvalidURL:
			code = protocol.ErrorHTTPInvalidURL
		case httpservice.CategoryForbiddenHost:
			code = protocol.ErrorHTTPForbiddenHost
		case httpservice.CategoryDNSFailure:
			code = protocol.ErrorHTTPDNSFailure
		case httpservice.CategoryConnection:
			code = protocol.ErrorHTTPConnection
		case httpservice.CategoryTLS:
			code = protocol.ErrorHTTPTLSFailure
		case httpservice.CategoryTimeout:
			code = protocol.ErrorHTTPTimeout
		case httpservice.CategoryCancellation:
			code = protocol.ErrorHTTPCancellation
		case httpservice.CategoryRedirect:
			code = protocol.ErrorHTTPRedirect
		case httpservice.CategoryRequestSize:
			code = protocol.ErrorHTTPRequestSize
		case httpservice.CategoryResponseSize:
			code = protocol.ErrorHTTPResponseSize
		case httpservice.CategoryHeaderLimit:
			code = protocol.ErrorHTTPHeaders
		case httpservice.CategoryRateLimit:
			code = protocol.ErrorHTTPRateLimit
		case httpservice.CategoryOverload:
			code = protocol.ErrorHTTPOverload
		case httpservice.CategoryUpstreamStatus:
			code = protocol.ErrorHTTPUpstream
		case httpservice.CategorySSRF:
			code = protocol.ErrorHTTPSSRF
		case httpservice.CategoryUnsupported:
			code = protocol.ErrorHTTPUnsupported
		}
		details := map[string]string{}
		if httpErr.Response != nil {
			details["status"] = fmt.Sprintf("%d", httpErr.Response.Status)
			details["redirects"] = fmt.Sprintf("%d", httpErr.Response.Redirects)
		}
		return &protocol.Error{Code: code, Message: httpErr.Message, Retryable: httpErr.Retryable, Details: details}
	}
	return err
}

func hostMediaError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	code := protocol.ErrorMediaTransfer
	switch {
	case errors.Is(err, media.ErrInvalidHandle):
		code = protocol.ErrorMediaInvalidHandle
	case errors.Is(err, media.ErrOwnership):
		code = protocol.ErrorPermissionDenied
	case errors.Is(err, media.ErrQuota):
		code = protocol.ErrorMediaQuota
	case errors.Is(err, media.ErrConcurrent):
		code = protocol.ErrorOverloaded
	case errors.Is(err, media.ErrUnsupported):
		code = protocol.ErrorMediaUnsupported
	}
	return &protocol.Error{Code: code, Message: err.Error(), Retryable: code == protocol.ErrorOverloaded}
}

func hostStorageError(operation protocol.StorageOperation, err error) error {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return err
	}
	if errors.Is(err, storage.ErrInvalidName) || errors.Is(err, storage.ErrInvalidValue) {
		return &protocol.Error{Code: protocol.ErrorInvalidRequest, Message: err.Error()}
	}
	if errors.Is(err, storage.ErrLimit) {
		return &protocol.Error{Code: protocol.ErrorOverloaded, Message: err.Error(), Retryable: false}
	}
	if operation == protocol.StorageSet || operation == protocol.StorageDelete || operation == protocol.StorageBatch {
		return &protocol.Error{Code: protocol.ErrorStorageCommit, Message: err.Error(), Retryable: true}
	}
	return err
}

func (s *Supervisor) Health(ctx context.Context, detailed bool) (protocol.HealthResponse, error) {
	client, err := s.readyClient()
	if err != nil {
		return protocol.HealthResponse{}, err
	}
	response, err := s.healthCall(ctx, client, detailed)
	if err != nil && terminalRPCError(err) {
		client.terminate(fmt.Errorf("health call failed: %w", err))
	}
	return response, err
}

func (s *Supervisor) healthCall(parent context.Context, client *RPCClient, detailed bool) (protocol.HealthResponse, error) {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, s.cfg.HealthTimeout)
	defer cancel()
	var response protocol.HealthResponse
	if err := client.Call(ctx, protocol.MethodHealth, protocol.HealthRequest{Detailed: detailed}, &response); err != nil {
		return protocol.HealthResponse{}, err
	}
	if !response.Ready {
		return response, fmt.Errorf("%w: %s", ErrPluginUnhealthy, response.Status)
	}
	if !utf8.ValidString(response.Status) || len(response.Status) > protocol.MaxStringBytes {
		return response, fmt.Errorf("%w: invalid health status", protocol.ErrInvalidDTO)
	}
	if len(response.Metrics) > protocol.MaxCollectionItems {
		return response, fmt.Errorf("%w: health metrics exceed %d entries", protocol.ErrInvalidDTO, protocol.MaxCollectionItems)
	}
	s.mu.Lock()
	s.healthMetrics = make(map[string]uint64, len(response.Metrics))
	for key, value := range response.Metrics {
		s.healthMetrics[key] = value
	}
	s.mu.Unlock()
	return response, nil
}

func terminalRPCError(err error) bool {
	var wireError *protocol.Error
	if errors.As(err, &wireError) {
		return wireError.Code == protocol.ErrorDeadlineExceeded || wireError.Code == protocol.ErrorInvalidResponse
	}
	return true
}
