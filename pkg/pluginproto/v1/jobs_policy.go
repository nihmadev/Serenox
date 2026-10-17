package v1

type JobMisfirePolicy string

const (
	JobMisfireSkip       JobMisfirePolicy = "skip"
	JobMisfireFireOnce   JobMisfirePolicy = "fire_once"
	JobMisfireCatchUpOne JobMisfirePolicy = "catch_up_one"
)

type JobOverlapPolicy string

const (
	JobOverlapSkipIfRunning      JobOverlapPolicy = "skip_if_running"
	JobOverlapCoalesceOnePending JobOverlapPolicy = "coalesce_one_pending"
	JobOverlapQueueOneBounded    JobOverlapPolicy = "queue_one_bounded"
	JobOverlapRejectQueueFull    JobOverlapPolicy = "reject_when_plugin_queue_full"
)

type JobRunStatus string

const (
	JobRunDue         JobRunStatus = "due"
	JobRunAccepted    JobRunStatus = "accepted"
	JobRunQueued      JobRunStatus = "queued"
	JobRunStarted     JobRunStatus = "started"
	JobRunCompleted   JobRunStatus = "completed"
	JobRunFailed      JobRunStatus = "failed"
	JobRunTimedOut    JobRunStatus = "timed_out"
	JobRunCancelled   JobRunStatus = "cancelled"
	JobRunSkipped     JobRunStatus = "skipped"
	JobRunCoalesced   JobRunStatus = "coalesced"
	JobRunOverdue     JobRunStatus = "overdue"
	JobRunRejected    JobRunStatus = "rejected"
	JobRunInterrupted JobRunStatus = "interrupted"
)

type JobErrorCategory string

const (
	JobErrorInvalidPayload JobErrorCategory = "invalid_payload"
	JobErrorHandler        JobErrorCategory = "handler_error"
	JobErrorTimeout        JobErrorCategory = "timeout"
	JobErrorCancelled      JobErrorCategory = "cancelled"
	JobErrorOverloaded     JobErrorCategory = "overloaded"
	JobErrorPermission     JobErrorCategory = "permission_denied"
	JobErrorInterrupted    JobErrorCategory = "interrupted"
)

type JobErrorSummary struct {
	Category  JobErrorCategory `json:"category"`
	Message   string           `json:"message,omitempty"`
	Retryable bool             `json:"retryable,omitempty"`
}
type JobExecutionAttempt struct {
	RunID             string           `json:"run_id"`
	Status            JobRunStatus     `json:"status"`
	ScheduledAtUnixMS int64            `json:"scheduled_at_unix_ms"`
	StartedAtUnixMS   int64            `json:"started_at_unix_ms,omitempty"`
	FinishedAtUnixMS  int64            `json:"finished_at_unix_ms,omitempty"`
	QueueDelayMS      int64            `json:"queue_delay_ms,omitempty"`
	RuntimeMS         int64            `json:"runtime_ms,omitempty"`
	Error             *JobErrorSummary `json:"error,omitempty"`
}
type JobStatus struct {
	Identity      JobIdentity         `json:"identity"`
	State         JobState            `json:"state"`
	Revision      uint64              `json:"revision"`
	NextRunUnixMS int64               `json:"next_run_unix_ms,omitempty"`
	LastRun       JobExecutionAttempt `json:"last_run"`
	Runs          uint64              `json:"runs"`
	Missed        uint64              `json:"missed"`
	Rejected      uint64              `json:"rejected"`
	Skipped       uint64              `json:"skipped"`
	Coalesced     uint64              `json:"coalesced"`
	Active        int                 `json:"active"`
}
