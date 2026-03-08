package toolcalling

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/openai/openai-go/v3"
)

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

type TaskState string

const (
	TaskPending    TaskState = "pending"
	TaskRunning    TaskState = "running"
	TaskFound      TaskState = "found"
	TaskNoMatch    TaskState = "no_match"
	TaskError      TaskState = "error"
	TaskTerminating TaskState = "terminating"
	TaskTerminated TaskState = "terminated"
	TaskTimeout    TaskState = "timeout"
)

type RunState string

const (
	RunRunning   RunState = "running"
	RunSucceeded RunState = "succeeded"
	RunFailed    RunState = "failed"
	RunTimeout   RunState = "timeout"
)

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

type OrchestrationRunConfig struct {
	SuccessCond        SuccessCondition
	MaxConcurrent      int
	TerminateAckTimeout time.Duration
	EventBuffer        int
}

type OrchestrationRunResult struct {
	RunID    string
	Winner   *OrchestrationRaceResult
	Status   OrchestrationRunStatus
}

type orchestrationResultItem struct {
	index     int
	taskID    string
	msgs      []openai.ChatCompletionMessageParamUnion
	err       error
	cancelled bool
}

type OrchestrationBus struct {
	mu          sync.RWMutex
	subscribers map[string]map[chan OrchestrationEvent]struct{}
}

func newOrchestrationBus() *OrchestrationBus {
	return &OrchestrationBus{
		subscribers: make(map[string]map[chan OrchestrationEvent]struct{}),
	}
}

func (b *OrchestrationBus) Subscribe(runID string, buffer int) (<-chan OrchestrationEvent, func()) {
	if buffer <= 0 {
		buffer = 64
	}
	ch := make(chan OrchestrationEvent, buffer)

	b.mu.Lock()
	if b.subscribers[runID] == nil {
		b.subscribers[runID] = make(map[chan OrchestrationEvent]struct{})
	}
	b.subscribers[runID][ch] = struct{}{}
	b.mu.Unlock()

	unsub := func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if subs, ok := b.subscribers[runID]; ok {
			if _, exists := subs[ch]; exists {
				delete(subs, ch)
				close(ch)
			}
			if len(subs) == 0 {
				delete(b.subscribers, runID)
			}
		}
	}
	return ch, unsub
}

func (b *OrchestrationBus) Publish(event OrchestrationEvent) {
	b.mu.RLock()
	subs := b.subscribers[event.RunID]
	for ch := range subs {
		select {
		case ch <- event:
		default:
			// Drop instead of blocking the orchestrator control path.
		}
	}
	allSubs := b.subscribers["*"]
	for ch := range allSubs {
		select {
		case ch <- event:
		default:
		}
	}
	b.mu.RUnlock()
}

func (o *OrchestrationAgent) SubscribeRun(runID string, buffer int) (<-chan OrchestrationEvent, func()) {
	return o.bus.Subscribe(runID, buffer)
}

// SubscribeAllRuns subscribes to events from all runs.
func (o *OrchestrationAgent) SubscribeAllRuns(buffer int) (<-chan OrchestrationEvent, func()) {
	return o.bus.Subscribe("*", buffer)
}

func (o *OrchestrationAgent) emit(ev OrchestrationEvent) {
	if ev.At.IsZero() {
		ev.At = time.Now()
	}
	o.bus.Publish(ev)
}

func (o *OrchestrationAgent) storeRun(status *OrchestrationRunStatus) {
	o.runsMu.Lock()
	o.runs[status.RunID] = status
	o.runsMu.Unlock()
}

func cloneRunStatus(in *OrchestrationRunStatus) OrchestrationRunStatus {
	out := *in
	out.Tasks = make(map[string]TaskStatus, len(in.Tasks))
	for k, v := range in.Tasks {
		out.Tasks[k] = v
	}
	if len(in.ErrorSummary) > 0 {
		out.ErrorSummary = append([]string(nil), in.ErrorSummary...)
	}
	return out
}

func (o *OrchestrationAgent) GetRunStatus(runID string) (OrchestrationRunStatus, bool) {
	o.runsMu.RLock()
	run, ok := o.runs[runID]
	o.runsMu.RUnlock()
	if !ok {
		return OrchestrationRunStatus{}, false
	}
	return cloneRunStatus(run), true
}

func (o *OrchestrationAgent) updateTask(
	runID string,
	taskID string,
	mutate func(*TaskStatus),
) {
	o.runsMu.Lock()
	defer o.runsMu.Unlock()
	run, ok := o.runs[runID]
	if !ok {
		return
	}
	st, ok := run.Tasks[taskID]
	if !ok {
		return
	}
	mutate(&st)
	st.UpdatedAt = time.Now()
	run.Tasks[taskID] = st
}

