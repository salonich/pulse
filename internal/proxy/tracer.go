package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// Tracer posts captured traces to the collector service.
// It fails open: if the collector is unreachable the error is logged and dropped.
type Tracer struct {
	collectorURL string
	namespace    string
	name         string
	client       *http.Client
	log          *slog.Logger
}

func newTracer(collectorURL, namespace, name string, log *slog.Logger) *Tracer {
	return &Tracer{
		collectorURL: collectorURL,
		namespace:    namespace,
		name:         name,
		client:       &http.Client{Timeout: 5 * time.Second},
		log:          log,
	}
}

// Send posts the trace to the collector. Non-blocking: called from a goroutine.
// Errors are logged and swallowed so the proxy never fails an LLM call.
func (t *Tracer) Send(ctx context.Context, trace Trace) error {
	data, err := json.Marshal(trace)
	if err != nil {
		return fmt.Errorf("marshalling trace: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		t.collectorURL+"/v1/traces", bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("building collector request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := t.client.Do(req)
	if err != nil {
		// Fail open — log and discard.
		t.log.Warn("collector unreachable, dropping trace", "error", err)
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		t.log.Warn("collector returned non-2xx", "status", resp.StatusCode)
	}
	return nil
}
