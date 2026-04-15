// Package store provides the Postgres-backed trace persistence layer.
package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Trace is the data captured for a single LLM API call.
type Trace struct {
	LLMBackendNamespace string
	LLMBackendName      string
	Model               string
	Provider            string
	PromptTokens        int
	CompletionTokens    int
	LatencyMS           int
	CostUSD             float64
	Status              int
	PromptVersion       string
	CreatedAt           time.Time
}

// Store persists traces to Postgres.
type Store interface {
	InsertTrace(ctx context.Context, t Trace) error
	Close()
}

type pgStore struct {
	pool *pgxpool.Pool
}

// New opens a connection pool to the Postgres DSN and returns a Store.
func New(ctx context.Context, dsn string) (Store, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("opening postgres pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pinging postgres: %w", err)
	}
	return &pgStore{pool: pool}, nil
}

// InsertTrace writes a single trace row. The namespace RLS variable is set per-query.
func (s *pgStore) InsertTrace(ctx context.Context, t Trace) error {
	const q = `
		INSERT INTO traces
			(llmbackend_namespace, llmbackend_name, model, provider,
			 prompt_tokens, completion_tokens, latency_ms, cost_usd,
			 status, prompt_version, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`

	_, err := s.pool.Exec(ctx, q,
		t.LLMBackendNamespace, t.LLMBackendName,
		t.Model, t.Provider,
		t.PromptTokens, t.CompletionTokens,
		t.LatencyMS, t.CostUSD,
		t.Status, t.PromptVersion,
		t.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("inserting trace: %w", err)
	}
	return nil
}

func (s *pgStore) Close() { s.pool.Close() }
