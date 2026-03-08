package toolcalling

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/openai/openai-go/v3"
)

// RaceResult holds the output from the first agent that satisfies the
// SuccessCondition in a BatchRace call.
type RaceResult struct {
	Index    int
	Messages []openai.ChatCompletionMessageParamUnion
}

// SuccessCondition is a user-supplied predicate that inspects an agent's
// completed message history and returns true when the agent has found
// the target (or otherwise "won" the race).
type SuccessCondition func(messages []openai.ChatCompletionMessageParamUnion) bool

// RaceEventType represents a BatchRace progress event type.
type RaceEventType string

// RaceEvent type values emitted by BatchRace.
const (
	// RaceEventStarted indicates an agent goroutine has started.
	RaceEventStarted RaceEventType = "started"
	// RaceEventSuccess indicates an agent satisfied SuccessCondition.
	RaceEventSuccess RaceEventType = "success"
	// RaceEventNoMatch indicates an agent finished normally without a match.
	RaceEventNoMatch RaceEventType = "no_match"
	// RaceEventError indicates an agent finished with a non-cancellation error.
	RaceEventError RaceEventType = "error"
	// RaceEventCancelled indicates an agent was stopped by cascading cancellation.
	RaceEventCancelled RaceEventType = "cancelled"
)

// RaceEvent carries a lightweight progress update from a BatchRace goroutine
// to the caller's event handler.
type RaceEvent struct {
	// Type is one of the RaceEvent* constants above.
	Type    RaceEventType
	AgentID int
	Message string
}

// ---------------------------------------------------------------------------
// Options
// ---------------------------------------------------------------------------

type raceConfig struct {
	maxConcurrent int
	eventHandler  func(RaceEvent)
}

// RaceOption configures optional behaviour for BatchRace.
type RaceOption func(*raceConfig)

// WithMaxConcurrent limits how many agents may run simultaneously.
// A value <= 0 means unlimited (one goroutine per observation).
func WithMaxConcurrent(n int) RaceOption {
	return func(c *raceConfig) { c.maxConcurrent = n }
}

// WithEventHandler registers a callback that receives real-time progress
// events from every agent goroutine. The handler is called synchronously
// inside the goroutine, so keep it fast (e.g. fmt.Println).
func WithEventHandler(fn func(RaceEvent)) RaceOption {
	return func(c *raceConfig) { c.eventHandler = fn }
}

// ---------------------------------------------------------------------------
// BatchRace
// ---------------------------------------------------------------------------

type raceItem struct {
	index int
	msgs  []openai.ChatCompletionMessageParamUnion
	err   error
}

// BatchRace runs multiple Agent.Chat sessions concurrently and returns the
// result of the FIRST agent whose output satisfies successCond. The moment a
// winner is found, all other in-flight agents are terminated via context
// cancellation (cascading termination).
//
// If no agent satisfies the condition and all finish or fail, an error is
// returned summarising the outcome.
func BatchRace(
	parentCtx context.Context,
	agent *Agent,
	observations [][]openai.ChatCompletionMessageParamUnion,
	successCond SuccessCondition,
	opts ...RaceOption,
) (*RaceResult, error) {

	if len(observations) == 0 {
		return nil, fmt.Errorf("observations must not be empty")
	}

	cfg := raceConfig{}
	for _, o := range opts {
		o(&cfg)
	}
	emit := cfg.eventHandler
	if emit == nil {
		emit = func(RaceEvent) {} // no-op
	}

	ctx, cancel := context.WithCancel(parentCtx)
	defer cancel()

	// Buffered so goroutines never block after cancellation.
	resultCh := make(chan raceItem, len(observations))

	var sem chan struct{}
	if cfg.maxConcurrent > 0 {
		sem = make(chan struct{}, cfg.maxConcurrent)
	}

	var wg sync.WaitGroup
	for i := range observations {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			if sem != nil {
				sem <- struct{}{}
				defer func() { <-sem }()
			}

			emit(RaceEvent{Type: RaceEventStarted, AgentID: idx, Message: fmt.Sprintf("agent %d started", idx)})

			msgs, err := agent.Chat(ctx, observations[idx])

			if err != nil {
				if ctx.Err() != nil {
					emit(RaceEvent{Type: RaceEventCancelled, AgentID: idx, Message: fmt.Sprintf("agent %d cancelled", idx)})
					resultCh <- raceItem{index: idx, err: ctx.Err()}
					return
				}
				emit(RaceEvent{Type: RaceEventError, AgentID: idx, Message: fmt.Sprintf("agent %d error: %v", idx, err)})
				resultCh <- raceItem{index: idx, err: err}
				return
			}

			if successCond(msgs) {
				emit(RaceEvent{Type: RaceEventSuccess, AgentID: idx, Message: fmt.Sprintf("agent %d found target", idx)})
				resultCh <- raceItem{index: idx, msgs: msgs}
				cancel() // cascading termination
				return
			}

			emit(RaceEvent{Type: RaceEventNoMatch, AgentID: idx, Message: fmt.Sprintf("agent %d finished without match", idx)})
			resultCh <- raceItem{index: idx, msgs: msgs}
		}(i)
	}

	go func() {
		wg.Wait()
		close(resultCh)
	}()

	var errs []string
	for r := range resultCh {
		if r.err != nil {
			if r.err == context.Canceled {
				continue
			}
			errs = append(errs, fmt.Sprintf("agent %d: %v", r.index, r.err))
			continue
		}
		if successCond(r.msgs) {
			cancel()
			return &RaceResult{Index: r.index, Messages: r.msgs}, nil
		}
	}

	if len(errs) > 0 {
		return nil, fmt.Errorf("no agent matched; %d errors:\n%s", len(errs), strings.Join(errs, "\n"))
	}
	return nil, fmt.Errorf("all %d agents completed but none matched the success condition", len(observations))
}
