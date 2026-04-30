// Package provider centralises LLM provider classification and response
// parsing. Both the proxy sidecar and the eBPF agent depend on this — keep
// provider knowledge here, not duplicated at each capture site.
package provider

import (
	"bytes"
	"encoding/json"
	"strings"
)

// Provider name constants used as the value of trace.Trace.Provider.
const (
	Anthropic = "anthropic"
	OpenAI    = "openai"
	Custom    = "custom"
)

// FromHost classifies an HTTP Host header into a provider name.
// Substring match is used so subdomains and proxies (e.g. "openai-proxy.acme.io")
// classify correctly. Falls back to Custom for unknown hosts.
func FromHost(host string) string {
	h := strings.ToLower(host)
	switch {
	case strings.Contains(h, "anthropic"):
		return Anthropic
	case strings.Contains(h, "openai"):
		return OpenAI
	case strings.Contains(h, "mockllm"):
		// Local test mock speaks the Anthropic dialect.
		return Anthropic
	default:
		return Custom
	}
}

// FromPath classifies a proxy path prefix and returns the remaining tail.
// Returns ok=false if no recognised provider prefix is present.
func FromPath(path string) (provider, tail string, ok bool) {
	switch {
	case strings.HasPrefix(path, "/anthropic/"):
		return Anthropic, strings.TrimPrefix(path, "/anthropic"), true
	case strings.HasPrefix(path, "/openai/"):
		return OpenAI, strings.TrimPrefix(path, "/openai"), true
	}
	return "", "", false
}

// UpstreamURL returns the canonical upstream base URL for a provider.
// Returns "" for providers without a default upstream.
func UpstreamURL(provider string) string {
	switch provider {
	case Anthropic:
		return "https://api.anthropic.com"
	case OpenAI:
		return "https://api.openai.com"
	}
	return ""
}

// ExtractUsage parses an LLM response body and returns model + token counts.
// Returns zero values if the body is unrecognised, truncated, or not JSON.
//
// The body may carry trailing garbage (e.g. ring buffer artefacts in eBPF
// captures); we trim from the first '{' to the last '}' before unmarshalling.
func ExtractUsage(body []byte) (model string, promptTokens, completionTokens int) {
	open := bytes.IndexByte(body, '{')
	if open < 0 {
		return
	}
	closeBrace := bytes.LastIndexByte(body, '}')
	if closeBrace <= open {
		return
	}
	jsonBytes := body[open : closeBrace+1]

	// Anthropic shape: usage.input_tokens / usage.output_tokens.
	var ar anthropicResp
	if err := json.Unmarshal(jsonBytes, &ar); err == nil && ar.Usage.InputTokens > 0 {
		return ar.Model, ar.Usage.InputTokens, ar.Usage.OutputTokens
	}
	// OpenAI shape: usage.prompt_tokens / usage.completion_tokens.
	var or openAIResp
	if err := json.Unmarshal(jsonBytes, &or); err == nil && or.Usage.PromptTokens > 0 {
		return or.Model, or.Usage.PromptTokens, or.Usage.CompletionTokens
	}
	return
}

type anthropicResp struct {
	Model string `json:"model"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

type openAIResp struct {
	Model string `json:"model"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}
