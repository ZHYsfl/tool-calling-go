package toolcalling

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/openai/openai-go/v3"
)

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

// OrchestrationAgent is a thin orchestration layer that coordinates multiple
// parallel sub-tasks on top of Agent + BatchRace.
//
// It is itself an agent-like wrapper (supports AddTool/RemoveTool/Chat) and
// can expose BatchRace as a callable tool via AddBatchRaceTool.
type OrchestrationAgent struct {
	controller *Agent
	worker     *Agent
	bus        *OrchestrationBus
	runs       map[string]*OrchestrationRunStatus
	runsMu     sync.RWMutex
	runSeq     uint64
}

// NewOrchestrationAgent creates an orchestration wrapper.
//
// By default, the same Agent is used for both:
// - controller: orchestrator's own chat/tools
// - worker: parallel sub-task execution by BatchRace
//
// You can call SetWorkerAgent to separate them.
func NewOrchestrationAgent(agent *Agent) *OrchestrationAgent {
	return &OrchestrationAgent{
		controller: agent,
		worker:     agent,
		bus:        newOrchestrationBus(),
		runs:       make(map[string]*OrchestrationRunStatus),
	}
}

func (o *OrchestrationAgent) nextRunID() string {
	seq := atomic.AddUint64(&o.runSeq, 1)
	return fmt.Sprintf("run_%d", seq)
}

// SetWorkerAgent configures a dedicated worker Agent used by BatchRace.
func (o *OrchestrationAgent) SetWorkerAgent(agent *Agent) {
	o.worker = agent
}

func (o *OrchestrationAgent) workerAgent() *Agent {
	if o.worker != nil {
		return o.worker
	}
	return o.controller
}

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

// BatchRaceToolConfig defines how AddBatchRaceTool exposes BatchRace as a tool
// on the orchestration controller.
type BatchRaceToolConfig struct {
	Name                 string
	Description          string
	DefaultMaxConcurrent int
	Parameters           map[string]any
}

func defaultBatchRaceToolParameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"tasks": map[string]any{
				"type":        "array",
				"description": "Task list. Each item: {id, prompt}",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"id": map[string]any{
							"type":        "string",
							"description": "Task ID used in winner output",
						},
						"prompt": map[string]any{
							"type":        "string",
							"description": "User prompt for this sub-task",
						},
					},
					"required": []any{"id", "prompt"},
				},
			},
			"success_substring": map[string]any{
				"type":        "string",
				"description": "Substring that marks a successful winner result",
			},
			"max_concurrent": map[string]any{
				"type":        "number",
				"description": "Optional override for max parallel workers",
			},
		},
		"required": []any{"tasks", "success_substring"},
	}
}

type raceTaskArg struct {
	ID     string `json:"id"`
	Prompt string `json:"prompt"`
}

func parseRaceTaskArgs(v any) ([]raceTaskArg, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("marshal tasks: %w", err)
	}
	var tasks []raceTaskArg
	if err := json.Unmarshal(raw, &tasks); err != nil {
		return nil, fmt.Errorf("invalid tasks payload: %w", err)
	}
	if len(tasks) == 0 {
		return nil, fmt.Errorf("tasks must not be empty")
	}
	for i, t := range tasks {
		if strings.TrimSpace(t.Prompt) == "" {
			return nil, fmt.Errorf("tasks[%d].prompt must not be empty", i)
		}
	}
	return tasks, nil
}

func lastAssistantString(messages []openai.ChatCompletionMessageParamUnion) string {
	if len(messages) == 0 {
		return ""
	}
	last := messages[len(messages)-1]
	if last.OfAssistant == nil || !last.OfAssistant.Content.OfString.Valid() {
		return ""
	}
	return last.OfAssistant.Content.OfString.Value
}

// AddBatchRaceTool registers a controller tool that executes BatchRace on the
// worker agent. This makes "orchestration via tools" explicit.
func (o *OrchestrationAgent) AddBatchRaceTool(cfg BatchRaceToolConfig) {
	name := cfg.Name
	if strings.TrimSpace(name) == "" {
		name = "batch_race"
	}
	desc := cfg.Description
	if strings.TrimSpace(desc) == "" {
		desc = "Run parallel sub-tasks and return first successful result"
	}
	params := cfg.Parameters
	if params == nil {
		params = defaultBatchRaceToolParameters()
	}

	o.controller.AddTool(Tool{
		Name:        name,
		Description: desc,
		Parameters:  params,
		Function: func(ctx context.Context, args map[string]any) (string, error) {
			rawTasks, ok := args["tasks"]
			if !ok {
				return "", fmt.Errorf("missing required arg: tasks")
			}
			taskArgs, err := parseRaceTaskArgs(rawTasks)
			if err != nil {
				return "", err
			}

			successSubstring, _ := args["success_substring"].(string)
			successSubstring = strings.TrimSpace(successSubstring)
			if successSubstring == "" {
				return "", fmt.Errorf("success_substring must not be empty")
			}
			target := strings.ToLower(successSubstring)

			observations := make([][]openai.ChatCompletionMessageParamUnion, len(taskArgs))
			taskIDs := make([]string, len(taskArgs))
			for i, t := range taskArgs {
				taskIDs[i] = t.ID
				observations[i] = []openai.ChatCompletionMessageParamUnion{
					openai.UserMessage(t.Prompt),
				}
			}

			raceOpts := make([]RaceOption, 0, 1)
			maxConcurrent := cfg.DefaultMaxConcurrent
			if rawMC, ok := args["max_concurrent"]; ok {
				if mc, ok := rawMC.(float64); ok {
					maxConcurrent = int(mc)
				}
			}
			if maxConcurrent > 0 {
				raceOpts = append(raceOpts, WithMaxConcurrent(maxConcurrent))
			}

			winner, err := BatchRace(
				ctx,
				o.workerAgent(),
				observations,
				func(msgs []openai.ChatCompletionMessageParamUnion) bool {
					return strings.Contains(strings.ToLower(lastAssistantString(msgs)), target)
				},
				raceOpts...,
			)
			if err != nil {
				return "", err
			}

			taskID := ""
			if winner.Index >= 0 && winner.Index < len(taskIDs) {
				taskID = taskIDs[winner.Index]
			}
			out, err := json.Marshal(map[string]any{
				"winner_index": winner.Index,
				"winner_task":  taskID,
				"answer":       lastAssistantString(winner.Messages),
			})
			if err != nil {
				return "", fmt.Errorf("marshal batch race result: %w", err)
			}
			return string(out), nil
		},
	})
}
