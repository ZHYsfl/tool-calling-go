// 1:1 port of tool_calling/example/get_weather.py
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/joho/godotenv"
	"github.com/openai/openai-go/v3"

	tc "toolcalling"
)

func getWeather(args map[string]any) (string, error) {
	city, _ := args["city"].(string)
	return fmt.Sprintf("城市 %s 的天气是晴朗，气温20度，湿度50%%，风力2级，空气质量优。", city), nil
}

func main() {
	if err := godotenv.Load(".env"); err != nil {
		log.Println("未找到 .env，继续使用系统环境变量")
	}

	config := tc.LLMConfig{
		APIKey:  os.Getenv("API_KEY"),
		Model:   os.Getenv("MODEL"),
		BaseURL: os.Getenv("BASE_URL"),
	}

	agent := tc.NewAgent(config)
	agent.AddTool(tc.Tool{
		Name:        "get_weather",
		Description: "获取天气",
		Function:    getWeather,
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"city": map[string]any{
					"type":        "string",
					"description": "城市名称",
				},
			},
			"required": []any{"city"},
		},
	})

	observations := []openai.ChatCompletionMessageParamUnion{
		openai.UserMessage("请获取北京和杭州各自的天气，并行调用工具get_weather"),
	}

	result, err := agent.Chat(context.Background(), observations)
	if err != nil {
		log.Fatal(err)
	}

	last := result[len(result)-1]
	if last.OfAssistant != nil {
		if s := last.OfAssistant.Content.OfString; s.Valid() {
			fmt.Println(s.Value)
		}
	}
}
