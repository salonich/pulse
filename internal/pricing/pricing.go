// Package pricing loads per-token LLM costs from a JSON ConfigMap and calculates trace costs.
package pricing

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
)

// ModelPricing holds the per-token prices for a model in USD per million tokens.
type ModelPricing struct {
	InputPerMillion  float64 `json:"input_per_million"`
	OutputPerMillion float64 `json:"output_per_million"`
}

// Table maps model name → pricing. Thread-safe after Load.
type Table struct {
	mu     sync.RWMutex
	models map[string]ModelPricing
}

// pricingFile is the JSON structure expected in pricing.json.
type pricingFile struct {
	Models map[string]ModelPricing `json:"models"`
}

// defaultPricing provides fallback values so the proxy works before the ConfigMap mounts.
var defaultPricing = map[string]ModelPricing{
	// Anthropic
	"claude-opus-4-6":                 {InputPerMillion: 15.00, OutputPerMillion: 75.00},
	"claude-sonnet-4-6":               {InputPerMillion: 3.00, OutputPerMillion: 15.00},
	"claude-haiku-4-5-20251001":       {InputPerMillion: 0.80, OutputPerMillion: 4.00},
	"claude-3-5-sonnet-20241022":      {InputPerMillion: 3.00, OutputPerMillion: 15.00},
	"claude-3-opus-20240229":          {InputPerMillion: 15.00, OutputPerMillion: 75.00},
	"claude-3-haiku-20240307":         {InputPerMillion: 0.25, OutputPerMillion: 1.25},
	// OpenAI
	"gpt-4o":                          {InputPerMillion: 2.50, OutputPerMillion: 10.00},
	"gpt-4o-mini":                     {InputPerMillion: 0.15, OutputPerMillion: 0.60},
	"gpt-4-turbo":                     {InputPerMillion: 10.00, OutputPerMillion: 30.00},
	"gpt-3.5-turbo":                   {InputPerMillion: 0.50, OutputPerMillion: 1.50},
	// Google
	"gemini-1.5-pro":                  {InputPerMillion: 1.25, OutputPerMillion: 5.00},
	"gemini-1.5-flash":                {InputPerMillion: 0.075, OutputPerMillion: 0.30},
}

// New returns a Table pre-loaded with default pricing.
func New() *Table {
	t := &Table{models: make(map[string]ModelPricing)}
	for k, v := range defaultPricing {
		t.models[k] = v
	}
	return t
}

// Load reads pricing from a JSON file path and merges it over the defaults.
// It is safe to call from a file-watch goroutine while other goroutines call Calculate.
func (t *Table) Load(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading pricing file %s: %w", path, err)
	}
	var pf pricingFile
	if err := json.Unmarshal(data, &pf); err != nil {
		return fmt.Errorf("parsing pricing file: %w", err)
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	// Start from defaults, overlay file values.
	merged := make(map[string]ModelPricing, len(defaultPricing)+len(pf.Models))
	for k, v := range defaultPricing {
		merged[k] = v
	}
	for k, v := range pf.Models {
		merged[k] = v
	}
	t.models = merged
	return nil
}

// Calculate returns the cost in USD for a given model and token counts.
// Returns 0 (not an error) for unknown models — the collector can re-enrich server-side.
func (t *Table) Calculate(model string, promptTokens, completionTokens int) float64 {
	t.mu.RLock()
	p, ok := t.models[model]
	t.mu.RUnlock()
	if !ok {
		return 0
	}
	return (float64(promptTokens)/1_000_000)*p.InputPerMillion +
		(float64(completionTokens)/1_000_000)*p.OutputPerMillion
}

// JSON returns the current pricing table serialised as JSON for writing to a ConfigMap.
func (t *Table) JSON() ([]byte, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return json.Marshal(pricingFile{Models: t.models})
}
