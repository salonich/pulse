// Package trace defines the canonical trace shape exchanged between capture
// (proxy / eBPF) and the collector. Every component that emits or consumes a
// trace MUST use this type — duplicating the struct in each package was the
// source of silent field drift.
package trace

import "time"

// Trace is one captured LLM API call.
//
// JSON tags are the wire contract for POST /v1/traces. Fields with JSON
// omitempty are optional; everything else is required for a valid payload.
type Trace struct {
	LLMBackendNamespace string    `json:"llmbackend_namespace"`
	LLMBackendName      string    `json:"llmbackend_name"`
	Provider            string    `json:"provider"`
	Model               string    `json:"model,omitempty"`
	PromptTokens        int       `json:"prompt_tokens"`
	CompletionTokens    int       `json:"completion_tokens"`
	LatencyMS           int       `json:"latency_ms"`
	CostUSD             float64   `json:"cost_usd,omitempty"`
	Status              int       `json:"status"`
	PromptVersion       string    `json:"prompt_version,omitempty"`
	CreatedAt           time.Time `json:"created_at"`
}

// Validate returns "" if the trace is well-formed for collector ingestion.
// Returns a human-readable reason otherwise.
func (t Trace) Validate() string {
	if t.LLMBackendNamespace == "" {
		return "llmbackend_namespace is required"
	}
	if t.LLMBackendName == "" {
		return "llmbackend_name is required"
	}
	if t.Provider == "" {
		return "provider is required"
	}
	if t.LatencyMS < 0 {
		return "latency_ms must be non-negative"
	}
	return ""
}
