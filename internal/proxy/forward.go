package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/velorai/pulse/internal/pricing"
)

// upstreamURL maps the incoming path prefix to the upstream base URL.
// /anthropic/* → https://api.anthropic.com
// /openai/*    → https://api.openai.com  (Iteration 1)
func upstreamURL(path string) (string, string, error) {
	switch {
	case strings.HasPrefix(path, "/anthropic/"):
		tail := strings.TrimPrefix(path, "/anthropic")
		return "https://api.anthropic.com" + tail, "anthropic", nil
	case strings.HasPrefix(path, "/openai/"):
		tail := strings.TrimPrefix(path, "/openai")
		return "https://api.openai.com" + tail, "openai", nil
	default:
		return "", "", fmt.Errorf("unknown provider prefix in path %q", path)
	}
}

// Forwarder handles a single LLM request: forwards it upstream, captures the trace,
// and posts the trace to the collector asynchronously.
type Forwarder struct {
	httpClient *http.Client
	pricing    *pricing.Table
	tracer     *Tracer
	log        *slog.Logger
}

func newForwarder(pt *pricing.Table, tracer *Tracer, log *slog.Logger) *Forwarder {
	return &Forwarder{
		httpClient: &http.Client{Timeout: 120 * time.Second},
		pricing:    pt,
		tracer:     tracer,
		log:        log,
	}
}

// ServeHTTP handles an inbound proxy request (non-streaming only for Weekend 1).
func (f *Forwarder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	targetURL, provider, err := upstreamURL(r.URL.Path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Buffer the request body so we can forward it.
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "reading body: "+err.Error(), http.StatusInternalServerError)
		return
	}
	r.Body.Close()

	// Build upstream request.
	upReq, err := http.NewRequestWithContext(r.Context(), r.Method, targetURL, bytes.NewReader(body))
	if err != nil {
		http.Error(w, "building upstream request: "+err.Error(), http.StatusInternalServerError)
		return
	}
	copyHeaders(upReq.Header, r.Header)
	if r.URL.RawQuery != "" {
		upReq.URL.RawQuery = r.URL.RawQuery
	}

	resp, err := f.httpClient.Do(upReq)
	if err != nil {
		f.log.Error("upstream request failed", "error", err, "url", targetURL)
		http.Error(w, "upstream error: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// Read full response body.
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		http.Error(w, "reading upstream response: "+err.Error(), http.StatusBadGateway)
		return
	}
	latencyMS := int(time.Since(start).Milliseconds())

	// Forward response headers + body to the app — app is unblocked here.
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(respBody)

	// Async trace capture — never blocks the response path.
	go f.captureTrace(context.Background(), provider, resp.StatusCode, latencyMS, respBody)
}

// captureTrace extracts trace fields from the upstream response body and posts to collector.
func (f *Forwarder) captureTrace(ctx context.Context, provider string, status, latencyMS int, body []byte) {
	var model string
	var promptTokens, completionTokens int

	switch provider {
	case "anthropic":
		var ar anthropicResponse
		if err := json.Unmarshal(body, &ar); err == nil {
			model = ar.Model
			promptTokens = ar.Usage.InputTokens
			completionTokens = ar.Usage.OutputTokens
		}
	// openai: Iteration 1
	}

	cost := f.pricing.Calculate(model, promptTokens, completionTokens)

	t := Trace{
		LLMBackendNamespace: f.tracer.namespace,
		LLMBackendName:      f.tracer.name,
		Provider:            provider,
		Model:               model,
		PromptTokens:        promptTokens,
		CompletionTokens:    completionTokens,
		LatencyMS:           latencyMS,
		CostUSD:             cost,
		Status:              status,
		CreatedAt:           time.Now().UTC(),
	}

	if err := f.tracer.Send(ctx, t); err != nil {
		f.log.Warn("failed to send trace to collector", "error", err)
	}
}

// copyHeaders copies headers from src to dst, skipping hop-by-hop headers.
func copyHeaders(dst, src http.Header) {
	hopByHop := map[string]bool{
		"Connection": true, "Keep-Alive": true, "Proxy-Authenticate": true,
		"Proxy-Authorization": true, "Te": true, "Trailers": true,
		"Transfer-Encoding": true, "Upgrade": true,
	}
	for k, vs := range src {
		if hopByHop[k] {
			continue
		}
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}
