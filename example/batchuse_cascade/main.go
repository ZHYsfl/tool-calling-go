// BatchRace 示例：与 batchuse 相同的「多路并发查天气」场景，但使用 BatchRace。
// 当任意一路 agent 先完成并满足成功条件时，会级联取消其余仍在运行的 agent（见 toolcalling.BatchRace）。
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/joho/godotenv"
	"github.com/openai/openai-go/v3"

	tc "toolcalling"
)

func getWeather(_ context.Context, args map[string]any) (string, error) {
	city, _ := args["city"].(string)
	return fmt.Sprintf("The weather in %s is sunny, 20°C, humidity 50%%, wind level 2, air quality excellent.", city), nil
}

// 判定一轮对话是否已「完成任务」：助手最后一条文本同时覆盖北京与杭州（中英均可）。
func taskDone(messages []openai.ChatCompletionMessageParamUnion) bool {
	if len(messages) == 0 {
		return false
	}
	last := messages[len(messages)-1]
	if last.OfAssistant == nil {
		return false
	}
	if !last.OfAssistant.Content.OfString.Valid() {
		return false
	}
	s := strings.ToLower(last.OfAssistant.Content.OfString.Value)
	hasBj := strings.Contains(s, "beijing") || strings.Contains(s, "北京")
	hasHz := strings.Contains(s, "hangzhou") || strings.Contains(s, "杭州")
	return hasBj && hasHz
}

func main() {
	if err := godotenv.Load(".env"); err != nil {
		log.Println("No .env found, falling back to system environment variables")
	}

	config := tc.LLMConfig{
		APIKey:  os.Getenv("LLM_API_KEY"),
		Model:   os.Getenv("LLM_MODEL"),
		BaseURL: os.Getenv("LLM_BASE_URL"),
		ExtraBody: map[string]any{
			"thinking": map[string]any{"type": "disabled"},
		},
	}

	agent := tc.NewAgent(config)
	agent.AddTool(tc.Tool{
		Name:        "get_weather",
		Description: "Get the current weather for a given city",
		Function:    getWeather,
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"city": map[string]any{
					"type":        "string",
					"description": "City name",
				},
			},
			"required": []any{"city"},
		},
	})

	observations := make([][]openai.ChatCompletionMessageParamUnion, 10)
	for i := range observations {
		observations[i] = []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage("Get the weather for Beijing and Hangzhou. Call get_weather in parallel."),
		}
	}

	result, err := tc.BatchRace(
		context.Background(),
		agent,
		observations,
		taskDone,
		tc.WithMaxConcurrent(10),
		tc.WithEventHandler(func(ev tc.RaceEvent) {
			switch ev.Type {
			case tc.RaceEventStarted:
				fmt.Printf("[race] %s\n", ev.Message)
			case tc.RaceEventSuccess:
				fmt.Printf("[race] %s — cascading cancel to other agents\n", ev.Message)
			case tc.RaceEventCancelled:
				fmt.Printf("[race] %s\n", ev.Message)
			case tc.RaceEventNoMatch:
				fmt.Printf("[race] %s\n", ev.Message)
			case tc.RaceEventError:
				fmt.Printf("[race] %s\n", ev.Message)
			}
		}),
	)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println(strings.Repeat("=", 100))
	fmt.Printf("Winner: agent %d\n", result.Index)
	last := result.Messages[len(result.Messages)-1]
	if last.OfAssistant != nil {
		if s := last.OfAssistant.Content.OfString; s.Valid() {
			fmt.Println(s.Value)
		}
	}
}
