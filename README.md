# tool_calling_go

A Go implementation of the `tool_calling` SDK. Built on [openai-go/v3](https://github.com/openai/openai-go), compatible with any OpenAI-compatible API (OpenAI, DeepSeek, vLLM, Ollama, etc.).

## Features

- **Agent** — LLM chat with automatic tool-call loop and error retry
- **Batch** — Run many Agent.Chat sessions concurrently with bounded parallelism
- **Parallel tool execution** — Multiple tool calls in a single turn are dispatched via goroutines

## Directory Layout

```
tool_calling_go/
├── agent.go              # Core: LLMConfig / Tool / Agent / Chat
├── batch.go              # Batch concurrent dispatch
├── go.mod / go.sum       # Dependencies
├── .env.example                  # Environment variables (API_KEY / MODEL / BASE_URL)
└── example/
    ├── getweather/       # Single-chat example
    └── batchuse/         # 50-way concurrent batch example
```

## Quick Start

### 1. Configure environment

Create a `.env` in the `tool_calling_go/` directory, and copy the content of `.env.example`:

```bash
cp .env.example .env
```

and fill in the values:

```env
API_KEY=sk-your-api-key
MODEL=your-model-name
BASE_URL=your-base-url
```

### 2. Run examples

```bash
cd tool_calling_go

# Single chat (LLM calls get_weather twice in parallel)
go run ./example/getweather

# Batch (50 concurrent chats)
go run ./example/batchuse
```

## API Reference

### LLMConfig

```go
type LLMConfig struct {
    APIKey    string            // API key
    Model     string            // Model name
    BaseURL   string            // API endpoint (empty = OpenAI default)
    ExtraBody map[string]any    // Vendor-specific params (e.g. Kimi thinking toggle)
}
```

### Tool

```go
type Tool struct {
    Name        string              // Tool name
    Description string              // Description shown to the LLM
    Function    ToolFunc            // Actual implementation
    Parameters  map[string]any      // JSON Schema for parameters
}

type ToolFunc func(args map[string]any) (string, error)
```

### Agent

```go
// Create
agent := NewAgent(config)
agent := NewAgent(config, WithDebug(true), WithMaxToolRetries(5))

// Register / remove tools
agent.AddTool(tool)
agent.RemoveTool("tool_name")

// Chat (handles tool-call loop automatically)
messages, err := agent.Chat(ctx, []openai.ChatCompletionMessageParamUnion{
    openai.UserMessage("your question"),
})
```

### Batch

```go
// Run multiple chats concurrently; maxConcurrent controls parallelism
results, err := Batch(ctx, agent, observations, maxConcurrent)
```

## How It Works

```
User message
  │
  ▼
Agent.Chat() ──→ LLM API
  │                 │
  │     ◄───────────┘  finish_reason == "tool_calls"
  │
  ├─ Execute all tool calls in parallel (goroutines)
  ├─ Append tool results to conversation
  ├─ If errors and retries remaining, append error hint
  │
  └─ Call LLM API again ──→ ... (loop until finish_reason != "tool_calls")
  │
  ▼
Return full conversation history
```

## Dependencies

- Go 1.25.5+
- [openai-go/v3](https://github.com/openai/openai-go) v3.26.0
- [godotenv](https://github.com/joho/godotenv) v1.5.1 (examples only)

---

<details>
<summary>中文版 / Chinese</summary>

# tool_calling_go

`tool_calling` SDK 的 Go 实现。基于 [openai-go/v3](https://github.com/openai/openai-go)，兼容所有 OpenAI 兼容 API（OpenAI、DeepSeek、vLLM、Ollama 等）。

## 功能

- **Agent** — LLM 对话 + 自动工具调用循环 + 错误自动重试
- **Batch** — 批量并发调用多个 Agent.Chat，带信号量限流
- **并行工具执行** — 同一轮多个 tool call 通过 goroutine 并行执行

## 目录结构

```
tool_calling_go/
├── agent.go              # 核心：LLMConfig / Tool / Agent / Chat
├── batch.go              # Batch 批量并发调度
├── go.mod / go.sum       # 依赖管理
├── .env.example          # 环境变量模板（API_KEY / MODEL / BASE_URL）
└── example/
    ├── getweather/       # 单次对话示例
    └── batchuse/         # 50 路并发批量示例
```

## 快速开始

### 1. 配置环境变量

在 `tool_calling_go/` 目录下复制 `.env.example` 并填入实际值：

```bash
cp .env.example .env
```

```env
API_KEY=sk-your-api-key
MODEL=your-model-name
BASE_URL=your-base-url
```

### 2. 运行示例

```bash
cd tool_calling_go

# 单次对话（LLM 并行调用 2 次 get_weather）
go run ./example/getweather

# 批量并发（50 个问题，最多 50 路并发）
go run ./example/batchuse
```

## API 参考

### LLMConfig

```go
type LLMConfig struct {
    APIKey    string            // API 密钥
    Model     string            // 模型名称
    BaseURL   string            // API 地址（留空使用 OpenAI 默认）
    ExtraBody map[string]any    // 厂商特定参数（如 Kimi 的 thinking 开关）
}
```

### Tool

```go
type Tool struct {
    Name        string              // 工具名称
    Description string              // 工具描述（给 LLM 看）
    Function    ToolFunc            // 实际执行函数
    Parameters  map[string]any      // JSON Schema 参数定义
}

type ToolFunc func(args map[string]any) (string, error)
```

### Agent

```go
// 创建
agent := NewAgent(config)
agent := NewAgent(config, WithDebug(true), WithMaxToolRetries(5))

// 注册 / 移除工具
agent.AddTool(tool)
agent.RemoveTool("tool_name")

// 对话（自动处理工具调用循环）
messages, err := agent.Chat(ctx, []openai.ChatCompletionMessageParamUnion{
    openai.UserMessage("你的问题"),
})
```

### Batch

```go
// 批量并发调用；maxConcurrent 控制最大并行数
results, err := Batch(ctx, agent, observations, maxConcurrent)
```

## 调用流程

```
用户消息
  │
  ▼
Agent.Chat() ──→ LLM API
  │                 │
  │     ◄───────────┘  finish_reason == "tool_calls"
  │
  ├─ 并行执行所有 tool call（goroutine）
  ├─ 将工具结果追加到对话
  ├─ 如有错误且未超重试次数，追加错误提示
  │
  └─ 再次调用 LLM API ──→ ...（循环直到 finish_reason != "tool_calls"）
  │
  ▼
返回完整对话历史
```

## 依赖

- Go 1.25.5+
- [openai-go/v3](https://github.com/openai/openai-go) v3.26.0
- [godotenv](https://github.com/joho/godotenv) v1.5.1（仅示例使用）

</details>
