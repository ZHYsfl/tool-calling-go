// Batch example: 50 concurrent chats, each asking for weather in two cities.
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

func getWeather(args map[string]any) (string, error) {
	city, _ := args["city"].(string)
	return fmt.Sprintf("The weather in %s is sunny, 20°C, humidity 50%%, wind level 2, air quality excellent.", city), nil
}

func main() {
	if err := godotenv.Load(".env"); err != nil {
		log.Println("No .env found, falling back to system environment variables")
	}

	config := tc.LLMConfig{
		APIKey:  os.Getenv("API_KEY"),
		Model:   os.Getenv("MODEL"),
		BaseURL: os.Getenv("BASE_URL"),
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

	observations := make([][]openai.ChatCompletionMessageParamUnion, 50)
	for i := range observations {
		observations[i] = []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage("Get the weather for Beijing and Hangzhou. Call get_weather in parallel."),
		}
	}

	results, err := tc.Batch(context.Background(), agent, observations, 50)
	if err != nil {
		log.Fatal(err)
	}

	for _, msgs := range results {
		last := msgs[len(msgs)-1]
		if last.OfAssistant != nil {
			if s := last.OfAssistant.Content.OfString; s.Valid() {
				fmt.Println(s.Value)
			}
		}
		fmt.Println(strings.Repeat("-", 100))
	}
}
