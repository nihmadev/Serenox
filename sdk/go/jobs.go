package plugin

import (
	"context"
	"encoding/json"
	"fmt"

	protocol "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

type JobsClient struct{ rpc *serverRPC }

// NewJobHandlerSpec returns a bounded declaration suitable for PluginMetadata.Jobs.
func NewJobHandlerSpec(name, description string) protocol.JobHandlerSpec {
	return protocol.JobHandlerSpec{
		Name: name, Description: description, Persistent: true,
		MaxRuntimeMS: 30_000, Overlap: protocol.JobOverlapSkipIfRunning,
		MaxPayloadBytes: protocol.MaxJobPayloadBytes,
	}
}

func newJobsClient(rpc *serverRPC) *JobsClient { return &JobsClient{rpc: rpc} }
func (c *JobsClient) call(ctx context.Context, operation protocol.JobOperation, input, output any) error {
	if c == nil || c.rpc == nil {
		return fmt.Errorf("jobs client is unavailable")
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return err
	}
	request := protocol.JobRPCRequest{Operation: operation, Payload: raw}
	return c.rpc.Call(ctx, protocol.MethodHostJobs, request, output)
}
func (c *JobsClient) Create(ctx context.Context, spec protocol.JobSpec) (protocol.JobStatus, error) {
	var result protocol.JobCreateResponse
	if err := c.call(ctx, protocol.JobOperationCreate, protocol.JobCreateRequest{Spec: spec}, &result); err != nil {
		return protocol.JobStatus{}, err
	}
	return result.Status, nil
}
func (c *JobsClient) Update(ctx context.Context, request protocol.JobUpdateRequest) (protocol.JobStatus, error) {
	var result protocol.JobUpdateResponse
	if err := c.call(ctx, protocol.JobOperationUpdate, request, &result); err != nil {
		return protocol.JobStatus{}, err
	}
	return result.Status, nil
}
func (c *JobsClient) Cancel(ctx context.Context, request protocol.JobIdentityRequest) (protocol.JobStatus, error) {
	return c.action(ctx, protocol.JobOperationCancel, request)
}
func (c *JobsClient) Pause(ctx context.Context, request protocol.JobIdentityRequest) (protocol.JobStatus, error) {
	return c.action(ctx, protocol.JobOperationPause, request)
}
func (c *JobsClient) Resume(ctx context.Context, request protocol.JobIdentityRequest) (protocol.JobStatus, error) {
	return c.action(ctx, protocol.JobOperationResume, request)
}
func (c *JobsClient) action(ctx context.Context, operation protocol.JobOperation, request protocol.JobIdentityRequest) (protocol.JobStatus, error) {
	var result protocol.JobActionResponse
	if err := c.call(ctx, operation, request, &result); err != nil {
		return protocol.JobStatus{}, err
	}
	return result.Status, nil
}
func (c *JobsClient) Trigger(ctx context.Context, request protocol.JobTriggerRequest) (protocol.JobTriggerResponse, error) {
	var result protocol.JobTriggerResponse
	err := c.call(ctx, protocol.JobOperationTrigger, request, &result)
	return result, err
}
func (c *JobsClient) Get(ctx context.Context, request protocol.JobGetRequest) (protocol.JobStatus, error) {
	var result protocol.JobGetResponse
	if err := c.call(ctx, protocol.JobOperationGet, request, &result); err != nil {
		return protocol.JobStatus{}, err
	}
	return result.Status, nil
}
func (c *JobsClient) List(ctx context.Context, request protocol.JobListRequest) (protocol.JobListResponse, error) {
	var result protocol.JobListResponse
	err := c.call(ctx, protocol.JobOperationList, request, &result)
	return result, err
}
func (c *JobsClient) Inspect(ctx context.Context, request protocol.JobInspectRequest) (protocol.JobInspectResponse, error) {
	var result protocol.JobInspectResponse
	err := c.call(ctx, protocol.JobOperationInspect, request, &result)
	return result, err
}

func (c *JobsClient) Delete(ctx context.Context, request protocol.JobIdentityRequest) error {
	return c.call(ctx, protocol.JobOperationDelete, request, &struct{}{})
}

func (c *JobsClient) Clear(ctx context.Context, request protocol.JobClearRequest) (protocol.JobClearResponse, error) {
	var result protocol.JobClearResponse
	err := c.call(ctx, protocol.JobOperationClear, request, &result)
	return result, err
}
