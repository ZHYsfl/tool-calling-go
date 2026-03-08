// BatchRace example: parallel faculty search with cascading termination.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"github.com/openai/openai-go/v3"

	tc "toolcalling"
)

type teacher struct {
	Name     string `json:"name"`
	Position string `json:"position"`
	Research string `json:"research"`
}

type facultyPayload struct {
	Faculty  string    `json:"faculty"`
	Teachers []teacher `json:"teachers"`
}

type facultySite struct {
	Name      string
	Delay     time.Duration
	HasTarget bool
	Server    *httptest.Server
}

func newFacultySite(name string, delay time.Duration, hasTarget bool) *facultySite {
	s := &facultySite{Name: name, Delay: delay, HasTarget: hasTarget}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(s.Delay):
		}

		payload := facultyPayload{
			Faculty: s.Name,
			Teachers: []teacher{
				{Name: "Li Ming", Position: "Associate Professor", Research: "Optimization"},
				{Name: "Wang Lei", Position: "Professor", Research: "Data Mining"},
			},
		}
		if s.HasTarget {
			payload.Teachers = append(payload.Teachers, teacher{
				Name:     "Zhang Wei",
				Position: "Professor",
				Research: "Multi-Agent Systems and Distributed Orchestration",
			})
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(payload)
	}))
	return s
}

func scrapeFacultyPage(ctx context.Context, args map[string]any) (string, error) {
	url, _ := args["url"].(string)
	targetName, _ := args["target_name"].(string)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("http request failed: %w", err)
	}
	defer resp.Body.Close()

	var payload facultyPayload
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}

	for _, t := range payload.Teachers {
		if strings.EqualFold(t.Name, targetName) {
			return fmt.Sprintf(
				`{"status":"found","faculty":"%s","teacher":"%s","position":"%s","research":"%s"}`,
				payload.Faculty, t.Name, t.Position, t.Research,
			), nil
		}
	}
	return fmt.Sprintf(`{"status":"not_found","faculty":"%s","teacher":"%s"}`, payload.Faculty, targetName), nil
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
		Name:        "scrape_faculty_page",
		Description: "Fetch one faculty roster page and search for target teacher",
		Function:    scrapeFacultyPage,
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url": map[string]any{
					"type":        "string",
					"description": "Faculty roster URL",
				},
				"target_name": map[string]any{
					"type":        "string",
					"description": "Teacher name to search for",
				},
			},
			"required": []any{"url", "target_name"},
		},
	})

	sites := []*facultySite{
		newFacultySite("Computer Science", 1300*time.Millisecond, false),
		newFacultySite("Mathematics", 2800*time.Millisecond, false),
		newFacultySite("Physics", 1700*time.Millisecond, false),
		newFacultySite("Chemistry", 3400*time.Millisecond, false),
		newFacultySite("Biology", 2100*time.Millisecond, true), // winner
		newFacultySite("Economics", 2900*time.Millisecond, false),
		newFacultySite("Civil Engineering", 3600*time.Millisecond, false),
		newFacultySite("Mechanical Engineering", 2500*time.Millisecond, false),
		newFacultySite("Materials", 3200*time.Millisecond, false),
		newFacultySite("Statistics", 1900*time.Millisecond, false),
	}
	for _, s := range sites {
		defer s.Server.Close()
	}

	target := "Zhang Wei"
	observations := make([][]openai.ChatCompletionMessageParamUnion, len(sites))
	var estimatedSerial time.Duration
	for i, s := range sites {
		estimatedSerial += s.Delay
		prompt := fmt.Sprintf(
			"You are agent_%d. Check only this faculty page for teacher %q.\n"+
				"Call scrape_faculty_page with url=%q and target_name=%q.\n"+
				"If found, respond with JSON containing status=found, faculty, position, research.\n"+
				"If not found, respond with JSON containing status=not_found and faculty.",
			i, target, s.Server.URL, target,
		)
		observations[i] = []openai.ChatCompletionMessageParamUnion{openai.UserMessage(prompt)}
	}

	successCond := func(msgs []openai.ChatCompletionMessageParamUnion) bool {
		if len(msgs) == 0 {
			return false
		}
		last := msgs[len(msgs)-1]
		if last.OfAssistant == nil || !last.OfAssistant.Content.OfString.Valid() {
			return false
		}
		return strings.Contains(strings.ToLower(last.OfAssistant.Content.OfString.Value), `"status":"found"`) ||
			strings.Contains(strings.ToLower(last.OfAssistant.Content.OfString.Value), `"status": "found"`)
	}

	timeoutCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	start := time.Now()
	result, err := tc.BatchRace(
		timeoutCtx,
		agent,
		observations,
		successCond,
		tc.WithMaxConcurrent(len(observations)),
		tc.WithEventHandler(func(e tc.RaceEvent) {
			log.Printf("[event=%s] agent=%d %s", e.Type, e.AgentID, e.Message)
		}),
	)
	elapsed := time.Since(start)

	fmt.Println(strings.Repeat("=", 80))
	fmt.Printf("Estimated serial time: %s\n", estimatedSerial)
	fmt.Printf("Parallel race time:    %s\n", elapsed)
	fmt.Println(strings.Repeat("=", 80))

	if err != nil {
		log.Fatalf("BatchRace failed: %v", err)
	}

	last := result.Messages[len(result.Messages)-1]
	if last.OfAssistant != nil && last.OfAssistant.Content.OfString.Valid() {
		fmt.Printf("Winner agent index: %d\n", result.Index)
		fmt.Printf("Final answer: %s\n", last.OfAssistant.Content.OfString.Value)
	}
}
