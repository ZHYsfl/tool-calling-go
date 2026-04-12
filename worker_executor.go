package toolcalling

import (
	"context"

	"github.com/openai/openai-go/v3"
)

// RaceTask is one unit of work (message history) passed to WorkerExecutor.
type RaceTask struct {
	Messages []openai.ChatCompletionMessageParamUnion
}

// WorkerExecutor defines how one sub-task is executed.
// Implementations can run in-process, out-of-process, or remotely.
type WorkerExecutor interface {
	ExecuteTask(
		ctx context.Context,
		worker *Agent,
		task RaceTask,
	) ([]openai.ChatCompletionMessageParamUnion, error)
}

// InProcessWorkerExecutor runs tasks in the current process using Agent.Chat.
// This is the default executor.
type InProcessWorkerExecutor struct{}

func (InProcessWorkerExecutor) ExecuteTask(
	ctx context.Context,
	worker *Agent,
	task RaceTask,
) ([]openai.ChatCompletionMessageParamUnion, error) {
	return worker.Chat(ctx, task.Messages)
}
