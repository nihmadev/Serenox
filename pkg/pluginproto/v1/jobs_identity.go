package v1

import "encoding/json"

type JobScheduleKind string

const (
	JobScheduleOneShot    JobScheduleKind = "one_shot"
	JobScheduleFixedDelay JobScheduleKind = "fixed_delay"
	JobScheduleFixedRate  JobScheduleKind = "fixed_rate"
	JobScheduleCalendar   JobScheduleKind = "calendar"
)

type JobState string

const (
	JobStateEnabled     JobState = "enabled"
	JobStatePaused      JobState = "paused"
	JobStateCancelled   JobState = "cancelled"
	JobStateInterrupted JobState = "interrupted"
)

type JobIdentity struct {
	PluginID  string `json:"plugin_id"`
	AccountID int64  `json:"account_id"`
	ID        string `json:"id"`
}
type JobDSTPolicy string

const (
	JobDSTReject       JobDSTPolicy = "reject"
	JobDSTShiftForward JobDSTPolicy = "shift_forward"
	JobDSTFirstFold    JobDSTPolicy = "first_fold"
	JobDSTSecondFold   JobDSTPolicy = "second_fold"
)

type JobTimezone struct {
	Name string       `json:"name"`
	DST  JobDSTPolicy `json:"dst,omitempty"`
}

type JobSpec struct {
	Identity     JobIdentity      `json:"identity"`
	Handler      string           `json:"handler"`
	Description  string           `json:"description,omitempty"`
	Schedule     JobSchedule      `json:"schedule"`
	Timezone     JobTimezone      `json:"timezone"`
	Persistent   bool             `json:"persistent"`
	DeadlineMS   int64            `json:"deadline_ms"`
	MaxRuntimeMS int64            `json:"max_runtime_ms"`
	Misfire      JobMisfirePolicy `json:"misfire"`
	Overlap      JobOverlapPolicy `json:"overlap"`
	Payload      json.RawMessage  `json:"payload,omitempty"`
	PayloadType  string           `json:"payload_type,omitempty"`
	Revision     uint64           `json:"revision"`
}
type JobHandlerSpec struct {
	Name                 string           `json:"name"`
	Description          string           `json:"description,omitempty"`
	Persistent           bool             `json:"persistent"`
	MaxRuntimeMS         int64            `json:"max_runtime_ms"`
	Overlap              JobOverlapPolicy `json:"overlap"`
	RequiredCapabilities []Capability     `json:"required_capabilities,omitempty"`
	RequiredPermissions  []string         `json:"required_permissions,omitempty"`
	PayloadType          string           `json:"payload_type,omitempty"`
	MaxPayloadBytes      int              `json:"max_payload_bytes,omitempty"`
}
type JobSchedule struct {
	Kind     JobScheduleKind   `json:"kind"`
	OneShot  *OneShotSchedule  `json:"one_shot,omitempty"`
	Interval *IntervalSchedule `json:"interval,omitempty"`
	Calendar *CalendarSchedule `json:"calendar,omitempty"`
}
type OneShotSchedule struct {
	RunAtUnixMS int64 `json:"run_at_unix_ms"`
}
type IntervalSchedule struct {
	IntervalMS int64 `json:"interval_ms"`
}
type CalendarSchedule struct {
	Seconds     []int `json:"seconds,omitempty"`
	Minutes     []int `json:"minutes,omitempty"`
	Hours       []int `json:"hours,omitempty"`
	DaysOfMonth []int `json:"days_of_month,omitempty"`
	Weekdays    []int `json:"weekdays,omitempty"`
	Months      []int `json:"months,omitempty"`
}
