-- Pulse trace storage schema — migration 000001
-- Partitioned monthly; partitions created by nightly CronJob.

CREATE EXTENSION IF NOT EXISTS "pgcrypto";

-- traces: one row per LLM API call.
-- Partitioned by created_at (RANGE) for efficient purges at retention boundary.
CREATE TABLE IF NOT EXISTS traces (
    id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
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
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now()
) PARTITION BY RANGE (created_at);

-- Initial partition covering the current month. Additional months added by CronJob.
CREATE TABLE IF NOT EXISTS traces_default PARTITION OF traces DEFAULT;

-- Namespace-scoped access via Row-Level Security.
ALTER TABLE traces ENABLE ROW LEVEL SECURITY;

CREATE POLICY traces_ns_isolation ON traces
    USING (llmbackend_namespace = current_setting('app.namespace', true));

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

CREATE INDEX IF NOT EXISTS idx_metrics_hourly_ns ON metrics_hourly (llmbackend_namespace, llmbackend_name, hour DESC);