func (o *OrchestrationAgent) appendRunError(runID, errMsg string) {
	o.runsMu.Lock()
	defer o.runsMu.Unlock()
	if run, ok := o.runs[runID]; ok {
		run.ErrorSummary = append(run.ErrorSummary, errMsg)
	}
}

func (o *OrchestrationAgent) markRunCompletion(
	runID string,
	state RunState,
	winnerTaskID string,
	winnerIndex int,
) OrchestrationRunStatus {
	o.runsMu.Lock()
	defer o.runsMu.Unlock()
	run := o.runs[runID]
	run.State = state
	run.EndedAt = time.Now()
	run.WinnerTaskID = winnerTaskID
	run.WinnerIndex = winnerIndex
	return cloneRunStatus(run)
}

// RunManagedRace executes race tasks with control-plane features:
// - dynamic task startup
// - realtime event bus updates
// - task status table
// - cascading termination with terminate/terminated_ack events
// - winner aggregation and run status querying
func (o *OrchestrationAgent) RunManagedRace(
	parentCtx context.Context,
	tasks []RaceTask,
	cfg OrchestrationRunConfig,
) (*OrchestrationRunResult, error) {
	if len(tasks) == 0 {
		return nil, fmt.Errorf("tasks must not be empty")
	}
	if cfg.SuccessCond == nil {
		return nil, fmt.Errorf("SuccessCond must not be nil")
	}
	if cfg.TerminateAckTimeout <= 0 {
		cfg.TerminateAckTimeout = 10 * time.Second
	}

	runID := o.nextRunID()
	runStatus := &OrchestrationRunStatus{
		RunID:       runID,
		State:       RunRunning,
		StartedAt:   time.Now(),
		WinnerIndex: -1,
		Tasks:       make(map[string]TaskStatus, len(tasks)),
	}
	for i, t := range tasks {
		taskID := t.ID
		if taskID == "" {
			taskID = fmt.Sprintf("task_%d", i)
			tasks[i].ID = taskID
		}
		runStatus.Tasks[taskID] = TaskStatus{
			ID:        taskID,
			Index:     i,
			State:     TaskPending,
			UpdatedAt: time.Now(),
		}
	}
	o.storeRun(runStatus)
	o.emit(OrchestrationEvent{
		RunID:   runID,
		Type:    EventRunStarted,
		Message: "run started",
	})

	ctx, cancel := context.WithCancel(parentCtx)
	defer cancel()

	var sem chan struct{}
	if cfg.MaxConcurrent > 0 {
		sem = make(chan struct{}, cfg.MaxConcurrent)
	}

	resultCh := make(chan orchestrationResultItem, len(tasks))
	var wg sync.WaitGroup

	for i, task := range tasks {
		wg.Add(1)
		go func(idx int, t RaceTask) {
			defer wg.Done()
			if sem != nil {
				sem <- struct{}{}
				defer func() { <-sem }()
			}

			o.updateTask(runID, t.ID, func(ts *TaskStatus) {
				ts.State = TaskRunning
				ts.LastMessage = "task started"
			})
			o.emit(OrchestrationEvent{
				RunID:     runID,
				TaskID:    t.ID,
				TaskIndex: idx,
				Type:      EventTaskStarted,
				Message:   "task started",
			})

			taskCtx := WithProgressReporter(ctx, func(message string, data map[string]any) {
				o.updateTask(runID, t.ID, func(ts *TaskStatus) { ts.LastMessage = message })
				o.emit(OrchestrationEvent{
					RunID:     runID,
					TaskID:    t.ID,
					TaskIndex: idx,
					Type:      EventTaskProgress,
					Message:   message,
					Data:      data,
				})
			})

			msgs, err := o.workerExecutor().ExecuteTask(taskCtx, o.workerAgent(), t)
			if err != nil {
				if ctx.Err() != nil {
					resultCh <- orchestrationResultItem{
						index: idx, taskID: t.ID, err: ctx.Err(), cancelled: true,
					}
					return
				}
				resultCh <- orchestrationResultItem{
					index: idx, taskID: t.ID, err: err,
				}
				return
			}
			resultCh <- orchestrationResultItem{
				index: idx, taskID: t.ID, msgs: msgs,
			}
		}(i, task)
	}

	go func() {
		wg.Wait()
		close(resultCh)
	}()

	done := make([]bool, len(tasks))
	doneCount := 0
	winnerIndex := -1
	winnerTaskID := ""
	var winnerMsgs []openai.ChatCompletionMessageParamUnion
	terminatedSent := 0
	terminatedAck := 0
	waitingForAck := false
	var ackTimer <-chan time.Time

	handleResult := func(r orchestrationResultItem) {
		if !done[r.index] {
			done[r.index] = true
			doneCount++
		}

		if r.cancelled {
			terminatedAck++
			o.updateTask(runID, r.taskID, func(ts *TaskStatus) {
				ts.State = TaskTerminated
				ts.LastMessage = "terminated by orchestration"
			})
			o.emit(OrchestrationEvent{
				RunID:     runID,
				TaskID:    r.taskID,
				TaskIndex: r.index,
				Type:      EventTerminatedAck,
				Message:   "terminated ack",
			})
			return
		}

		if r.err != nil {
			o.appendRunError(runID, fmt.Sprintf("%s: %v", r.taskID, r.err))
			o.updateTask(runID, r.taskID, func(ts *TaskStatus) {
				ts.State = TaskError
				ts.LastError = r.err.Error()
				ts.LastMessage = "task failed"
			})
			o.emit(OrchestrationEvent{
				RunID:     runID,
				TaskID:    r.taskID,
				TaskIndex: r.index,
				Type:      EventTaskError,
				Message:   r.err.Error(),
			})
			return
		}

		if cfg.SuccessCond(r.msgs) {
			o.updateTask(runID, r.taskID, func(ts *TaskStatus) {
				ts.State = TaskFound
				ts.LastMessage = "target found"
			})
			o.emit(OrchestrationEvent{
				RunID:     runID,
				TaskID:    r.taskID,
				TaskIndex: r.index,
				Type:      EventTargetFound,
				Message:   "target found",
			})

			if winnerIndex == -1 {
				winnerIndex = r.index
				winnerTaskID = r.taskID
				winnerMsgs = r.msgs
				cancel()

				for i, t := range tasks {
					if i == winnerIndex || done[i] {
						continue
					}
					terminatedSent++
					o.updateTask(runID, t.ID, func(ts *TaskStatus) {
						if ts.State == TaskRunning {
							ts.State = TaskTerminating
							ts.LastMessage = "terminate sent"
						}
					})
					o.emit(OrchestrationEvent{
						RunID:     runID,
						TaskID:    t.ID,
						TaskIndex: i,
						Type:      EventTerminateSent,
						Message:   fmt.Sprintf("terminate reason: target_found_by_%s", winnerTaskID),
					})
				}

				waitingForAck = true
				ackTimer = time.After(cfg.TerminateAckTimeout)
			}
			return
		}

		o.updateTask(runID, r.taskID, func(ts *TaskStatus) {
			ts.State = TaskNoMatch
			ts.LastMessage = "task completed without match"
		})
		o.emit(OrchestrationEvent{
			RunID:     runID,
			TaskID:    r.taskID,
			TaskIndex: r.index,
			Type:      EventTaskCompleted,
			Message:   "completed without match",
		})
	}

	for doneCount < len(tasks) {
		if waitingForAck {
			select {
			case r, ok := <-resultCh:
				if !ok {
					doneCount = len(tasks)
					break
				}
				handleResult(r)
			case <-ackTimer:
				o.appendRunError(runID, "terminate ack timeout reached")
				for i, t := range tasks {
					if i == winnerIndex || done[i] {
						continue
					}
					o.updateTask(runID, t.ID, func(ts *TaskStatus) {
						if ts.State == TaskTerminating || ts.State == TaskRunning {
							ts.State = TaskTimeout
							ts.LastMessage = "terminate ack timeout"
						}
					})
				}
				doneCount = len(tasks)
			}
		} else {
			r, ok := <-resultCh
			if !ok {
				break
			}
			handleResult(r)
		}
	}

	var finalState RunState
	if winnerIndex >= 0 {
		finalState = RunSucceeded
	} else if parentCtx.Err() == context.DeadlineExceeded {
		finalState = RunTimeout
	} else {
		finalState = RunFailed
	}

	o.runsMu.Lock()
	if run, ok := o.runs[runID]; ok {
		run.TerminateSent = terminatedSent
		run.TerminateAck = terminatedAck
	}
	o.runsMu.Unlock()

	finalStatus := o.markRunCompletion(runID, finalState, winnerTaskID, winnerIndex)
	o.emit(OrchestrationEvent{
		RunID:   runID,
		Type:    EventRunCompleted,
		Message: fmt.Sprintf("run completed with state=%s", finalState),
		Data: map[string]any{
			"winner_task":   winnerTaskID,
			"winner_index":  winnerIndex,
			"terminate_sent": terminatedSent,
			"terminate_ack":  terminatedAck,
		},
	})

	if winnerIndex >= 0 {
		return &OrchestrationRunResult{
			RunID: runID,
			Winner: &OrchestrationRaceResult{
				TaskID:   winnerTaskID,
				Index:    winnerIndex,
				Messages: winnerMsgs,
			},
			Status: finalStatus,
		}, nil
	}

	return &OrchestrationRunResult{
		RunID:  runID,
		Winner: nil,
		Status: finalStatus,
	}, fmt.Errorf("run %s completed without winner", runID)
}
