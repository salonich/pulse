package pricing

import (
	"encoding/json"
	"os"
	"testing"
)

func TestCalculate_KnownModel(t *testing.T) {
	table := New()
	// 1M input + 1M output tokens on claude-3-5-sonnet → $3 + $15 = $18
	got := table.Calculate("claude-3-5-sonnet-20241022", 1_000_000, 1_000_000)
	if got != 18.00 {
		t.Errorf("expected 18.00, got %f", got)
	}
}

func TestCalculate_UnknownModel(t *testing.T) {
	table := New()
	got := table.Calculate("unknown-model-xyz", 1_000_000, 1_000_000)
	if got != 0 {
		t.Errorf("expected 0 for unknown model, got %f", got)
	}
}

func TestLoad_OverridesDefault(t *testing.T) {
	data := `{"models":{"my-custom-model":{"input_per_million":1.0,"output_per_million":2.0}}}`
	f, err := os.CreateTemp(t.TempDir(), "pricing*.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(data); err != nil {
		t.Fatal(err)
	}
	f.Close()

	table := New()
	if err := table.Load(f.Name()); err != nil {
		t.Fatalf("Load: %v", err)
	}

	got := table.Calculate("my-custom-model", 1_000_000, 1_000_000)
	if got != 3.00 {
		t.Errorf("expected 3.00, got %f", got)
	}
	// Defaults still present.
	if table.Calculate("gpt-4o", 1_000_000, 0) == 0 {
		t.Error("default pricing should still be present after Load")
	}
}

func TestJSON_RoundTrip(t *testing.T) {
	table := New()
	data, err := table.JSON()
	if err != nil {
		t.Fatal(err)
	}
	var pf pricingFile
	if err := json.Unmarshal(data, &pf); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(pf.Models) == 0 {
		t.Error("expected non-empty models in JSON output")
	}
}
