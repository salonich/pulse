// Package store provides the Postgres-backed trace persistence layer.
//
// All reads and writes are wrapped in a transaction with `app.namespace` set
// to the trace's namespace. The Postgres RLS policies on `traces` and
// `metrics_hourly` filter on this GUC, so the runtime DB role MUST NOT be the
// table owner — RLS is bypassed for owners. See deploy/migrations/000001 for
// the `pulse_runtime` role.
package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNamespaceRequired is returned when a query that depends on RLS is called
// without a namespace. Returning silently-empty rows would mask a bug.
var ErrNamespaceRequired = errors.New("namespace is required for tenant-scoped queries")

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
	ListTraces(ctx context.Context, namespace string, limit int) ([]Trace, error)
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

// withNamespace runs fn inside a transaction with `app.namespace` bound to ns.
// set_config(..., true) makes the GUC local to the transaction so concurrent
// queries on other connections aren't affected.
func (s *pgStore) withNamespace(ctx context.Context, ns string, fn func(pgx.Tx) error) error {
	if ns == "" {
		return ErrNamespaceRequired
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, "SELECT set_config('app.namespace', $1, true)", ns); err != nil {
		return fmt.Errorf("setting app.namespace: %w", err)
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// InsertTrace writes a single trace row, scoped by RLS to t.LLMBackendNamespace.
func (s *pgStore) InsertTrace(ctx context.Context, t Trace) error {
	const q = `
		INSERT INTO traces
			(llmbackend_namespace, llmbackend_name, model, provider,
			 prompt_tokens, completion_tokens, latency_ms, cost_usd,
			 status, prompt_version, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`

	return s.withNamespace(ctx, t.LLMBackendNamespace, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, q,
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
	})
}

// ListTraces returns the most recent traces for a namespace. namespace is required;
// unscoped reads would silently return zero rows under RLS.
func (s *pgStore) ListTraces(ctx context.Context, namespace string, limit int) ([]Trace, error) {
	if limit <= 0 {
		limit = 50
	}
	if namespace == "" {
		return nil, ErrNamespaceRequired
	}

	const q = `SELECT llmbackend_namespace, llmbackend_name, model, provider,
				prompt_tokens, completion_tokens, latency_ms, cost_usd,
				status, COALESCE(prompt_version,''), created_at
			FROM traces WHERE llmbackend_namespace = $1
			ORDER BY created_at DESC LIMIT $2`

	var traces []Trace
	err := s.withNamespace(ctx, namespace, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, q, namespace, limit)
		if err != nil {
			return fmt.Errorf("querying traces: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			var t Trace
			if err := rows.Scan(
				&t.LLMBackendNamespace, &t.LLMBackendName, &t.Model, &t.Provider,
				&t.PromptTokens, &t.CompletionTokens, &t.LatencyMS, &t.CostUSD,
				&t.Status, &t.PromptVersion, &t.CreatedAt,
			); err != nil {
				return fmt.Errorf("scanning trace row: %w", err)
			}
			traces = append(traces, t)
		}
		return rows.Err()
	})
	return traces, err
}

func (s *pgStore) Close() { s.pool.Close() }
