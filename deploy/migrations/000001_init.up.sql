-- Pulse trace storage schema — migration 000001
-- Partitioned monthly; partitions created by nightly CronJob.

CREATE EXTENSION IF NOT EXISTS "pgcrypto";

-- Runtime role used by the collector. Must NOT own any tables — RLS is bypassed
-- for table owners, so the collector connecting as the schema owner would
-- silently see every namespace. The bootstrap user (from POSTGRES_USER) is the
-- owner; pulse_runtime is the runtime principal.
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'pulse_runtime') THEN
        CREATE ROLE pulse_runtime WITH LOGIN PASSWORD 'pulse_runtime';
    END IF;
END
$$;

-- traces: one row per LLM API call.
-- Partitioned by created_at (RANGE) for efficient purges at retention boundary.
CREATE TABLE IF NOT EXISTS traces (
    id                   UUID NOT NULL DEFAULT gen_random_uuid(),
    llmbackend_namespace TEXT        NOT NULL,
    llmbackend_name      TEXT        NOT NULL,
    model                TEXT,
    provider             TEXT,
    prompt_tokens        INT         NOT NULL DEFAULT 0,
    completion_tokens    INT         NOT NULL DEFAULT 0,
    latency_ms           INT         NOT NULL DEFAULT 0,
    cost_usd             NUMERIC(12, 8) NOT NULL DEFAULT 0,
    status               INT         NOT NULL DEFAULT 0,
    prompt_version       TEXT,
    tags                 JSONB,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);

-- Initial partition covering the current month. Additional months added by CronJob.
CREATE TABLE IF NOT EXISTS traces_default PARTITION OF traces DEFAULT;

-- Namespace-scoped access via Row-Level Security.
ALTER TABLE traces ENABLE ROW LEVEL SECURITY;

-- USING enforces the namespace filter on reads; WITH CHECK enforces it on writes
-- so a misconfigured collector cannot insert rows it could not subsequently read.
DROP POLICY IF EXISTS traces_ns_isolation ON traces;
CREATE POLICY traces_ns_isolation ON traces
    USING (llmbackend_namespace = current_setting('app.namespace', true))
    WITH CHECK (llmbackend_namespace = current_setting('app.namespace', true));

GRANT SELECT, INSERT ON traces TO pulse_runtime;

-- Indexes for dashboard query patterns.
CREATE INDEX IF NOT EXISTS idx_traces_ns_created ON traces (llmbackend_namespace, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_traces_ns_model ON traces (llmbackend_namespace, model, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_traces_ns_version ON traces (llmbackend_namespace, prompt_version, created_at DESC);

-- metrics_hourly: pre-aggregated rollup for fast dashboard queries.
-- Populated hourly by pg_cron (configured separately; requires pg_cron extension).
CREATE TABLE IF NOT EXISTS metrics_hourly (
    llmbackend_namespace TEXT        NOT NULL,
    llmbackend_name      TEXT        NOT NULL,
    hour                 TIMESTAMPTZ NOT NULL,
    model                TEXT,
    provider             TEXT,
    request_count        BIGINT      NOT NULL DEFAULT 0,
    total_cost_usd       NUMERIC(14, 8) NOT NULL DEFAULT 0,
    p50_ms               INT,
    p95_ms               INT,
    p99_ms               INT,
    error_count          BIGINT      NOT NULL DEFAULT 0,
    PRIMARY KEY (llmbackend_namespace, llmbackend_name, hour, model, provider)
) PARTITION BY RANGE (hour);

CREATE TABLE IF NOT EXISTS metrics_hourly_default PARTITION OF metrics_hourly DEFAULT;

ALTER TABLE metrics_hourly ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS metrics_hourly_ns_isolation ON metrics_hourly;
CREATE POLICY metrics_hourly_ns_isolation ON metrics_hourly
    USING (llmbackend_namespace = current_setting('app.namespace', true))
    WITH CHECK (llmbackend_namespace = current_setting('app.namespace', true));

GRANT SELECT, INSERT ON metrics_hourly TO pulse_runtime;

CREATE INDEX IF NOT EXISTS idx_metrics_hourly_ns ON metrics_hourly (llmbackend_namespace, llmbackend_name, hour DESC);
