# ToolCallingGO SDK Documentation

ToolCallingGO is a Go SDK for calling tools in LLM, based on [openai-go/v3](https://github.com/openai/openai-go).

## LLMConfig

LLMConfig is the configuration for the LLM.

```go
type LLMConfig struct {
	APIKey    string
	Model     string
	BaseURL   string
	ExtraBody map[string]any
}
```

## ToolFunc

ToolFunc is the function for the Tool, it is used to call the tool in the Agent.

```go
type ToolFunc func(ctx context.Context, args map[string]any) (string, error)
```

## Tool

Tool is the tool for the Agent.

```go
type Tool struct {
	Name        string
	Description string
	Function    ToolFunc
	Parameters  map[string]any
}
```

## toolExecResult

toolExecResult is the result of the tool execution.

```go
type toolExecResult struct {
	message   openai.ChatCompletionMessageParamUnion
	status    string // "success" | "error"
	errorType string // "parse_error" | "not_found" | "arg_error" | "exec_error"
	content   string
}
```

## AgentOption

AgentOption is the option for the Agent.

```go
type AgentOption func(*Agent)
```

WithMaxToolRetries is the option for the maximum number of tool retries.
WithDebug is the option for the debug mode.

```go
func WithMaxToolRetries(n int) AgentOption {
	return func(a *Agent) { a.maxToolRetries = n }
}
func WithDebug(debug bool) AgentOption {
	return func(a *Agent) { a.debug = debug }
}
```

## Agent

Agent is the core unit of the ToolCallingGO SDK.

```go
type Agent struct {
	client         openai.Client
	tools          []Tool
	config         LLMConfig
	debug          bool
	maxToolRetries int
}
```

NewAgent is the constructor for the Agent, it creates a new Agent with the given configuration and options.

```go
func NewAgent(config LLMConfig, opts ...AgentOption) *Agent
```

AddTool is the method to add a tool to the Agent.

```go
func (a *Agent) AddTool(tool Tool)
```

RemoveTool is the method to remove a tool from the Agent by name.

```go
func (a *Agent) RemoveTool(name string)
```

getTools is the method to get the tools of the Agent.

```go
func (a *Agent) getTools() []openai.ChatCompletionToolUnionParam
```

executeToolCall is the method to execute a tool call.

```go
func (a *Agent) executeToolCall(
	ctx context.Context,
	toolCall openai.ChatCompletionMessageToolCallUnion,
	availableFunctions map[string]ToolFunc,
) toolExecResult
```

getToolResponseObservations is the method to asynchronously execute the tool calls,and store the results in a slice of toolExecResult.

```go
func (a *Agent) getToolResponseObservations(
	ctx context.Context,
	toolCalls []openai.ChatCompletionMessageToolCallUnion,
) []toolExecResult
```

Chat is the method to chat with the Agent, it handles the tool-call loop automatically and returns the final messages.

```go
func (a *Agent) Chat(
	ctx context.Context,
	messages []openai.ChatCompletionMessageParamUnion,
) ([]openai.ChatCompletionMessageParamUnion, error)
```

## assistantMsgToParam

assistantMsgToParam is the method to convert a response ChatCompletionMessage into a request ChatCompletionMessageParamUnion so it can be appended back to the conversation history. This is the Go equivalent of Python's response.choices[0].message.model_dump().

```go
func assistantMsgToParam(msg openai.ChatCompletionMessage) openai.ChatCompletionMessageParamUnion
```

## indexedResult

indexedResult is the result of the batch execution, it contains the index of the observation(or task/agent), the messages of the chat and the error if any.

```go
type indexedResult struct {
	index    int
	messages []openai.ChatCompletionMessageParamUnion
	err      error
}
```

## Batch

Batch is the method to run multiple chats concurrently and asynchronously.

```
func Batch(
	ctx context.Context,
	agent *Agent,
	observations [][]openai.ChatCompletionMessageParamUnion,
	maxConcurrent int,
) ([][]openai.ChatCompletionMessageParamUnion, error) 
```

## SuccessCondition

SuccessCondition is the condition for the race execution, it is a function that takes the messages of the chat and returns true if the condition is satisfied.

```go
type SuccessCondition func(messages []openai.ChatCompletionMessageParamUnion) bool
```

## RaceEventType

RaceEventType is the event type for the race execution, it is a string that represents the type of the event.

```go
type RaceEventType string
```

`RaceEventType` has the following const values:

- `RaceEventStarted`: indicates an agent goroutine has started.
- `RaceEventSuccess`: indicates an agent satisfied SuccessCondition.
- `RaceEventNoMatch`: indicates an agent finished normally without a match.
- `RaceEventError`: indicates an agent finished with a non-cancellation error.
- `RaceEventCancelled`: indicates an agent was stopped by cascading cancellation.

```go
const (
RaceEventStarted RaceEventType = "started"
RaceEventSuccess RaceEventType = "success"
RaceEventNoMatch RaceEventType = "no_match"
RaceEventError RaceEventType = "error"
RaceEventCancelled RaceEventType = "cancelled"
)
```

## RaceEvent

RaceEvent carries a lightweight progress update from a BatchRace goroutine to the caller's event handler, it contains the type of the event, the agent ID and the message of the event.

```go
type RaceEvent struct {
	Type    RaceEventType
	AgentID int
	Message string
}
```

## raceConfig

raceConfig is the configuration for the race execution, it contains the maximum number of concurrent agents and the event handler.

```go
type raceConfig struct {
	maxConcurrent int
	eventHandler  func(RaceEvent)
}
```

## RaceOption

RaceOption is the option for the race execution, it is a function that takes the raceConfig and returns the raceConfig.

```go
type RaceOption func(*raceConfig)
```

WithMaxConcurrent is the option for the maximum number of concurrent agents.
WithEventHandler is the option for the event handler.

```go
func WithMaxConcurrent(n int) RaceOption {
	return func(c *raceConfig) { c.maxConcurrent = n }
}
func WithEventHandler(fn func(RaceEvent)) RaceOption {
	return func(c *raceConfig) { c.eventHandler = fn }
}
```

## raceItem

raceItem is the item of the race execution, it contains the index of the observation(or task/agent), the messages of the chat and the error if any.

```go
type raceItem struct {
	index int
	msgs  []openai.ChatCompletionMessageParamUnion
	err   error
}
```

## RaceResult

RaceResult is the result of the race execution, it contains the index of the agent that satisfied the SuccessCondition and the messages of the chat.In compare to the Batch execution, the Race execution returns the result of the first agent that satisfied the SuccessCondition,so it is more efficient and add a field `Index` to the result.

```go
type RaceResult struct {
	Index    int
	Messages []openai.ChatCompletionMessageParamUnion
}
```

## BatchRace

BatchRace is the method to run multiple Agent.Chat sessions concurrently and returns the result of the FIRST agent whose output satisfies successCond. The moment a winner is found, all other in-flight agents are terminated via context cancellation (cascading termination).If no agent satisfies the condition and all finish or fail, an error is returned summarising the outcome.

```go
func BatchRace(
	parentCtx context.Context,
	agent *Agent,
	observations [][]openai.ChatCompletionMessageParamUnion,
	successCond SuccessCondition,
	opts ...RaceOption,
) (*RaceResult, error)
```

