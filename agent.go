// Package toolcalling provides a lightweight LLM agent with parallel tool
// calling and automatic retry, built on top of openai-go/v3.
//
// This is a 1:1 Go port of the Python tool_calling SDK
// (async_tool_calling.py).
package toolcalling

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/shared"
)

// ---------------------------------------------------------------------------
// Types  (← Python LLMConfig, Tool)
// ---------------------------------------------------------------------------

type LLMConfig struct {
	APIKey    string
	Model     string
	BaseURL   string
	ExtraBody map[string]any
}

type ToolFunc func(ctx context.Context, args map[string]any) (string, error)

type Tool struct {
	Name        string
	Description string
	Function    ToolFunc
	Parameters  map[string]any
}

// ---------------------------------------------------------------------------
// Agent options  (← Python __init__ kwargs)
// ---------------------------------------------------------------------------

type AgentOption func(*Agent)

func WithMaxToolRetries(n int) AgentOption {
	return func(a *Agent) { a.maxToolRetries = n }
}

func WithDebug(debug bool) AgentOption {
	return func(a *Agent) { a.debug = debug }
}

// ---------------------------------------------------------------------------
// Internal: tool execution result  (← Python _tool_status / _error_type)
// ---------------------------------------------------------------------------

type toolExecResult struct {
	message   openai.ChatCompletionMessageParamUnion
	status    string // "success" | "error"
	errorType string // "parse_error" | "not_found" | "arg_error" | "exec_error"
	content   string
}

// ---------------------------------------------------------------------------
// Agent  (← Python class Agent)
// ---------------------------------------------------------------------------

type Agent struct {
	client         openai.Client
	tools          []Tool
	config         LLMConfig
	debug          bool
	maxToolRetries int
}

func NewAgent(config LLMConfig, opts ...AgentOption) *Agent {
	clientOpts := []option.RequestOption{
		option.WithAPIKey(config.APIKey),
	}
	if config.BaseURL != "" {
		clientOpts = append(clientOpts, option.WithBaseURL(config.BaseURL))
	}
	client := openai.NewClient(clientOpts...)

	a := &Agent{
		client:         client,
		tools:          nil,
		config:         config,
		debug:          false,
		maxToolRetries: 3,
	}
	for _, o := range opts {
		o(a)
	}
	return a
}

func (a *Agent) AddTool(tool Tool) {
	a.tools = append(a.tools, tool)
}

func (a *Agent) RemoveTool(name string) {
	filtered := make([]Tool, 0, len(a.tools))
	for _, t := range a.tools {
		if t.Name != name {
			filtered = append(filtered, t)
		}
	}
	a.tools = filtered
}

// ---------------------------------------------------------------------------
// Private helpers  (← Python _get_tools, _execute_tool_call, etc.)
// ---------------------------------------------------------------------------

func (a *Agent) getTools() []openai.ChatCompletionToolUnionParam {
	out := make([]openai.ChatCompletionToolUnionParam, len(a.tools))
	for i, t := range a.tools {
		out[i] = openai.ChatCompletionFunctionTool(shared.FunctionDefinitionParam{
			Name:        t.Name,
			Description: openai.String(t.Description),
			Parameters:  shared.FunctionParameters(t.Parameters),
		})
	}
	return out
}

func (a *Agent) executeToolCall(
	ctx context.Context,
	toolCall openai.ChatCompletionMessageToolCallUnion,
	availableFunctions map[string]ToolFunc,
) toolExecResult {
	functionTC := toolCall.AsFunction()
	if functionTC.ID == "" {
		content := "[NOT_FOUND] Unsupported tool call type (only function tool calls are supported)"
		if a.debug {
			fmt.Printf("[ERROR] %s\n", content)
		}
		return toolExecResult{
			message:   openai.ToolMessage(content, "unsupported-tool-call"),
			status:    "error",
			errorType: "not_found",
			content:   content,
		}
	}

	toolCallID := functionTC.ID
	funcName := functionTC.Function.Name
	rawArgs := functionTC.Function.Arguments

	var args map[string]any
	if rawArgs != "" && strings.TrimSpace(rawArgs) != "" && rawArgs != "{}" {
		if err := json.Unmarshal([]byte(rawArgs), &args); err != nil {
			content := fmt.Sprintf("[PARSE_ERROR] JSON parse failed: %v. Raw: '%s'", err, rawArgs)
			if a.debug {
				fmt.Printf("[ERROR] %s\n", content)
			}
			return toolExecResult{
				message:   openai.ToolMessage(content, toolCallID),
				status:    "error",
				errorType: "parse_error",
				content:   content,
			}
		}
	}
	if args == nil {
		args = make(map[string]any)
	}

	fn, ok := availableFunctions[funcName]
	if !ok {
		content := fmt.Sprintf("[NOT_FOUND] Function '%s' not found", funcName)
		if a.debug {
			fmt.Printf("[ERROR] %s\n", content)
		}
		return toolExecResult{
			message:   openai.ToolMessage(content, toolCallID),
			status:    "error",
			errorType: "not_found",
			content:   content,
		}
	}

	if a.debug {
		keys := make([]string, 0, len(args))
		for k := range args {
			keys = append(keys, k)
		}
		fmt.Printf("[DEBUG] Executing %s with args: %v\n", funcName, keys)
	}

	result, err := fn(ctx, args)
	if err != nil {
		content := fmt.Sprintf("[EXEC_ERROR] Execution failed: %v", err)
		if a.debug {
			fmt.Printf("[ERROR] %s\n", content)
		}
		return toolExecResult{
			message:   openai.ToolMessage(content, toolCallID),
			status:    "error",
			errorType: "exec_error",
			content:   content,
		}
	}

	if a.debug {
		fmt.Printf("[DEBUG] Tool %s returned (id=%s): %s\n", funcName, toolCallID, result)
	}

	return toolExecResult{
		message: openai.ToolMessage(result, toolCallID),
		status:  "success",
		content: result,
	}
}

