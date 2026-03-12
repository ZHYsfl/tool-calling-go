// orchestration_runtime.go contains the RunManagedRace engine and internal
// state-management helpers. This is the "control plane loop" that drives
// dynamic task startup, cascading termination, event emission, and winner
// aggregation.
package toolcalling

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/openai/openai-go/v3"
)

// ---------------------------------------------------------------------------
// Internal: event emission
// ---------------------------------------------------------------------------

func (o *OrchestrationAgent) emit(ev OrchestrationEvent) {
	o.bus.Publish(ev)
}

// ---------------------------------------------------------------------------
// Internal: run state store
// ---------------------------------------------------------------------------

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

// ---------------------------------------------------------------------------
// RunManagedRace
// ---------------------------------------------------------------------------

// RunManagedRace executes race tasks with control-plane features:
//   - dynamic task startup
//   - realtime event bus updates
//   - task status table
//   - cascading termination with terminate/terminated_ack events
//   - winner aggregation and run status querying
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

	// ---- initialise run status ----
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

	// ---- launch workers ----
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

	// ---- collect results ----
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

	// ---- drain loop ----
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

	// ---- finalise ----
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
			"winner_task":    winnerTaskID,
			"winner_index":   winnerIndex,
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
