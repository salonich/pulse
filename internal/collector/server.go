// Package collector receives trace payloads from proxy sidecars and writes them to Postgres.
package collector

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/velorai/pulse/internal/pricing"
	"github.com/velorai/pulse/internal/store"
	"github.com/velorai/pulse/internal/trace"
)

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
	r.Get("/v1/traces", s.handleListTraces)
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
	_ = r.Body.Close()

	var t trace.Trace
	if err := json.Unmarshal(body, &t); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if msg := t.Validate(); msg != "" {
		http.Error(w, msg, http.StatusBadRequest)
		return
	}

	// Server-side cost enrichment is the single source of truth — proxies
	// emit tokens only, the collector applies the cluster pricing table.
	t.CostUSD = s.pricing.Calculate(t.Model, t.PromptTokens, t.CompletionTokens)
	if t.CreatedAt.IsZero() {
		t.CreatedAt = time.Now().UTC()
	}

	row := store.Trace{
		LLMBackendNamespace: t.LLMBackendNamespace,
		LLMBackendName:      t.LLMBackendName,
		Model:               t.Model,
		Provider:            t.Provider,
		PromptTokens:        t.PromptTokens,
		CompletionTokens:    t.CompletionTokens,
		LatencyMS:           t.LatencyMS,
		CostUSD:             t.CostUSD,
		Status:              t.Status,
		PromptVersion:       t.PromptVersion,
		CreatedAt:           t.CreatedAt,
	}

	if err := s.store.InsertTrace(r.Context(), row); err != nil {
		s.log.Error("failed to insert trace", "error", err,
			"namespace", t.LLMBackendNamespace, "name", t.LLMBackendName)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) handleListTraces(w http.ResponseWriter, r *http.Request) {
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		http.Error(w, "namespace query parameter is required", http.StatusBadRequest)
		return
	}
	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		if v, err := strconv.Atoi(l); err == nil && v > 0 {
			limit = v
		}
	}

	traces, err := s.store.ListTraces(r.Context(), ns, limit)
	if err != nil {
		s.log.Error("failed to list traces", "error", err, "namespace", ns)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(traces)
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
