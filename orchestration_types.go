// orchestration_types.go defines all shared types, constants, and structs
// used across the orchestration layer. No logic lives here.
package toolcalling

import (
	"time"

	"github.com/openai/openai-go/v3"
)

// ---------------------------------------------------------------------------
// Orchestration event types
// ---------------------------------------------------------------------------

type OrchestrationEventType string

const (
	EventRunStarted    OrchestrationEventType = "run_started"
	EventTaskStarted   OrchestrationEventType = "task_started"
	EventTaskProgress  OrchestrationEventType = "task_progress"
	EventTargetFound   OrchestrationEventType = "target_found"
	EventTerminateSent OrchestrationEventType = "terminate_sent"
	EventTerminatedAck OrchestrationEventType = "terminated_ack"
	EventTaskCompleted OrchestrationEventType = "task_completed"
	EventTaskError     OrchestrationEventType = "task_error"
	EventRunCompleted  OrchestrationEventType = "run_completed"
)

// ---------------------------------------------------------------------------
// Task & run state enums
// ---------------------------------------------------------------------------

type TaskState string

const (
	TaskPending     TaskState = "pending"
	TaskRunning     TaskState = "running"
	TaskFound       TaskState = "found"
	TaskNoMatch     TaskState = "no_match"
	TaskError       TaskState = "error"
	TaskTerminating TaskState = "terminating"
	TaskTerminated  TaskState = "terminated"
	TaskTimeout     TaskState = "timeout"
)

type RunState string

const (
	RunRunning   RunState = "running"
	RunSucceeded RunState = "succeeded"
	RunFailed    RunState = "failed"
	RunTimeout   RunState = "timeout"
)

// ---------------------------------------------------------------------------
// Event & status structs
// ---------------------------------------------------------------------------

type OrchestrationEvent struct {
	RunID     string
	TaskID    string
	TaskIndex int
	Type      OrchestrationEventType
	Message   string
	Data      map[string]any
	At        time.Time
}

type TaskStatus struct {
	ID          string
	Index       int
	State       TaskState
	LastMessage string
	LastError   string
	UpdatedAt   time.Time
}

type OrchestrationRunStatus struct {
	RunID         string
	State         RunState
	StartedAt     time.Time
	EndedAt       time.Time
	WinnerTaskID  string
	WinnerIndex   int
	TerminateSent int
	TerminateAck  int
	Tasks         map[string]TaskStatus
	ErrorSummary  []string
}

// ---------------------------------------------------------------------------
// Config & result
// ---------------------------------------------------------------------------

type OrchestrationRunConfig struct {
	SuccessCond         SuccessCondition
	MaxConcurrent       int
	TerminateAckTimeout time.Duration
	EventBuffer         int
}

type OrchestrationRunResult struct {
	RunID  string
	Winner *OrchestrationRaceResult
	Status OrchestrationRunStatus
}

// ---------------------------------------------------------------------------
// Task & race result
// ---------------------------------------------------------------------------

// RaceTask represents one independently executable sub-task managed by an
// OrchestrationAgent.
type RaceTask struct {
	ID       string
	Messages []openai.ChatCompletionMessageParamUnion
}

// OrchestrationRaceResult is the winner payload returned by RunRaceTasks.
type OrchestrationRaceResult struct {
	TaskID   string
	Index    int
	Messages []openai.ChatCompletionMessageParamUnion
}

// BatchRaceToolConfig defines how AddBatchRaceTool exposes BatchRace as a tool
// on the orchestration controller.
type BatchRaceToolConfig struct {
	Name                 string
	Description          string
	DefaultMaxConcurrent int
	Parameters           map[string]any
}

// ---------------------------------------------------------------------------
// Internal
// ---------------------------------------------------------------------------

type orchestrationResultItem struct {
	index     int
	taskID    string
	msgs      []openai.ChatCompletionMessageParamUnion
	err       error
	cancelled bool
}
