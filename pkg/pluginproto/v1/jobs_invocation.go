package v1

type JobInvocation struct {
	Identity          JobIdentity `json:"identity"`
	Handler           string      `json:"handler"`
	Payload           []byte      `json:"payload,omitempty"`
	ScheduledAtUnixMS int64       `json:"scheduled_at_unix_ms"`
	RunID             string      `json:"run_id"`
}
