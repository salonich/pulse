// Command mockllm fakes an Anthropic-compatible API for local testing.
// Returns a canned response with realistic token counts so the full
// proxy → collector → postgres pipeline can be exercised without an API key.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math/rand/v2"
	"net/http"
)

func main() {
	http.HandleFunc("/v1/messages", handleMessages)
	http.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	log.Println("mock LLM listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}

func handleMessages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}

	// Decode request just to echo back the model.
	var req struct {
		Model string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.Model == "" {
		req.Model = "claude-sonnet-4-20250514"
	}

	inputTokens := 20 + rand.IntN(80)
	outputTokens := 10 + rand.IntN(60)

	resp := map[string]any{
		"id":    fmt.Sprintf("msg_%012d", rand.IntN(1e12)),
		"type":  "message",
		"role":  "assistant",
		"model": req.Model,
		"content": []map[string]string{
			{"type": "text", "text": "Hello! This is a mock response from Pulse's local test server."},
		},
		"stop_reason": "end_turn",
		"usage": map[string]int{
			"input_tokens":  inputTokens,
			"output_tokens": outputTokens,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}
