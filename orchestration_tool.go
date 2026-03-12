// orchestration_tool.go contains AddBatchRaceTool and its helper functions.
// This file is solely responsible for registering BatchRace as a callable tool
// on the orchestration controller agent.
package toolcalling

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/openai/openai-go/v3"
)

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
