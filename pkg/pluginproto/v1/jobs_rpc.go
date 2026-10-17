package v1

import "encoding/json"

type JobOperation string

const (
	JobOperationCreate  JobOperation = "create"
	JobOperationUpdate  JobOperation = "update"
	JobOperationCancel  JobOperation = "cancel"
	JobOperationPause   JobOperation = "pause"
	JobOperationResume  JobOperation = "resume"
	JobOperationTrigger JobOperation = "trigger"
	JobOperationGet     JobOperation = "get"
	JobOperationList    JobOperation = "list"
	JobOperationInspect JobOperation = "inspect"
	JobOperationClear   JobOperation = "clear"
	JobOperationDelete  JobOperation = "delete"
)

type JobRPCRequest struct {
	Operation JobOperation    `json:"operation"`
	Payload   json.RawMessage `json:"payload"`
}