func (a *Agent) getToolResponseObservations(
	ctx context.Context,
	toolCalls []openai.ChatCompletionMessageToolCallUnion,
) []toolExecResult {
	availableFunctions := make(map[string]ToolFunc, len(a.tools))
	for _, t := range a.tools {
		availableFunctions[t.Name] = t.Function
	}

	results := make([]toolExecResult, len(toolCalls))
	var wg sync.WaitGroup
	for i, tc := range toolCalls {
		wg.Add(1)
		go func(idx int, call openai.ChatCompletionMessageToolCallUnion) {
			defer wg.Done()
			results[idx] = a.executeToolCall(ctx, call, availableFunctions)
		}(i, tc)
	}
	wg.Wait()
	return results
}

func hasToolErrors(results []toolExecResult) bool {
	for _, r := range results {
		if r.status == "error" {
			return true
		}
	}
	return false
}

func getErrorSummary(results []toolExecResult) string {
	var parts []string
	for _, r := range results {
		if r.status == "error" {
			c := r.content
			if len(c) > 200 {
				c = c[:200]
			}
			parts = append(parts, fmt.Sprintf("- %s: %s", r.errorType, c))
		}
	}
	if len(parts) == 0 {
		return "unknown error"
	}
	return strings.Join(parts, "\n")
}

// assistantMsgToParam converts a response ChatCompletionMessage into a
// request ChatCompletionMessageParamUnion so it can be appended back to the
// conversation history. This is the Go equivalent of Python's
// response.choices[0].message.model_dump().
func assistantMsgToParam(msg openai.ChatCompletionMessage) openai.ChatCompletionMessageParamUnion {
	var tcParams []openai.ChatCompletionMessageToolCallUnionParam
	for _, tc := range msg.ToolCalls {
		tcParams = append(tcParams, tc.ToParam())
	}

	asst := &openai.ChatCompletionAssistantMessageParam{
		ToolCalls: tcParams,
	}
	if msg.Content != "" {
		asst.Content = openai.ChatCompletionAssistantMessageParamContentUnion{
			OfString: openai.String(msg.Content),
		}
	}
	return openai.ChatCompletionMessageParamUnion{OfAssistant: asst}
}

// ---------------------------------------------------------------------------
// Chat  (← Python Agent.chat)
// ---------------------------------------------------------------------------

func (a *Agent) Chat(
	ctx context.Context,
	messages []openai.ChatCompletionMessageParamUnion,
) ([]openai.ChatCompletionMessageParamUnion, error) {

	params := openai.ChatCompletionNewParams{
		Model:    openai.ChatModel(a.config.Model),
		Messages: messages,
		Tools:    a.getTools(),
		ToolChoice: openai.ChatCompletionToolChoiceOptionUnionParam{
			OfAuto: openai.String("auto"),
		},
	}
	if a.config.ExtraBody != nil {
		params.SetExtraFields(a.config.ExtraBody)
	}

	resp, err := a.client.Chat.Completions.New(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("chat completion: %w", err)
	}

	next := make([]openai.ChatCompletionMessageParamUnion, len(messages))
	copy(next, messages)

	retryCount := 0

	for resp.Choices[0].FinishReason == "tool_calls" {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		next = append(next, assistantMsgToParam(resp.Choices[0].Message))

		toolResults := a.getToolResponseObservations(ctx, resp.Choices[0].Message.ToolCalls)
		for _, tr := range toolResults {
			next = append(next, tr.message)
		}

		if hasToolErrors(toolResults) && retryCount < a.maxToolRetries {
			retryCount++
			summary := getErrorSummary(toolResults)
			if a.debug {
				fmt.Printf("[RETRY %d/%d] Tool errors:\n%s\n", retryCount, a.maxToolRetries, summary)
			}
			next = append(next, openai.UserMessage(
				fmt.Sprintf(
					"[System] Tool execution errors detected:\n\n%s\n\nPlease fix and retry. Retries left: %d",
					summary, a.maxToolRetries-retryCount,
				),
			))
		}

		if a.debug {
			jsonMsgs, _ := json.MarshalIndent(next, "", "  ")
			fmt.Printf("[DEBUG] Sending %d messages to LLM:\n%s\n", len(next), string(jsonMsgs))
		}

		params.Messages = next
		resp, err = a.client.Chat.Completions.New(ctx, params)
		if err != nil {
			return nil, fmt.Errorf("chat completion (tool loop): %w", err)
		}
	}

	next = append(next, assistantMsgToParam(resp.Choices[0].Message))
	return next, nil
}
