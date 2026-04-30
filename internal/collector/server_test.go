package collector

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/velorai/pulse/internal/pricing"
	"github.com/velorai/pulse/internal/store"
	"github.com/velorai/pulse/internal/trace"
)

// fakeStore records the last inserted trace.
type fakeStore struct {
	last store.Trace
	err  error
}

func (f *fakeStore) InsertTrace(_ context.Context, t store.Trace) error {
	f.last = t
	return f.err
}
func (f *fakeStore) ListTraces(_ context.Context, _ string, _ int) ([]store.Trace, error) {
	return nil, nil
}
func (f *fakeStore) Close() {}

func newTestServer(t *testing.T, s store.Store) *Server {
	t.Helper()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	return New(s, pricing.New(), log)
}

func TestHandleTrace_Valid(t *testing.T) {
	fs := &fakeStore{}
	srv := newTestServer(t, fs)

	payload := trace.Trace{
		LLMBackendNamespace: "my-app",
		LLMBackendName:      "checkout-anthropic",
		Provider:            "anthropic",
		Model:               "claude-3-5-sonnet-20241022",
		PromptTokens:        100,
		CompletionTokens:    200,
		LatencyMS:           350,
		Status:              200,
		CreatedAt:           time.Now(),
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/v1/traces", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", w.Code, w.Body.String())
	}
	if fs.last.LLMBackendNamespace != "my-app" {
		t.Errorf("unexpected namespace: %q", fs.last.LLMBackendNamespace)
	}
	// Cost is enriched server-side from the pricing table.
	if fs.last.CostUSD == 0 {
		t.Error("expected server-side cost enrichment, got 0")
	}
}

func TestHandleTrace_MissingNamespace(t *testing.T) {
	srv := newTestServer(t, &fakeStore{})

	payload := trace.Trace{Provider: "anthropic", LLMBackendName: "foo", LatencyMS: 100}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/v1/traces", bytes.NewReader(body))
	w := httptest.NewRecorder()

	srv.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestHandleTrace_InvalidJSON(t *testing.T) {
	srv := newTestServer(t, &fakeStore{})
	req := httptest.NewRequest(http.MethodPost, "/v1/traces", bytes.NewReader([]byte("not json")))
	w := httptest.NewRecorder()

	srv.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestHandleListTraces_RequiresNamespace(t *testing.T) {
	srv := newTestServer(t, &fakeStore{})
	req := httptest.NewRequest(http.MethodGet, "/v1/traces", nil)
	w := httptest.NewRecorder()

	srv.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing namespace, got %d", w.Code)
	}
}
