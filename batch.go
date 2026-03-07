package toolcalling

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/openai/openai-go/v3"
)

type indexedResult struct {
	index    int
	messages []openai.ChatCompletionMessageParamUnion
	err      error
}

// Batch runs multiple Agent.Chat sessions concurrently with bounded
// concurrency controlled by maxConcurrent (← Python batch()).
func Batch(
	ctx context.Context,
	agent *Agent,
	observations [][]openai.ChatCompletionMessageParamUnion,
	maxConcurrent int,
) ([][]openai.ChatCompletionMessageParamUnion, error) {

	if len(observations) == 0 {
		return nil, fmt.Errorf("observations must not be empty")
	}
	if maxConcurrent <= 0 {
		return nil, fmt.Errorf("maxConcurrent must be positive")
	}

	sem := make(chan struct{}, maxConcurrent)
	var mu sync.Mutex
	results := make([]indexedResult, 0, len(observations))
	var wg sync.WaitGroup

	for i := range observations {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			sem <- struct{}{}        // acquire slot
			defer func() { <-sem }() // release slot

			msgs, err := agent.Chat(ctx, observations[idx])

			mu.Lock()
			results = append(results, indexedResult{index: idx, messages: msgs, err: err})
			mu.Unlock()
		}(i)
	}

	wg.Wait()

	for _, r := range results {
		if r.err != nil {
			return nil, fmt.Errorf("batch item %d: %w", r.index, r.err)
		}
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].index < results[j].index
	})

	out := make([][]openai.ChatCompletionMessageParamUnion, len(results))
	for i, r := range results {
		out[i] = r.messages
	}
	return out, nil
}
