// Package proxy implements the LLM intercepting sidecar proxy.
package proxy

import "time"

// Trace carries the data extracted from a single LLM API call.
// Posted asynchronously to the collector after the response is returned to the app.
type Trace struct {
	LLMBackendNamespace string    `json:"llmbackend_namespace"`
	LLMBackendName      string    `json:"llmbackend_name"`
	Provider            string    `json:"provider"`
	Model               string    `json:"model"`
	PromptTokens        int       `json:"prompt_tokens"`
	CompletionTokens    int       `json:"completion_tokens"`
	LatencyMS           int       `json:"latency_ms"`
	CostUSD             float64   `json:"cost_usd"`
	Status              int       `json:"status"`
	PromptVersion       string    `json:"prompt_version,omitempty"`
	CreatedAt           time.Time `json:"created_at"`
}

// anthropicUsage is the usage object in Anthropic API responses.
type anthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// anthropicResponse is the minimal subset of the Anthropic messages API response
// needed for trace extraction.
type anthropicResponse struct {
	ID    string         `json:"id"`
	Model string         `json:"model"`
	Usage anthropicUsage `json:"usage"`
}
