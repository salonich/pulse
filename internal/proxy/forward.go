// Package proxy implements the LLM intercepting sidecar proxy.
package proxy

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/velorai/pulse/internal/provider"
	"github.com/velorai/pulse/internal/trace"
)

// upstreamURL maps the incoming path prefix to the upstream base URL.
// /anthropic/* → https://api.anthropic.com
// /openai/*    → https://api.openai.com
// If override is set, all providers route there instead.
func upstreamURL(path, override string) (string, string, error) {
	p, tail, ok := provider.FromPath(path)
	if !ok {
		return "", "", fmt.Errorf("unknown provider prefix in path %q", path)
	}
	base := provider.UpstreamURL(p)
	if override != "" {
		base = override
	}
	return base + tail, p, nil
}

// Forwarder handles a single LLM request: forwards it upstream, captures the
// trace, and submits to the bounded async sender.
//
// Cost is NOT computed here — the collector is the single source of truth.
type Forwarder struct {
	httpClient       *http.Client
	sender           *trace.Sender
	namespace        string
	name             string
	upstreamOverride string
	log              *slog.Logger
}

func newForwarder(sender *trace.Sender, namespace, name, upstreamOverride string, log *slog.Logger) *Forwarder {
	return &Forwarder{
		httpClient:       &http.Client{Timeout: 120 * time.Second},
		sender:           sender,
		namespace:        namespace,
		name:             name,
		upstreamOverride: upstreamOverride,
		log:              log,
	}
}

// ServeHTTP handles an inbound proxy request (non-streaming only for Weekend 1).
func (f *Forwarder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	targetURL, prov, err := upstreamURL(r.URL.Path, f.upstreamOverride)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "reading body: "+err.Error(), http.StatusInternalServerError)
		return
	}
	_ = r.Body.Close()

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
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		http.Error(w, "reading upstream response: "+err.Error(), http.StatusBadGateway)
		return
	}
	latencyMS := int(time.Since(start).Milliseconds())

	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(respBody)

	f.submitTrace(prov, resp.StatusCode, latencyMS, respBody)
}

// submitTrace builds a canonical trace and submits it to the sender.
// Non-blocking — full sender buffer drops the trace and bumps the dropped counter.
func (f *Forwarder) submitTrace(prov string, status, latencyMS int, body []byte) {
	model, promptTokens, completionTokens := provider.ExtractUsage(body)

	t := trace.Trace{
		LLMBackendNamespace: f.namespace,
		LLMBackendName:      f.name,
		Provider:            prov,
		Model:               model,
		PromptTokens:        promptTokens,
		CompletionTokens:    completionTokens,
		LatencyMS:           latencyMS,
		Status:              status,
		CreatedAt:           time.Now().UTC(),
	}
	f.sender.Submit(t)
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
