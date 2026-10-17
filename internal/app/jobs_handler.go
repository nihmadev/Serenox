package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/nihmadev/Serenox/internal/jobs"
	protocol "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

func (a *App) HandleHostJobs(ctx context.Context, pluginID string, accountID int64, request protocol.JobRPCRequest) (any, error) {
	if a.jobs == nil {
		return nil, &protocol.Error{Code: protocol.ErrorJobHandlerUnavailable, Message: "jobs scheduler is unavailable"}
	}
	if accountID == 0 {
		accountID = a.SelfID()
	}
	switch request.Operation {
	case protocol.JobOperationCreate:
		return a.jobCreate(ctx, pluginID, accountID, request.Payload)
	case protocol.JobOperationUpdate:
		return a.jobUpdate(ctx, pluginID, accountID, request.Payload)
	case protocol.JobOperationCancel, protocol.JobOperationPause, protocol.JobOperationResume:
		return a.jobAction(ctx, pluginID, accountID, request)
	case protocol.JobOperationTrigger:
		return a.jobTrigger(ctx, pluginID, accountID, request.Payload)
	case protocol.JobOperationGet:
		return a.jobGet(ctx, pluginID, accountID, request.Payload)
	case protocol.JobOperationList:
		return a.jobList(ctx, pluginID, accountID, request.Payload)
	case protocol.JobOperationInspect:
		return a.jobInspect(ctx, pluginID, accountID, request.Payload)
	case protocol.JobOperationClear:
		return a.jobClear(ctx, pluginID, accountID, request.Payload)
	case protocol.JobOperationDelete:
		return a.jobDelete(ctx, pluginID, accountID, request.Payload)
	default:
		return nil, &protocol.Error{Code: protocol.ErrorUnsupported, Message: fmt.Sprintf("unknown job operation %q", request.Operation)}
	}
}

func (a *App) jobCreate(ctx context.Context, pluginID string, accountID int64, raw json.RawMessage) (any, error) {
	var input protocol.JobCreateRequest
	if err := decodeJob(raw, &input); err != nil {
		return nil, err
	}
	input.Spec.Identity.PluginID, input.Spec.Identity.AccountID = pluginID, accountID
	a.jobMu.RLock()
	handlers, declared := a.jobHandlers[pluginID]
	handlerSpec, validHandler := handlers[input.Spec.Handler]
	a.jobMu.RUnlock()
	if !declared || len(handlers) == 0 {
		return nil, &protocol.Error{Code: protocol.ErrorJobHandlerUnavailable, Message: "plugin has no declared job handlers"}
	}
	if !validHandler {
		return nil, &protocol.Error{Code: protocol.ErrorJobHandlerUnavailable, Message: "job handler is not declared"}
	}
	if handlerSpec.MaxRuntimeMS > 0 && input.Spec.MaxRuntimeMS > handlerSpec.MaxRuntimeMS {
		return nil, &protocol.Error{Code: protocol.ErrorJobInvalid, Message: "job runtime exceeds handler policy"}
	}
	if handlerSpec.MaxPayloadBytes > 0 && len(input.Spec.Payload) > handlerSpec.MaxPayloadBytes {
		return nil, &protocol.Error{Code: protocol.ErrorJobInvalid, Message: "job payload exceeds handler policy"}
	}
	if input.Spec.Persistent != handlerSpec.Persistent {
		return nil, &protocol.Error{Code: protocol.ErrorJobInvalid, Message: "job persistence differs from handler policy"}
	}
	if handlerSpec.Overlap != "" && input.Spec.Overlap != handlerSpec.Overlap {
		return nil, &protocol.Error{Code: protocol.ErrorJobInvalid, Message: "job overlap differs from handler policy"}
	}
	status, err := a.jobs.Create(ctx, input.Spec)
	if err != nil {
		return nil, mapJobError(err)
	}
	return protocol.JobCreateResponse{Status: status}, nil
}

func (a *App) jobUpdate(ctx context.Context, pluginID string, accountID int64, raw json.RawMessage) (any, error) {
	var input protocol.JobUpdateRequest
	if err := decodeJob(raw, &input); err != nil {
		return nil, err
	}
	if err := checkJobIdentity(input.Identity, pluginID, accountID); err != nil {
		return nil, err
	}
	status, err := a.jobs.Update(ctx, input)
	if err != nil {
		return nil, mapJobError(err)
	}
	return protocol.JobUpdateResponse{Status: status}, nil
}

func (a *App) jobAction(ctx context.Context, pluginID string, accountID int64, request protocol.JobRPCRequest) (any, error) {
	var input protocol.JobIdentityRequest
	if err := decodeJob(request.Payload, &input); err != nil {
		return nil, err
	}
	if err := checkJobIdentity(input.Identity, pluginID, accountID); err != nil {
		return nil, err
	}
	state := protocol.JobStateCancelled
	if request.Operation == protocol.JobOperationPause {
		state = protocol.JobStatePaused
	}
	if request.Operation == protocol.JobOperationResume {
		state = protocol.JobStateEnabled
	}
	status, err := a.jobs.Action(ctx, input.Identity, input.Revision, state)
	if err != nil {
		return nil, mapJobError(err)
	}
	return protocol.JobActionResponse{Status: status}, nil
}

