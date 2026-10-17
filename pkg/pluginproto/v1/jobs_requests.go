package v1

type JobCreateRequest struct {
	Spec JobSpec `json:"spec"`
}
type JobCreateResponse struct {
	Status JobStatus `json:"status"`
}
type JobUpdateRequest struct {
	Identity     JobIdentity       `json:"identity"`
	Revision     uint64            `json:"revision"`
	Schedule     *JobSchedule      `json:"schedule,omitempty"`
	Timezone     *JobTimezone      `json:"timezone,omitempty"`
	DeadlineMS   *int64            `json:"deadline_ms,omitempty"`
	MaxRuntimeMS *int64            `json:"max_runtime_ms,omitempty"`
	Misfire      *JobMisfirePolicy `json:"misfire,omitempty"`
	Overlap      *JobOverlapPolicy `json:"overlap,omitempty"`
}
type JobUpdateResponse struct {
	Status JobStatus `json:"status"`
}
type JobIdentityRequest struct {
	Identity JobIdentity `json:"identity"`
	Revision uint64      `json:"revision,omitempty"`
}
type JobActionResponse struct {
	Status JobStatus `json:"status"`
}
type JobTriggerRequest struct {
	Identity JobIdentity `json:"identity"`
	Revision uint64      `json:"revision,omitempty"`
}
type JobTriggerResponse struct {
	Accepted bool      `json:"accepted"`
	Queued   bool      `json:"queued"`
	Status   JobStatus `json:"status"`
}
type JobGetRequest struct {
	Identity JobIdentity `json:"identity"`
}
type JobGetResponse struct {
	Status JobStatus `json:"status"`
}
type JobListRequest struct {
	PluginID  string `json:"plugin_id,omitempty"`
	AccountID int64  `json:"account_id,omitempty"`
	Limit     int    `json:"limit,omitempty"`
	Cursor    string `json:"cursor,omitempty"`
}
type JobListResponse struct {
	Jobs       []JobStatus `json:"jobs"`
	NextCursor string      `json:"next_cursor,omitempty"`
}
type JobInspectRequest struct {
	Identity       JobIdentity `json:"identity"`
	IncludeHistory bool        `json:"include_history,omitempty"`
}
type JobInspectResponse struct {
	Spec    JobSpec               `json:"spec"`
	Status  JobStatus             `json:"status"`
	History []JobExecutionAttempt `json:"history,omitempty"`
}
type JobClearRequest struct {
	Identity JobIdentity `json:"identity"`
	Revision uint64      `json:"revision,omitempty"`
}
type JobClearResponse struct {
	Status JobStatus `json:"status"`
}
type SchedulerHealth struct {
	Registered      int    `json:"registered"`
	Maximum         int    `json:"maximum"`
	Due             int    `json:"due"`
	Active          int    `json:"active"`
	LoopAlive       bool   `json:"loop_alive"`
	StoreFailures   uint64 `json:"store_failures"`
	RestoreFailures uint64 `json:"restore_failures"`
	Interrupted     uint64 `json:"interrupted"`
}
type JobHealthResponse struct {
	Scheduler SchedulerHealth `json:"scheduler"`
}
