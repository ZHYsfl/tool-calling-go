// orchestration_agent.go defines OrchestrationAgent and its public API surface:
// construction, configuration, Chat, RunRace, RunRaceTasks, event subscription,
// and run status querying. The heavy RunManagedRace engine lives separately in
// orchestration_runtime.go.
package toolcalling

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/openai/openai-go/v3"
)

// OrchestrationAgent is a thin orchestration layer that coordinates multiple
// parallel sub-tasks on top of Agent + BatchRace.
//
// It is itself an agent-like wrapper (supports AddTool/RemoveTool/Chat) and
// can expose BatchRace as a callable tool via AddBatchRaceTool.
type OrchestrationAgent struct {
	controller *Agent
	worker     *Agent
	executor   WorkerExecutor
	bus        *OrchestrationBus
	runs       map[string]*OrchestrationRunStatus
	runsMu     sync.RWMutex
	runSeq     uint64
}

// NewOrchestrationAgent creates an orchestration wrapper.
//
// By default, the same Agent is used for both:
//   - controller: orchestrator's own chat/tools
//   - worker: parallel sub-task execution by BatchRace
//
// You can call SetWorkerAgent to separate them.
func NewOrchestrationAgent(agent *Agent) *OrchestrationAgent {
	return &OrchestrationAgent{
		controller: agent,
		worker:     agent,
		executor:   InProcessWorkerExecutor{},
		bus:        newOrchestrationBus(),
		runs:       make(map[string]*OrchestrationRunStatus),
	}
}

func (o *OrchestrationAgent) nextRunID() string {
	seq := atomic.AddUint64(&o.runSeq, 1)
	return fmt.Sprintf("run_%d", seq)
}

// ---------------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------------

// SetWorkerAgent configures a dedicated worker Agent used by BatchRace.
func (o *OrchestrationAgent) SetWorkerAgent(agent *Agent) {
	o.worker = agent
}

// SetWorkerExecutor sets how sub-tasks are executed.
// Use this to swap in process-based or remote executors.
func (o *OrchestrationAgent) SetWorkerExecutor(executor WorkerExecutor) {
	if executor == nil {
		o.executor = InProcessWorkerExecutor{}
		return
	}
	o.executor = executor
}

func (o *OrchestrationAgent) workerAgent() *Agent {
	if o.worker != nil {
		return o.worker
	}
	return o.controller
}

func (o *OrchestrationAgent) workerExecutor() WorkerExecutor {
	if o.executor != nil {
		return o.executor
	}
	return InProcessWorkerExecutor{}
}

// ---------------------------------------------------------------------------
// Agent-like delegation (controller)
// ---------------------------------------------------------------------------

// AddTool registers a tool on the orchestration controller agent.
func (o *OrchestrationAgent) AddTool(tool Tool) {
	o.controller.AddTool(tool)
}

// RemoveTool removes a tool from the orchestration controller agent.
func (o *OrchestrationAgent) RemoveTool(name string) {
	o.controller.RemoveTool(name)
}

// Chat runs a normal chat turn on the orchestration controller agent.
func (o *OrchestrationAgent) Chat(
	ctx context.Context,
	messages []openai.ChatCompletionMessageParamUnion,
) ([]openai.ChatCompletionMessageParamUnion, error) {
	return o.controller.Chat(ctx, messages)
}

// ---------------------------------------------------------------------------
// Race execution (thin wrappers over BatchRace)
// ---------------------------------------------------------------------------

// RunRace runs all tasks in parallel and returns as soon as one task satisfies
// successCond. Remaining tasks are cancelled through BatchRace's cascading
// cancellation.
func (o *OrchestrationAgent) RunRace(
	ctx context.Context,
	observations [][]openai.ChatCompletionMessageParamUnion,
	successCond SuccessCondition,
	opts ...RaceOption,
) (*RaceResult, error) {
	return BatchRace(ctx, o.workerAgent(), observations, successCond, opts...)
}

// RunRaceTasks is an ID-aware variant of RunRace. It returns the winner's
// TaskID in addition to index and messages.
func (o *OrchestrationAgent) RunRaceTasks(
	ctx context.Context,
	tasks []RaceTask,
	successCond SuccessCondition,
	opts ...RaceOption,
) (*OrchestrationRaceResult, error) {
	observations := make([][]openai.ChatCompletionMessageParamUnion, len(tasks))
	for i, t := range tasks {
		observations[i] = t.Messages
	}

	winner, err := BatchRace(ctx, o.workerAgent(), observations, successCond, opts...)
	if err != nil {
		return nil, err
	}

	taskID := ""
	if winner.Index >= 0 && winner.Index < len(tasks) {
		taskID = tasks[winner.Index].ID
	}

	return &OrchestrationRaceResult{
		TaskID:   taskID,
		Index:    winner.Index,
		Messages: winner.Messages,
	}, nil
}

// ---------------------------------------------------------------------------
// Event subscription & run status
// ---------------------------------------------------------------------------

// SubscribeRun subscribes to events from a specific run.
func (o *OrchestrationAgent) SubscribeRun(runID string, buffer int) (<-chan OrchestrationEvent, func()) {
	return o.bus.Subscribe(runID, buffer)
}

// SubscribeAllRuns subscribes to events from all runs.
func (o *OrchestrationAgent) SubscribeAllRuns(buffer int) (<-chan OrchestrationEvent, func()) {
	return o.bus.Subscribe("*", buffer)
}

// GetRunStatus returns a snapshot of a run's current status.
func (o *OrchestrationAgent) GetRunStatus(runID string) (OrchestrationRunStatus, bool) {
	o.runsMu.RLock()
	run, ok := o.runs[runID]
	o.runsMu.RUnlock()
	if !ok {
		return OrchestrationRunStatus{}, false
	}
	return cloneRunStatus(run), true
}