func (a *App) jobTrigger(ctx context.Context, pluginID string, accountID int64, raw json.RawMessage) (any, error) {
	var input protocol.JobTriggerRequest
	if err := decodeJob(raw, &input); err != nil {
		return nil, err
	}
	if err := checkJobIdentity(input.Identity, pluginID, accountID); err != nil {
		return nil, err
	}
	result, err := a.jobs.Trigger(ctx, input.Identity, input.Revision)
	if err != nil {
		return nil, mapJobError(err)
	}
	return result, nil
}
func (a *App) jobGet(ctx context.Context, pluginID string, accountID int64, raw json.RawMessage) (any, error) {
	var input protocol.JobGetRequest
	if err := decodeJob(raw, &input); err != nil {
		return nil, err
	}
	if err := checkJobIdentity(input.Identity, pluginID, accountID); err != nil {
		return nil, err
	}
	status, err := a.jobs.Get(ctx, input.Identity)
	if err != nil {
		return nil, mapJobError(err)
	}
	return protocol.JobGetResponse{Status: status}, nil
}
func (a *App) jobList(ctx context.Context, pluginID string, accountID int64, raw json.RawMessage) (any, error) {
	var input protocol.JobListRequest
	if err := decodeJob(raw, &input); err != nil {
		return nil, err
	}
	if input.PluginID != "" && input.PluginID != pluginID || input.AccountID != 0 && input.AccountID != accountID {
		return nil, &protocol.Error{Code: protocol.ErrorPermissionDenied, Message: "job list is scoped to the caller"}
	}
	values, err := a.jobs.List(ctx, pluginID, accountID, input.Limit)
	if err != nil {
		return nil, mapJobError(err)
	}
	return protocol.JobListResponse{Jobs: values}, nil
}
func (a *App) jobInspect(ctx context.Context, pluginID string, accountID int64, raw json.RawMessage) (any, error) {
	var input protocol.JobInspectRequest
	if err := decodeJob(raw, &input); err != nil {
		return nil, err
	}
	if err := checkJobIdentity(input.Identity, pluginID, accountID); err != nil {
		return nil, err
	}
	record, err := a.jobs.Inspect(ctx, input.Identity)
	if err != nil {
		return nil, mapJobError(err)
	}
	response := protocol.JobInspectResponse{Spec: record.Spec, Status: record.Status}
	if input.IncludeHistory {
		response.History = record.History
	}
	return response, nil
}

func (a *App) jobClear(ctx context.Context, pluginID string, accountID int64, raw json.RawMessage) (any, error) {
	var input protocol.JobClearRequest
	if err := decodeJob(raw, &input); err != nil {
		return nil, err
	}
	if err := checkJobIdentity(input.Identity, pluginID, accountID); err != nil {
		return nil, err
	}
	status, err := a.jobs.Clear(ctx, input.Identity, input.Revision)
	if err != nil {
		return nil, mapJobError(err)
	}
	return protocol.JobClearResponse{Status: status}, nil
}

func (a *App) jobDelete(ctx context.Context, pluginID string, accountID int64, raw json.RawMessage) (any, error) {
	var input protocol.JobIdentityRequest
	if err := decodeJob(raw, &input); err != nil {
		return nil, err
	}
	if err := checkJobIdentity(input.Identity, pluginID, accountID); err != nil {
		return nil, err
	}
	if err := a.jobs.Delete(ctx, input.Identity, input.Revision); err != nil {
		return nil, mapJobError(err)
	}
	return struct{}{}, nil
}

func decodeJob(raw json.RawMessage, target any) error {
	if err := json.Unmarshal(raw, target); err != nil {
		return &protocol.Error{Code: protocol.ErrorInvalidRequest, Message: "invalid jobs payload"}
	}
	return nil
}
func checkJobIdentity(identity protocol.JobIdentity, pluginID string, accountID int64) error {
	if identity.PluginID != pluginID || identity.AccountID != accountID {
		return &protocol.Error{Code: protocol.ErrorPermissionDenied, Message: "job identity does not belong to plugin/account"}
	}
	return nil
}
func mapJobError(err error) error {
	if errors.Is(err, jobs.ErrDuplicate) {
		return &protocol.Error{Code: protocol.ErrorJobDuplicate, Message: "job already exists"}
	}
	if errors.Is(err, jobs.ErrNotFound) {
		return &protocol.Error{Code: protocol.ErrorJobNotFound, Message: "job was not found"}
	}
	if errors.Is(err, jobs.ErrRevision) {
		return &protocol.Error{Code: protocol.ErrorJobStaleRevision, Message: "job revision is stale"}
	}
	if errors.Is(err, jobs.ErrCapacity) {
		return &protocol.Error{Code: protocol.ErrorJobOverloaded, Message: "job capacity is full", Retryable: true}
	}
	return err
}
