// Package collector receives trace payloads from proxy sidecars and writes them to Postgres.
package collector

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/velorai/pulse/internal/pricing"
	"github.com/velorai/pulse/internal/store"
)

// tracePayload is the JSON body expected at POST /v1/traces.
type tracePayload struct {
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

func (p *tracePayload) validate() string {
	if p.LLMBackendNamespace == "" {
		return "llmbackend_namespace is required"
	}
	if p.LLMBackendName == "" {
		return "llmbackend_name is required"
	}
	if p.Provider == "" {
		return "provider is required"
	}
	if p.LatencyMS < 0 {
		return "latency_ms must be non-negative"
	}
	return ""
}

// Server is the collector HTTP server.
type Server struct {
	store   store.Store
	pricing *pricing.Table
	log     *slog.Logger
}

// New creates a Server backed by the given Store.
func New(s store.Store, pt *pricing.Table, log *slog.Logger) *Server {
	return &Server{store: s, pricing: pt, log: log}
}

// Handler returns the HTTP handler for the collector.
func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(middleware.RequestID)

	r.Post("/v1/traces", s.handleTrace)
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	return r
}

func (s *Server) handleTrace(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1 MiB max
	if err != nil {
		http.Error(w, "reading body: "+err.Error(), http.StatusBadRequest)
		return
	}
	r.Body.Close()

	var p tracePayload
	if err := json.Unmarshal(body, &p); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if msg := p.validate(); msg != "" {
		http.Error(w, msg, http.StatusBadRequest)
		return
	}

	// Server-side cost enrichment: fill in cost if proxy sent zero (unknown model).
	if p.CostUSD == 0 && s.pricing != nil {
		p.CostUSD = s.pricing.Calculate(p.Model, p.PromptTokens, p.CompletionTokens)
	}
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now().UTC()
	}

	t := store.Trace{
		LLMBackendNamespace: p.LLMBackendNamespace,
		LLMBackendName:      p.LLMBackendName,
		Model:               p.Model,
		Provider:            p.Provider,
		PromptTokens:        p.PromptTokens,
		CompletionTokens:    p.CompletionTokens,
		LatencyMS:           p.LatencyMS,
		CostUSD:             p.CostUSD,
		Status:              p.Status,
		PromptVersion:       p.PromptVersion,
		CreatedAt:           p.CreatedAt,
	}

	if err := s.store.InsertTrace(r.Context(), t); err != nil {
		s.log.Error("failed to insert trace", "error", err,
			"namespace", p.LLMBackendNamespace, "name", p.LLMBackendName)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusAccepted)
}

// ListenAndServe starts the collector HTTP server on addr.
func (s *Server) ListenAndServe(ctx context.Context, addr string) error {
	srv := &http.Server{
		Addr:         addr,
		Handler:      s.Handler(),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()
	s.log.Info("collector listening", "addr", addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}
