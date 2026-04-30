# Velorai Pulse — LLM Observability Operator
## Product Requirements Document v2.0

> **Supersedes:** v1.0 (SDK/SaaS model — obsolete)
> **Version:** 2.0 | **Date:** April 2026
> **Repo:** `github.com/velorai/pulse`
> **Stack:** Go · Kubernetes Operator · NGINX Gateway Fabric · controller-runtime
> **Capture (MVP):** Proxy sidecar → eBPF (Iteration 2)

---

## Table of Contents

1. [Context & Standards Alignment](#1-context--standards-alignment)
2. [Product Overview](#2-product-overview)
3. [CRD Design](#3-crd-design)
4. [Functional Requirements](#4-functional-requirements)
5. [Non-Functional Requirements](#5-non-functional-requirements)
6. [High-Level Design](#6-high-level-design)
7. [Execution Stories](#7-execution-stories)
8. [Weekend 1 Sprint Plan](#8-weekend-1-sprint-plan)
9. [Technology Stack](#9-technology-stack)
10. [Iteration Roadmap](#10-iteration-roadmap)

---

## 1. Context & Standards Alignment

### 1.1 Why This Supersedes v1.0

v1.0 described a SaaS SDK/proxy product where teams integrated by changing code and pointing environment variables at a Velorai-hosted service. That model was replaced after recognising a fundamentally stronger architecture: a Kubernetes-native operator that installs into any cluster, requires zero application code changes, and configures itself declaratively through CRDs.

### 1.2 The Kubernetes AI Gateway Working Group

> ⚑ **Critical context.** The Kubernetes `wg-ai-gateway` working group (`github.com/kubernetes-sigs/wg-ai-gateway`) is actively standardising the exact problem space Pulse operates in. Every design decision in this document is made with explicit awareness of their proposals. Pulse is designed to be a **reference implementation** of these emerging standards — not a product that will need painful migration later.

| WG Proposal | Status | What it standardises | Pulse alignment |
|---|---|---|---|
| Egress Gateways (GEP-Egress) | Proposed | How K8s workloads reach external LLM providers; `Backend` CRD for FQDN destinations with TLS, credential injection, and protocol config | `LLMBackend.spec.providers[].backendRef` references a Backend-compatible resource today; will reference the official `Backend` CRD when stable |
| Payload Processing | Proposed | Declarative pipeline for processing HTTP request/response bodies — PII detection, prompt injection, semantic caching, guardrails | `LLMBackend.spec.processors[]` field reserved as placeholder; wired to `PayloadProcessingPipeline` when GEP lands |
| Gateway API HTTPRoute / RateLimitPolicy | Stable / Experimental | Standard routing and rate limiting via NGINX Gateway Fabric and other implementations | Operator auto-creates `HTTPRoute` and `RateLimitPolicy` — fully Gateway API compliant |
| Backend credential injection (`extensions[]`) | Proposed | Standard pattern for injecting API keys into egress requests without workloads managing secrets | Operator uses the `inject-credentials` extension pattern for LLM provider API key management |
| MCP protocol (`BackendProtocol: MCP`) | Proposed | Model Context Protocol as a first-class backend protocol | Proxy sidecar protocol roadmap includes MCP in Iteration 3 |

> ⚑ **Action:** Join `#wg-ai-gateway` on Kubernetes Slack. Attend Thursday 2PM EST meetings. Submit Pulse as a reference implementation once MVP is working — this is a significant community credibility signal.

### 1.3 What Changed From v1.0

| Dimension | v1.0 (obsolete) | v2.0 (this document) |
|---|---|---|
| Distribution | SaaS — sign up, get API key | `kubectl apply` — installs into any cluster |
| Integration | Change code + redeploy app | Zero code changes — operator injects sidecar |
| Configuration | SDK config objects + env vars | CRD YAML — declarative, GitOps-friendly |
| Capture method | SDK wraps LLM client calls | Proxy sidecar auto-injected by mutating webhook |
| Prometheus | Manual setup by customer | Operator auto-creates `ServiceMonitor` |
| Grafana | Manual dashboard import | Operator auto-creates `GrafanaDashboard` resource |
| NGINX Gateway | Manual HTTPRoute config | Operator auto-creates `HTTPRoute` + `RateLimitPolicy` |
| API key management | Stored in Velorai database | Customer K8s Secrets — never leaves their cluster |
| Business model | Per-seat SaaS | Open core — operator OSS, dashboard + advanced features paid |
| Standards alignment | None | wg-ai-gateway Backend GEP + Gateway API + Payload Processing GEP |
| Comparable products | Helicone, Langfuse | cert-manager, Prometheus Operator, Istio |

---

## 2. Product Overview

### 2.1 What Pulse Does

Velorai Pulse is a Kubernetes operator that gives engineering teams complete observability into every LLM call made anywhere in their cluster — cost, latency, token usage, errors, and model performance — with **zero application code changes**. A platform engineer installs the operator once. Any team that wants to monitor their LLM usage applies a single YAML file. The operator handles everything else.

### 2.2 Install Experience

| Step | Command / Action | Time | What happens |
|---|---|---|---|
| 1 | `kubectl apply -f https://velorai.com/pulse/operator.yaml` | 30s | Installs operator deployment, CRD definitions, RBAC, mutating webhook, collector service in `pulse-system` |
| 2 | `kubectl apply -f my-llmbackend.yaml` | 10s | Operator reconciliation loop fires — detects the new `LLMBackend` |
| 3 | (automatic) | 60s | Operator injects proxy sidecar into matching pods, creates `ServiceMonitor`, creates `GrafanaDashboard`, creates `HTTPRoute` and `RateLimitPolicy` |
| 4 | (automatic) | < 5min | Grafana dashboard live with real cost and latency. `LLMBackend` status shows `Ready=True` |

### 2.3 Target Users

| Persona | Role | Pain solved |
|---|---|---|
| Platform / DevOps Engineer | Installs and manages the operator cluster-wide | Installs once for all teams. No per-team SDK work. Standard K8s tooling. |
| Engineering Lead | Owns services that call LLMs | Real-time cost and latency per service. Alert on spikes before the bill arrives. |
| CTO / VP Engineering | Budget and architecture decisions | Monthly LLM spend attribution per service. ROI comparison across models. |
| Security / Compliance Engineer | Controls data leaving the cluster | API keys stay in cluster Secrets — never sent to a third party. All LLM egress goes through gateway with audit log. |
| Individual Developer | Builds features with LLMs | Prompt version comparison. See which call costs the most. Debug errors with trace explorer. |

### 2.4 Repository Layout

```
github.com/velorai/pulse
├── cmd/
│   ├── operator/          # K8s operator main (controller-runtime reconciliation loop)
│   ├── proxy/             # Proxy sidecar main (injected into customer pods)
│   └── collector/         # Trace collector main (receives traces from all sidecars)
├── internal/
│   ├── controller/        # LLMBackend and PulseConfig reconcilers
│   ├── webhook/           # Mutating webhook for pod sidecar injection
│   ├── proxy/             # Proxy: forwarding, response capture, streaming tee, trace extraction
│   ├── collector/         # Trace ingestion, validation, cost enrichment, Postgres write pipeline
│   ├── gateway/           # NGINX GF HTTPRoute and RateLimitPolicy resource builders
│   ├── observability/     # ServiceMonitor and GrafanaDashboard resource builders
│   ├── store/             # Postgres data access layer (pgx v5)
│   └── pricing/           # LLM pricing table — loaded from ConfigMap, hot-reloaded
├── api/v1alpha1/          # CRD Go types: LLMBackend, PulseConfig (controller-gen)
├── config/
│   ├── crd/               # Generated CRD YAML manifests
│   ├── rbac/              # ClusterRole and ClusterRoleBinding for operator
│   └── webhook/           # MutatingWebhookConfiguration manifest
├── deploy/
│   ├── helm/pulse/        # Helm chart — primary install method
│   ├── grafana/           # Pre-built Grafana dashboard JSON
│   └── migrations/        # Postgres schema migrations (golang-migrate)
├── dashboard/             # Next.js 14 dashboard
└── docs/examples/         # Example LLMBackend and PulseConfig YAML for common providers
```

---

## 3. CRD Design

> ⚑ CRD field naming is explicitly designed for forward-compatibility with the wg-ai-gateway Backend GEP. The `providers[].backendRef` field follows the GEP's consumer resource model. When the official `Backend` CRD reaches stable in Gateway API, the operator will support referencing it directly with **zero customer-facing API changes**.

### 3.1 LLMBackend CRD

#### Example

```yaml
apiVersion: pulse.velorai.com/v1alpha1
kind: LLMBackend
metadata:
  name: checkout-anthropic
  namespace: my-app
spec:
  # Which K8s service makes LLM calls
  targetService:
    name: checkout-api
    namespace: my-app
    port: 8080

  # LLM providers this service calls
  # backendRef naming aligns with wg-ai-gateway Backend GEP consumer resource model
  providers:
    - name: anthropic
      backendRef:
        name: anthropic-backend   # internal BackendSpec today; official Backend CRD when stable
        namespace: pulse-system
      credentialRef:              # K8s Secret — follows wg-ai-gateway inject-credentials pattern
        name: anthropic-api-key
        namespace: my-app

    - name: openai
      backendRef:
        name: openai-backend
      credentialRef:
        name: openai-api-key

  # What to capture
  capture:
    enabled: true
    includePrompts: false   # privacy off by default
    sampleRate: 1.0         # 1.0 = 100%

  # API gateway integration
  gateway:
    type: nginx-gateway-fabric
    name: velorai-gateway
    namespace: velorai-gateway

  # Observability auto-setup
  observability:
    prometheus: true    # auto-create ServiceMonitor
    grafana: true       # auto-create GrafanaDashboard
    rateLimit: true     # auto-create RateLimitPolicy on gateway

  # Alert rules — creates PrometheusRule resources automatically
  alerts:
    - metric: cost_usd_per_hour
      threshold: 50
      notify: "slack://team-alerts"
    - metric: error_rate
      threshold: 0.05
      notify: "email://oncall@company.com"

  # RESERVED — wg-ai-gateway PayloadProcessingPipeline GEP (Iteration 3)
  # Accepted by CRD schema, ignored by reconciler until GEP lands
  processors: []
```

#### Spec Fields

| Field | Type | Required | Description | wg-ai-gateway alignment |
|---|---|---|---|---|
| `spec.targetService.name` | string | Yes | K8s Service whose pods make LLM calls. Operator injects proxy sidecar into pods backing this Service. | Consumer resource — describes how gateway connects to this service |
| `spec.targetService.namespace` | string | Yes | Namespace of the target Service | — |
| `spec.targetService.port` | int32 | Yes | Port the target Service listens on | — |
| `spec.providers[]` | []Provider | Yes | List of LLM providers this service calls | Maps to Backend GEP destinations |
| `spec.providers[].name` | string | Yes | Provider: `anthropic`, `openai`, `google`, `mistral`, `cohere`, `custom` | `BackendType` enum in Backend GEP |
| `spec.providers[].backendRef.name` | string | Yes | Name of BackendSpec resource (today: internal type; future: Gateway API `Backend` CRD) | Aligns with Backend GEP `backendRef` pattern |
| `spec.providers[].credentialRef.name` | string | No | K8s Secret containing the API key at key `api-key`. Never stored by operator. | `inject-credentials` extension from Backend GEP |
| `spec.capture.enabled` | bool | No | Enable trace capture. Default: `true` | — |
| `spec.capture.includePrompts` | bool | No | Include prompt/completion text in traces. Default: `false` | — |
| `spec.capture.sampleRate` | float64 | No | Fraction of traces to capture. Default: `1.0` | — |
| `spec.gateway.type` | string | No | `nginx-gateway-fabric` (MVP). Future: `kong`, `envoy`, `aws-api-gateway` | Gateway API compliant |
| `spec.gateway.name` | string | No | Name of the Gateway resource on the cluster | `parentRef` in HTTPRoute spec |
| `spec.observability.prometheus` | bool | No | Auto-create `ServiceMonitor`. Default: `true` | — |
| `spec.observability.grafana` | bool | No | Auto-create `GrafanaDashboard`. Default: `true` | — |
| `spec.observability.rateLimit` | bool | No | Auto-create `RateLimitPolicy`. Default: `true` | — |
| `spec.alerts[].metric` | string | No | `cost_usd_per_hour`, `error_rate`, `p95_latency_ms`, `token_count_per_min` | — |
| `spec.alerts[].threshold` | float64 | No | Threshold value | — |
| `spec.alerts[].notify` | string | No | `slack://webhook-url` or `email://address` | — |
| `spec.processors[]` | []ProcessorRef | No | **RESERVED** — wg-ai-gateway `PayloadProcessingPipeline`. Accepted, ignored until Iteration 3. | `PayloadProcessingPipeline` GEP |

#### Status Fields

| Field | Type | Description |
|---|---|---|
| `status.conditions[]` | []metav1.Condition | Types: `SidecarInjected`, `PrometheusConfigured`, `GrafanaDashboardReady`, `GatewayConfigured`, `Ready` |
| `status.observedGeneration` | int64 | Last generation successfully reconciled |
| `status.sidecarInjectedPods` | int32 | Number of pods currently running with proxy sidecar |
| `status.observedCost.lastHourUSD` | string | LLM spend in last 60 minutes — updated every 5 minutes |
| `status.observedCost.todayUSD` | string | Spend since midnight UTC today |
| `status.observedCost.monthUSD` | string | Spend since first of current month |
| `status.ownedResources[]` | []ResourceRef | All K8s resources owned by this LLMBackend — garbage collected on deletion |

### 3.2 PulseConfig CRD (Cluster-Wide, Applied Once)

```yaml
apiVersion: pulse.velorai.com/v1alpha1
kind: PulseConfig
metadata:
  name: cluster-config
  namespace: pulse-system
spec:
  captureMethod: sidecar      # sidecar (MVP) | ebpf (Iteration 2)

  proxy:
    image: ghcr.io/velorai/pulse-proxy:latest
    resources:
      cpu: 100m
      memory: 128Mi

  collector:
    endpoint: http://pulse-collector.pulse-system:9090

  storage:
    retentionDays: 90
    backend: postgres           # postgres | clickhouse (high-volume)

  dashboard:
    enabled: true
    ingress: pulse.internal.company.com
```

---

## 4. Functional Requirements

> **Priority key:** `Must` = MVP Weekend 1 · `Should` = Month 2 · `Could` = Month 3+

### 4.1 Operator — Core Reconciliation

| ID | Requirement | Priority | Acceptance Criteria |
|---|---|---|---|
| FR-OP-01 | Operator shall watch `LLMBackend` resources across all namespaces and trigger reconciliation within 5 seconds of any create, update, or delete event | Must | Creating an `LLMBackend` triggers sidecar injection within 5 seconds |
| FR-OP-02 | Reconcile function shall be idempotent — running it N times produces identical cluster state to running it once | Must | Applying same `LLMBackend` 10 times creates no duplicate resources |
| FR-OP-03 | All operator-created resources shall carry `ownerReferences` to their parent `LLMBackend` | Must | Deleting `LLMBackend` triggers garbage collection of all owned resources within 30 seconds |
| FR-OP-04 | Operator shall update `LLMBackend` status conditions after every reconciliation | Must | `kubectl describe llmbackend` shows accurate conditions within 10 seconds of any state change |
| FR-OP-05 | Operator shall expose `/metrics` on `:8080` in Prometheus exposition format | Must | Metrics include `reconcile_total`, `reconcile_errors_total`, `reconcile_duration_histogram`, `webhook_injections_total` |
| FR-OP-06 | Operator shall implement leader election — failover within 15 seconds of primary failure | Must | Killing leader pod; reconciliation resumes within 15 seconds |
| FR-OP-07 | Invalid `LLMBackend` specs shall be rejected at admission with a clear error message | Must | 10 invalid specs submitted; all rejected with human-readable errors |

### 4.2 Mutating Webhook — Sidecar Injection

| ID | Requirement | Priority | Acceptance Criteria |
|---|---|---|---|
| FR-WH-01 | Operator shall install a `MutatingWebhookConfiguration` intercepting Pod creation in namespaces labelled `pulse.velorai.com/inject=enabled` | Must | Unlabelled namespaces receive no injection |
| FR-WH-02 | Webhook shall inject a `pulse-proxy` container with: image from `PulseConfig`, resource limits, env vars rewriting `ANTHROPIC_BASE_URL` and `OPENAI_BASE_URL` to `localhost:8888` | Must | `kubectl describe pod` shows `pulse-proxy` container with correct env vars |
| FR-WH-03 | Webhook shall be idempotent — pods already containing `pulse-proxy` are not injected again | Must | Triggering injection twice on the same pod template creates no duplicate containers |
| FR-WH-04 | Webhook shall complete within 2 seconds | Must | Pod creation timing measured with webhook enabled |
| FR-WH-05 | Webhook shall fail open — pod creation proceeds if webhook is unreachable | Must | Scaling webhook to 0 replicas; pods still start |
| FR-WH-06 | Webhook shall inject a pricing `ConfigMap` volume mount into the proxy sidecar at `/etc/pulse/pricing.json` | Must | Proxy calculates cost correctly without any API call |

### 4.3 Proxy Sidecar

#### 4.3.1 Request Forwarding

| ID | Requirement | Priority | Acceptance Criteria |
|---|---|---|---|
| FR-PX-01 | Proxy shall intercept LLM calls on `localhost:8888` and forward to upstream: `/anthropic/*` → `api.anthropic.com`, `/openai/*` → `api.openai.com` | Must | Application calls succeed; responses identical to direct API calls |
| FR-PX-02 | Proxy shall support streaming (SSE/chunked) responses — piped to caller in real time while buffered for capture | Must | First byte delivered to caller within 5ms of first byte from upstream |
| FR-PX-03 | Proxy shall add no more than 20ms latency overhead at p95 for non-streaming requests | Must | k6 load test at 500 concurrent requests; p95 overhead measured |
| FR-PX-04 | Proxy shall fail open — LLM request forwarded successfully if collector is unreachable | Must | Stopping collector; application LLM calls continue with 100% success rate |

#### 4.3.2 Trace Capture

| ID | Requirement | Priority | Acceptance Criteria |
|---|---|---|---|
| FR-PX-05 | Proxy shall extract: provider, model, prompt tokens, completion tokens, latency (ms), HTTP status, streaming flag, timestamp | Must | All fields present in emitted trace |
| FR-PX-06 | Proxy shall calculate cost from pricing `ConfigMap` — accurate to within 0.5% of actual | Must | Verified against provider billing for 1,000 test calls |
| FR-PX-07 | Proxy shall expose `pulse_llm_requests_total` counter and `pulse_llm_latency_ms` histogram on `:9090/metrics` | Must | Metrics present after 10 LLM calls |
| FR-PX-08 | Proxy shall async-post trace to collector after returning response to application — never blocks LLM response path | Must | p99 latency overhead < 2ms confirmed by benchmark |
| FR-PX-09 | When `spec.capture.includePrompts: true`, proxy shall include prompt and completion text transmitted over mTLS | Should | Prompt text absent from trace when `includePrompts: false` (default) |
| FR-PX-10 | Proxy shall respect `sampleRate` — `0.1` captures ~10% of traces | Should | 1,000 requests with `sampleRate=0.1` yields ~100 traces in database |

### 4.4 Automatic Observability Setup

| ID | Requirement | Priority | Acceptance Criteria |
|---|---|---|---|
| FR-OBS-01 | When `observability.prometheus: true`, operator creates `ServiceMonitor` targeting `:9090` in the `LLMBackend` namespace | Must | Prometheus target shows `UP` within 2 minutes of `LLMBackend` creation |
| FR-OBS-02 | `ServiceMonitor` labels shall match default `kube-prometheus-stack` `serviceMonitorSelector` | Must | Works with default installation without custom selectors |
| FR-OBS-03 | When `spec.alerts[]` present, operator creates `PrometheusRule` with alerting rules | Must | Alertmanager fires test alert within 5 minutes of threshold breach |
| FR-OBS-04 | When `observability.grafana: true`, operator creates `GrafanaDashboard` resource with pre-built template | Must | Grafana shows dashboard within 2 minutes; all panels populated after first LLM call |
| FR-OBS-05 | Auto-created dashboard shall include: requests/sec, cost/hr, p50/p95/p99 latency, error rate, token breakdown, model distribution | Must | All panels render with data after 5 LLM calls |

### 4.5 NGINX Gateway Fabric Integration

| ID | Requirement | Priority | Acceptance Criteria |
|---|---|---|---|
| FR-GW-01 | When `spec.gateway.type: nginx-gateway-fabric`, operator creates `HTTPRoute` routing LLM-destined traffic through the configured gateway | Must | `HTTPRoute` exists with correct `parentRef` and `backendRef` after `LLMBackend` creation |
| FR-GW-02 | Operator creates `RateLimitPolicy` limiting LLM calls per service — default 600 req/min | Must | Sending >600 req/min returns 429 responses |
| FR-GW-03 | Operator creates `ObservabilityPolicy` enabling structured access logging for LLM routes | Should | NGINX access logs contain structured JSON entries for LLM traffic in Loki |
| FR-GW-04 | All gateway resources carry `ownerReferences` to parent `LLMBackend` | Must | Deleting `LLMBackend` garbage-collects all gateway resources |

### 4.6 Trace Collector

| ID | Requirement | Priority | Acceptance Criteria |
|---|---|---|---|
| FR-COL-01 | Collector exposes `POST /v1/traces` accepting trace JSON from proxy sidecars over mTLS | Must | Returns 202 within 50ms; trace visible in dashboard within 10 seconds |
| FR-COL-02 | Collector validates schema, enriches with server-side cost calculation, bulk-inserts using pgx pipeline mode | Must | Throughput >5,000 traces/sec per pod |
| FR-COL-03 | Collector runs hourly Postgres rollup into `metrics_hourly` via `pg_cron` | Must | Dashboard queries return within 500ms for any 90-day window |
| FR-COL-04 | Traces isolated by `llmbackend_namespace` via Postgres RLS — no cross-namespace queries | Must | Cross-namespace query returns zero rows — verified by penetration test |

### 4.7 Dashboard

| ID | Requirement | Priority | Acceptance Criteria |
|---|---|---|---|
| FR-DASH-01 | Four summary metric cards per `LLMBackend`: cost (month), avg latency (p50/p95/p99), request count, error rate — refresh every 30s | Must | Accurate data within 1 minute of LLM calls |
| FR-DASH-02 | Dual-axis line chart: request volume and cumulative cost over selected range (today/7d/30d/90d/custom) | Must | Renders within 1 second |
| FR-DASH-03 | Model breakdown table: cost, latency, error rate per model — sortable | Must | Updates within 30 seconds of new model being used |
| FR-DASH-04 | Trace explorer with cursor-based pagination, filterable by model, provider, status, latency range, cost range, date | Must | Filters apply within 500ms; filter state in URL |
| FR-DASH-05 | Cluster-wide `LLMBackend` list showing `Ready` status and `observedCost` for all namespaces | Must | Platform team can see all monitored services without kubectl |
| FR-DASH-06 | Prompt version comparison view showing cost/latency/error delta between two `prompt_version` tag values | Should | Comparison renders for any two version tags in trace data |

---

## 5. Non-Functional Requirements

### 5.1 Performance

| ID | Requirement | Priority | Acceptance Criteria |
|---|---|---|---|
| NFR-P-01 | Proxy sidecar adds ≤20ms latency at p95 for non-streaming requests | Must | k6 load test at 500 concurrent users |
| NFR-P-02 | Streaming first-token latency identical to direct call within 5ms tolerance | Must | Tested with Anthropic streaming API |
| NFR-P-03 | Collector sustains 5,000 trace writes/sec per pod | Must | k6 sustained 5 minutes; p99 write latency <200ms |
| NFR-P-04 | Dashboard API queries return within 500ms at p95 for any 90-day range | Must | Achieved via `metrics_hourly` rollup; verified with `EXPLAIN ANALYZE` |
| NFR-P-05 | Operator reconciliation completes within 10 seconds per `LLMBackend` | Must | Measured from `LLMBackend` creation to `Ready=True` |

### 5.2 Security

| ID | Requirement | Priority | Acceptance Criteria |
|---|---|---|---|
| NFR-S-01 | LLM provider API keys never leave the customer cluster — mounted from K8s Secrets at runtime; never stored or transmitted by operator | Must | Code review + penetration test confirms no credential transmission |
| NFR-S-02 | Proxy sidecar to collector communication uses mTLS (TLS 1.3) — certs issued by cert-manager, rotated every 90 days | Must | Intercepted traffic shows TLS 1.3 with client cert |
| NFR-S-03 | Proxy sidecar runs as UID 65534, `readOnlyRootFilesystem: true`, `allowPrivilegeEscalation: false` | Must | Pod security context verified; privileged containers rejected by admission controller |
| NFR-S-04 | Operator RBAC contains no wildcard rules — only specific resource types it manages | Must | RBAC audited; no `*` verbs or resources |
| NFR-S-05 | Prompt/completion text never transmitted or stored when `includePrompts: false` (default) | Must | Packet capture confirms no prompt text in proxy-to-collector traffic |
| NFR-S-06 | Sidecar injection only in namespaces with explicit `pulse.velorai.com/inject=enabled` label | Must | Pod created in unlabelled namespace receives no injection |
| NFR-S-07 | All Postgres credentials managed as `SealedSecrets` committed to Git | Must | No plaintext credentials in Git history |

### 5.3 Reliability

| ID | Requirement | Priority | Acceptance Criteria |
|---|---|---|---|
| NFR-R-01 | Proxy sidecar fails open on collector unavailability — LLM calls always succeed | Must | Stopping collector; application LLM calls 100% success rate |
| NFR-R-02 | Operator leader election failover within 15 seconds | Must | Killing leader pod; reconciliation resumes |
| NFR-R-03 | Webhook fails open — pod creation proceeds if webhook unreachable | Must | Webhook replicas set to 0; pods start without injection |
| NFR-R-04 | Operator implements exponential backoff with jitter on reconciliation failures — max interval 5 minutes | Must | Transient failure induced; retry behaviour verified in logs |
| NFR-R-05 | Collector minimum 2 replicas; HPA scales to 10 on CPU >70% | Must | HPA config in Helm values; anti-affinity spreads across nodes |

### 5.4 Observability of the Operator Itself

| ID | Requirement | Priority | Acceptance Criteria |
|---|---|---|---|
| NFR-O-01 | All components emit structured JSON logs: `service`, `level`, `msg`, `reconcile_id`, `namespace`, `name`, `latency_ms` | Must | Logs queryable by namespace and LLMBackend name in Loki |
| NFR-O-02 | Operator `/metrics` exposes `reconcile_total`, `reconcile_errors_total`, `reconcile_duration_histogram`, `webhook_injections_total` | Must | Metrics verified after 10 reconciliations |
| NFR-O-03 | Operator Grafana dashboard committed to `deploy/grafana/operator-dashboard.json` | Must | Dashboard renders correctly when imported |

### 5.5 Upgrade & Compatibility

| ID | Requirement | Priority | Acceptance Criteria |
|---|---|---|---|
| NFR-U-01 | CRD upgrades are backwards compatible — existing `LLMBackend` resources continue functioning | Must | Upgrade from `v1alpha1` to `v1beta1` with existing resources in place |
| NFR-U-02 | Operator Helm upgrade is zero-downtime — rolling update with readiness gates | Must | `LLMBackend` status conditions remain `Ready` throughout upgrade |
| NFR-U-03 | `spec.processors[]` accepted by CRD schema, ignored by reconciler — no error emitted | Must | `LLMBackend` with `processors[]` populated reconciles normally |

---

## 6. High-Level Design

### 6.1 Component Overview

| Component | Binary | Runs as | Replicas | Responsibility |
|---|---|---|---|---|
| Operator | `cmd/operator` | Deployment in `pulse-system` | 2 (leader election) | Watches `LLMBackend` CRDs; reconciles cluster state; manages webhook; creates owned resources |
| Mutating Webhook | `cmd/operator` (same binary) | HTTP server within operator pod on `:9443` | — | Intercepts Pod creation; injects `pulse-proxy` sidecar; rewrites LLM env vars |
| Proxy Sidecar | `cmd/proxy` | Container in customer pods (injected) | 1 per matching pod | Intercepts LLM calls at `localhost:8888`; forwards to upstream; captures traces; exposes `/metrics` |
| Collector | `cmd/collector` | Deployment in `pulse-system` | 2–10 (HPA) | Receives traces over mTLS; validates; enriches cost; bulk-writes to Postgres |
| Dashboard | `dashboard/` (Next.js) | Deployment in `pulse-system` | 2 | Web UI: cost charts, trace explorer, alert management, cluster-wide overview |
| Postgres | PostgreSQL 16 | StatefulSet in `pulse-system` | 3 (HA) | Traces, `metrics_hourly` rollups, alert rules, audit log |
| Redis | Redis 7 | StatefulSet in `pulse-system` | 1 | Rate limit counters, session cache, pricing table cache |

### 6.2 Reconciliation Loop — LLMBackend Controller

```
Event: LLMBackend CREATE/UPDATE/DELETE
  │
  ├─ 1.  Fetch LLMBackend from API server
  │      └─ If NotFound → return (ownerRefs handle cleanup)
  │
  ├─ 2.  Validate spec → emit ValidationFailed condition if invalid
  │
  ├─ 3.  Ensure pricing ConfigMap exists in pulse-system
  │
  ├─ 4.  Ensure target namespace has pulse.velorai.com/inject=enabled label
  │
  ├─ 5.  Ensure ServiceAccount + RBAC for proxy sidecar in target namespace
  │
  ├─ 6.  Create/Update ServiceMonitor  (if observability.prometheus=true)
  │
  ├─ 7.  Create/Update GrafanaDashboard (if observability.grafana=true)
  │
  ├─ 8.  Create/Update HTTPRoute        (if gateway configured)
  │
  ├─ 9.  Create/Update RateLimitPolicy  (if gateway configured)
  │
  ├─ 10. Create/Update PrometheusRule   (for each spec.alerts[] entry)
  │
  ├─ 11. Set ownerReference on all created resources → parent LLMBackend
  │
  ├─ 12. Patch LLMBackend status conditions + observedCost
  │
  └─ 13. Return RequeueAfter: 5min (periodic cost refresh)
```

### 6.3 Mutating Webhook — Injection Decision Tree

```
Pod CREATE admission request
  │
  ├─ Is namespace labelled pulse.velorai.com/inject=enabled?
  │   └─ No → admit unchanged
  │
  ├─ Does a LLMBackend target this pod's Service?
  │   └─ No → admit unchanged
  │
  ├─ Does pod already contain a pulse-proxy container?
  │   └─ Yes → admit unchanged (idempotent)
  │
  └─ Yes to all → return JSON patch:
       ├─ Add pulse-proxy container (image from PulseConfig)
       ├─ Rewrite ANTHROPIC_BASE_URL → http://localhost:8888/anthropic
       ├─ Rewrite OPENAI_BASE_URL   → http://localhost:8888/openai
       ├─ Mount pricing ConfigMap at /etc/pulse/pricing.json
       └─ Add PULSE_COLLECTOR env var → collector ClusterIP
```

### 6.4 Proxy Sidecar — Request Flows

#### Non-Streaming

```
App → localhost:8888/anthropic/v1/messages
  │
  ├─ Record start_time
  ├─ Forward request to api.anthropic.com (original headers + body)
  ├─ Receive full response
  ├─ Forward response to app ← app unblocked here
  │
  └─ Background goroutine:
       ├─ Parse response: model, input_tokens, output_tokens, status
       ├─ Calculate cost_usd from pricing ConfigMap
       ├─ Build Trace struct
       ├─ POST trace to collector over mTLS (async, non-blocking)
       └─ Increment Prometheus counters + histograms on :9090/metrics
```

#### Streaming (SSE / Chunked)

```
App → localhost:8888/anthropic/v1/messages (stream=true)
  │
  ├─ Open upstream connection to api.anthropic.com
  ├─ Create tee writer: chunks → app response writer AND internal buffer
  │   └─ App receives bytes in real time — zero added latency on first token
  │
  ├─ On final SSE chunk (message_stop / [DONE]):
  │   └─ Signal stream complete
  │
  └─ Background goroutine:
       ├─ Parse buffer for usage metadata in final chunk
       ├─ Build Trace struct (latency = stream_start → final_chunk)
       └─ POST trace to collector (async)
```

### 6.5 Data Model

#### Core Tables

```sql
-- Partitioned monthly; old partitions dropped by nightly CronJob
CREATE TABLE traces (
    id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    llmbackend_namespace TEXT NOT NULL,
    llmbackend_name      TEXT NOT NULL,
    model                TEXT,
    provider             TEXT,
    prompt_tokens        INT,
    completion_tokens    INT,
    latency_ms           INT,
    cost_usd             NUMERIC(10, 6),
    status               INT,
    prompt_version       TEXT,
    tags                 JSONB,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now()
) PARTITION BY RANGE (created_at);

-- RLS: no cross-namespace queries
ALTER TABLE traces ENABLE ROW LEVEL SECURITY;
CREATE POLICY traces_ns_isolation ON traces
    USING (llmbackend_namespace = current_setting('app.namespace'));

-- Pre-aggregated for fast dashboard queries
CREATE TABLE metrics_hourly (
    llmbackend_namespace TEXT NOT NULL,
    llmbackend_name      TEXT NOT NULL,
    hour                 TIMESTAMPTZ NOT NULL,
    model                TEXT,
    provider             TEXT,
    request_count        BIGINT,
    total_cost_usd       NUMERIC,
    p50_ms               INT,
    p95_ms               INT,
    p99_ms               INT,
    error_count          BIGINT
) PARTITION BY RANGE (hour);
```

#### Key Indexes

```sql
CREATE INDEX ON traces (llmbackend_namespace, created_at DESC);
CREATE INDEX ON traces (llmbackend_namespace, model, created_at DESC);
CREATE INDEX ON traces (llmbackend_namespace, prompt_version, created_at DESC);
CREATE INDEX ON metrics_hourly (llmbackend_namespace, llmbackend_name, hour DESC);
```

### 6.6 Kubernetes Namespace Layout

```
pulse-system
  ├── operator          (Deployment, 2 replicas, leader election)
  ├── collector         (Deployment, 2–10 replicas, HPA)
  ├── dashboard         (Deployment, 2 replicas)
  ├── postgres          (StatefulSet, 3 replicas)
  └── redis             (StatefulSet, 1 replica)
  NetworkPolicy: ingress from proxy sidecars on :9090 (mTLS only)

velorai-gateway
  └── nginx-gateway-fabric (DaemonSet on ingress nodes)
  NetworkPolicy: ingress from Cloudflare IPs only

[customer namespaces]
  └── pulse-proxy containers (injected into existing pods)
  NetworkPolicy: egress to pulse-system:9090 (mTLS) + LLM providers :443

velorai-monitoring
  ├── prometheus-operator
  ├── grafana
  ├── loki
  └── promtail
```

### 6.7 wg-ai-gateway Design Decision Mapping

| Design decision | MVP implementation | Future migration (wg-ai-gateway) |
|---|---|---|
| Provider upstream config | Internal `BackendSpec` type with FQDN, port, TLS | When `Backend` CRD reaches stable: swap internal type for official CRD; zero customer-facing change |
| API key injection | Proxy mounts K8s Secret as env var | wg-ai-gateway `inject-credentials` extension — same Secret ref, standard pattern |
| Payload processors | `processors[]` accepted by CRD, ignored by reconciler | Wire to `PayloadProcessingPipeline` when GEP reaches implementable stage |
| Protocol support | HTTP and HTTP/2 only | `BackendProtocol: MCP` added to proxy in Iteration 3 |
| Gateway resources | `HTTPRoute` + `RateLimitPolicy` via `gateway.networking.k8s.io` | Already Gateway API compliant — works with any conformant implementation |

---

## 7. Execution Stories

> **Priority:** `M` = Must (Weekend 1) · `S` = Should (Month 2) · `C` = Could (Month 3+)
> **Points:** `2`=<1hr · `3`=1–2hr · `5`=2–4hr · `8`=4–8hr

### Epic 1 — Repository & CRD Bootstrap

| ID | Title | As a… | I want to… | So that… | P | Pts |
|---|---|---|---|---|---|---|
| PUL-001 | Repo scaffold + Go workspace | developer | initialise monorepo with `cmd/`, `internal/`, `api/`, `config/`, `deploy/` and `go.work` | start building without restructuring later | M | 2 |
| PUL-002 | CRD type — LLMBackend | developer | define `LLMBackend` Go struct in `api/v1alpha1` with all spec and status fields | generate CRD YAML with `controller-gen` | M | 5 |
| PUL-003 | CRD type — PulseConfig | developer | define `PulseConfig` Go struct | configure operator behaviour declaratively per cluster | M | 3 |
| PUL-004 | controller-gen CRD generation | developer | run `make generate` and have CRD YAML output to `config/crd/` | apply CRDs to a cluster from a single manifest | M | 2 |
| PUL-005 | Helm chart scaffold | ops engineer | deploy all operator resources via `helm install pulse ./deploy/helm/pulse` | manage operator installation consistently across environments | M | 5 |
| PUL-006 | Postgres schema + golang-migrate | developer | run `make migrate up` and have all tables created with RLS policies | store and query trace data safely from day one | M | 5 |
| PUL-007 | Sealed Secrets bootstrap | ops engineer | encrypt all credentials as SealedSecrets committed to Git | manage secrets safely in GitOps workflow without Vault | M | 3 |
| PUL-008 | CI pipeline (GitHub Actions) | developer | run `go test ./...`, `go vet`, `staticcheck`, `controller-gen` diff check on every PR | catch bugs and CRD drift before merge | M | 3 |

### Epic 2 — Operator & Reconciler

| ID | Title | As a… | I want to… | So that… | P | Pts |
|---|---|---|---|---|---|---|
| PUL-009 | Operator main + controller-runtime | developer | initialise `controller-manager` with scheme, leader election, health endpoints, metrics server | run the operator as a proper K8s controller | M | 5 |
| PUL-010 | LLMBackend reconciler skeleton | developer | have `Reconcile()` called on every `LLMBackend` event | start implementing reconcile steps against a real cluster | M | 3 |
| PUL-011 | Namespace injection label reconciler | developer | have reconciler add `pulse.velorai.com/inject=enabled` to target namespace | enable webhook to inject sidecars without manual labelling | M | 3 |
| PUL-012 | Pricing ConfigMap reconciler | developer | have reconciler create `pulse-pricing` ConfigMap in `pulse-system` | give proxy sidecars accurate cost calculation without API calls | M | 3 |
| PUL-013 | ServiceMonitor reconciler | developer | have reconciler create `ServiceMonitor` targeting `:9090` | auto-configure Prometheus scraping with zero manual steps | M | 5 |
| PUL-014 | GrafanaDashboard reconciler | developer | have reconciler create `GrafanaDashboard` from `deploy/grafana/` template | auto-provision a live Grafana dashboard per `LLMBackend` | M | 5 |
| PUL-015 | HTTPRoute reconciler (NGINX GF) | ops engineer | have reconciler create `HTTPRoute` on the configured NGINX Gateway | route LLM egress through gateway for visibility and rate limiting | M | 8 |
| PUL-016 | RateLimitPolicy reconciler | ops engineer | have reconciler create `RateLimitPolicy` on the gateway | protect against runaway LLM cost from bugs or abuse | M | 5 |
| PUL-017 | ownerReferences on all owned resources | developer | have all operator-created resources carry `ownerReferences` | delete `LLMBackend` and have everything clean up automatically | M | 3 |
| PUL-018 | Status conditions reconciler | developer | have reconciler patch `LLMBackend` status after every reconcile | give operators a single `kubectl describe` to understand health | M | 5 |
| PUL-019 | observedCost status update | developer | have reconciler query `metrics_hourly` and write cost fields to status every 5 minutes | see live LLM cost in `kubectl describe` without opening dashboard | S | 5 |
| PUL-020 | PrometheusRule reconciler | developer | have reconciler create `PrometheusRule` from `spec.alerts[]` | wire alert rules to Alertmanager automatically | S | 5 |
| PUL-021 | Operator `/metrics` endpoint | developer | expose reconcile and webhook metrics on `:8080/metrics` | monitor operator health from existing Prometheus | M | 3 |
| PUL-022 | PulseConfig reconciler | developer | watch `PulseConfig` and update operator behaviour without restart | let platform teams configure operator declaratively | S | 5 |

### Epic 3 — Mutating Webhook

| ID | Title | As a… | I want to… | So that… | P | Pts |
|---|---|---|---|---|---|---|
| PUL-023 | Webhook server setup | developer | run an HTTPS webhook server on `:9443` with cert-manager-issued certificate | receive pod admission requests from the K8s API server | M | 5 |
| PUL-024 | MutatingWebhookConfiguration manifest | ops engineer | have `MutatingWebhookConfiguration` in `config/webhook/` | have K8s call the webhook for matching pods | M | 3 |
| PUL-025 | Namespace label check | developer | have webhook admit pods unchanged in unlabelled namespaces | ensure opt-in injection — never inject without explicit consent | M | 3 |
| PUL-026 | LLMBackend lookup in webhook | developer | have webhook find the matching `LLMBackend` for a pod being created | know which `LLMBackend` to inject for | M | 5 |
| PUL-027 | Proxy sidecar injection patch | developer | have webhook return JSON patch adding `pulse-proxy` container to pod spec | run the proxy alongside the application with zero pod YAML changes | M | 8 |
| PUL-028 | Env var rewrite in injection patch | developer | have webhook rewrite `ANTHROPIC_BASE_URL` and `OPENAI_BASE_URL` to `localhost:8888` | redirect LLM calls through proxy transparently | M | 5 |
| PUL-029 | Pricing ConfigMap volume mount | developer | have webhook add volume + volumeMount for pricing ConfigMap in proxy container | give proxy accurate cost calculation from ConfigMap | M | 3 |
| PUL-030 | Idempotent injection check | developer | have webhook skip injection for pods already containing `pulse-proxy` | prevent duplicate sidecars on pod restarts | M | 3 |
| PUL-031 | Webhook `failurePolicy: Ignore` | ops engineer | configure `MutatingWebhookConfiguration` with `failurePolicy: Ignore` | ensure pod creation always succeeds if webhook is unavailable | M | 2 |

### Epic 4 — Proxy Sidecar

| ID | Title | As a… | I want to… | So that… | P | Pts |
|---|---|---|---|---|---|---|
| PUL-032 | Proxy HTTP server | developer | run Go HTTP server on `:8888` routing `/anthropic/*` and `/openai/*` | intercept LLM calls from the application | M | 3 |
| PUL-033 | Non-streaming request forwarding | developer | forward non-streaming POST to `api.anthropic.com` and return response unchanged | prove transparent proxy works before adding capture | M | 5 |
| PUL-034 | Streaming response (tee pattern) | developer | forward SSE streaming response in real time while buffering for trace capture | support streaming without delaying first token | M | 8 |
| PUL-035 | Trace extraction — Anthropic | developer | parse Anthropic response and extract `model`, `input_tokens`, `output_tokens`, `stop_reason`, `latency` | capture accurate trace fields | M | 5 |
| PUL-036 | Trace extraction — OpenAI | developer | parse OpenAI response and extract equivalent fields | support OpenAI-format APIs and compatible providers | M | 5 |
| PUL-037 | Cost calculation from ConfigMap | developer | load `pricing.json` at startup; hot-reload on change; calculate `cost_usd` | accurate cost without calling any external service | M | 5 |
| PUL-038 | Async trace post to collector | developer | post trace to collector in background goroutine after returning LLM response | ensure proxy latency is not affected by collector write speed | M | 5 |
| PUL-039 | Fail-open on collector unreachable | developer | continue forwarding LLM requests when collector is down; drop trace with warning | never fail an LLM call due to Pulse unavailability | M | 3 |
| PUL-040 | Prometheus metrics on `:9090/metrics` | developer | expose `pulse_llm_requests_total` and `pulse_llm_latency_ms` histogram | provide metrics that ServiceMonitor scrapes | M | 3 |
| PUL-041 | Non-root security context | ops engineer | run proxy as UID 65534 with `readOnlyRootFilesystem: true` | meet Pod Security Standards restricted profile | M | 2 |
| PUL-042 | Sample rate support | developer | capture ~10% of traces when `PULSE_SAMPLE_RATE=0.1` | reduce collector load for very high-volume services | S | 3 |

### Epic 5 — Collector Service

| ID | Title | As a… | I want to… | So that… | P | Pts |
|---|---|---|---|---|---|---|
| PUL-043 | Collector HTTP server + mTLS | developer | run Go HTTP server on `:9090` with mTLS accepting trace POSTs | receive traces securely without an unauthenticated endpoint | M | 5 |
| PUL-044 | Trace schema validation | developer | validate incoming JSON and return structured 400 errors | prevent dirty data entering the traces table | M | 3 |
| PUL-045 | Cost enrichment on collector | developer | calculate `cost_usd` when absent from payload | ensure all traces have accurate cost regardless of proxy version | M | 3 |
| PUL-046 | Buffered write channel + pgx pipeline | developer | buffer traces in Go channel and drain in batches of 500 via pgx pipeline | sustain 5,000 trace writes/sec without blocking the HTTP handler | M | 8 |
| PUL-047 | Postgres RLS enforcement | developer | set `app.namespace` session variable; apply RLS to all tables | prevent cross-namespace trace access at database level | M | 5 |
| PUL-048 | Hourly rollup pg_cron job | developer | aggregate traces into `metrics_hourly` every hour via `pg_cron` | serve dashboard queries in <500ms | M | 5 |
| PUL-049 | Monthly partition CronJob | ops engineer | create next month's partition on the 25th; drop partitions beyond retention | maintain query performance as trace volume grows | M | 5 |
| PUL-050 | Collector HPA | ops engineer | configure HPA scaling collector from 2 to 10 pods on CPU >70% | handle traffic spikes from many sidecars simultaneously | M | 3 |

### Epic 6 — Dashboard

| ID | Title | As a… | I want to… | So that… | P | Pts |
|---|---|---|---|---|---|---|
| PUL-051 | Next.js scaffold + Clerk auth | developer | have Next.js 14 app with Clerk org auth and sidebar listing all `LLMBackend` resources | provide the shell for all dashboard pages | M | 5 |
| PUL-052 | Cluster-wide LLMBackend list | platform engineer | see all `LLMBackend` resources with `Ready` status, cost today, last-seen | monitor all services without kubectl | M | 5 |
| PUL-053 | Metric cards per LLMBackend | developer | see four metric cards with period-over-period comparison | understand LLM usage at a glance | M | 5 |
| PUL-054 | Time-series chart (Chart.js) | developer | see dual-axis cost and request volume chart over selected range | identify cost trends and spikes visually | M | 5 |
| PUL-055 | Model breakdown table | engineering lead | see cost, latency, error rate per model — sortable | identify most expensive or slowest model | M | 3 |
| PUL-056 | Trace explorer with filters | developer | browse last 10,000 traces with filter by model, provider, status, latency, cost, date | debug individual LLM calls | M | 8 |
| PUL-057 | Date range picker | developer | select today/7d/30d/90d/custom and have all components update | analyse any time window | M | 3 |
| PUL-058 | Prompt version comparison | developer | select two `prompt_version` values and see cost/latency/error delta | measure impact of prompt changes before rolling out | S | 8 |
| PUL-059 | Alert rule management UI | ops engineer | create, edit, delete alert rules from dashboard without editing YAML | configure alerting without kubectl | S | 5 |

### Epic 7 — Observability Infrastructure

| ID | Title | As a… | I want to… | So that… | P | Pts |
|---|---|---|---|---|---|---|
| PUL-060 | NGINX GF Helm install + config | ops engineer | install NGINX Gateway Fabric via Helm | have a K8s-native API gateway for all LLM egress routes | M | 3 |
| PUL-061 | cert-manager + Let's Encrypt | ops engineer | configure cert-manager for dashboard ingress TLS and proxy-to-collector mTLS | enable HTTPS and mTLS without manual certificate management | M | 3 |
| PUL-062 | Prometheus Operator install check | ops engineer | have Helm chart detect existing Prometheus Operator and skip duplicate install | plug into the team's existing Prometheus stack | M | 3 |
| PUL-063 | Operator Grafana dashboard | ops engineer | import `deploy/grafana/operator-dashboard.json` | monitor operator health from existing Grafana | M | 3 |
| PUL-064 | Loki log aggregation | ops engineer | configure Loki + Promtail for structured logs from operator, collector, proxy sidecars | query logs by namespace and `LLMBackend` name | M | 3 |
| PUL-065 | Cloudflare DNS + WAF | ops engineer | point `pulse.velorai.com` at cluster via Cloudflare proxy | protect dashboard endpoint without managing WAF rules manually | M | 2 |
| PUL-066 | ArgoCD GitOps app | ops engineer | define Pulse as ArgoCD Application syncing from `deploy/helm/pulse` | deploy changes declaratively with automatic sync and self-healing | S | 5 |

### Epic 8 — wg-ai-gateway Alignment (Month 2–3)

| ID | Title | As a… | I want to… | So that… | P | Pts |
|---|---|---|---|---|---|---|
| PUL-067 | Internal BackendSpec type | developer | define internal `BackendSpec` struct mirroring wg-ai-gateway Backend GEP schema | have a migration-ready type swappable for the official CRD with no customer changes | S | 5 |
| PUL-068 | backendRef field in LLMBackend provider | developer | replace inline upstream string with `providers[].backendRef` | align field naming with Backend GEP consumer resource pattern | S | 5 |
| PUL-069 | Credential injection via Secret ref | developer | implement API key injection via `providers[].credentialRef` Secret mount | follow the wg-ai-gateway `inject-credentials` extension pattern | S | 5 |
| PUL-070 | processors[] field reservation | developer | add `processors[]` to `LLMBackend` spec — accepted by CRD, ignored by reconciler | customer configs using `processors[]` are forward-compatible with PayloadProcessingPipeline GEP | S | 3 |
| PUL-071 | wg-ai-gateway community engagement | product | open a discussion in wg-ai-gateway GitHub presenting Pulse as a reference implementation | build community credibility and get early feedback on CRD design | S | 2 |

---

## 8. Weekend 1 Sprint Plan

> **Done condition:** A 3-minute Loom showing `kubectl apply` installing the operator, applying an `LLMBackend` YAML, and a Grafana dashboard showing live LLM cost and latency — **zero code changes to the application.**

> ⚑ **Scope discipline:** Everything in the tables below is the complete Weekend 1 scope. If it is not in the table it does not get built this weekend. Write every new idea in a parking lot doc and keep moving.

### Saturday

| Time | Task | Stories | Done when… |
|---|---|---|---|
| 09:00–10:00 | Repo scaffold, Go workspace, Makefile, Dockerfile, CI skeleton | PUL-001, PUL-008 (skeleton) | `make build` succeeds; `go test ./...` passes |
| 10:00–11:30 | CRD types + controller-gen + Helm chart scaffold | PUL-002, PUL-003, PUL-004, PUL-005 | `kubectl apply -f config/crd/` succeeds; `kubectl get llmbackends` returns empty list |
| 11:30–13:00 | Operator main + reconciler skeleton + leader election + `/metrics` | PUL-009, PUL-010, PUL-021 | Operator runs; reconcile log appears when `LLMBackend` is applied |
| 13:00–14:00 | **Lunch — write parking lot doc** | — | Parking lot has ≥10 explicitly deferred items |
| 14:00–16:00 | Namespace label reconciler + pricing ConfigMap + ServiceMonitor reconciler | PUL-011, PUL-012, PUL-013 | `ServiceMonitor` appears in namespace; namespace has injection label |
| 16:00–18:00 | GrafanaDashboard reconciler + status conditions | PUL-014, PUL-018 | `kubectl describe llmbackend` shows conditions; Grafana shows dashboard (even empty) |

### Sunday

| Time | Task | Stories | Done when… |
|---|---|---|---|
| 09:00–10:00 | Webhook server + `MutatingWebhookConfiguration` + namespace label check | PUL-023, PUL-024, PUL-025 | Webhook running; pod creation in labelled namespace calls webhook (visible in logs) |
| 10:00–12:00 | Sidecar injection patch + env var rewrite + pricing volume + idempotency | PUL-026, PUL-027, PUL-028, PUL-029, PUL-030 | `kubectl describe pod` shows `pulse-proxy` container with rewritten `ANTHROPIC_BASE_URL` |
| 12:00–14:00 | Proxy: non-streaming forward + Anthropic trace extraction + Prometheus metrics + fail-open | PUL-032, PUL-033, PUL-035, PUL-037, PUL-039, PUL-040 | Curl through proxy returns Anthropic response; `/metrics` shows request counter |
| 14:00–15:00 | Collector: HTTP server + validation + pgx write + Postgres schema | PUL-006, PUL-043, PUL-044, PUL-046 (simple) | Proxy posts trace; row appears in `traces` table |
| 15:00–16:00 | Deploy to real cluster (kind, Civo, or DigitalOcean) | PUL-005 (complete), PUL-065 | `helm install pulse` succeeds; all pods `Running`; `LLMBackend` `Ready=True` |
| 16:00–17:00 | Record Loom (3 min max) + post to `r/kubernetes` + LinkedIn | — | Video live; post published with GitHub repo link |

### Explicitly Out of Scope — Weekend 1

- Dashboard UI (use auto-provisioned Grafana for the demo)
- Streaming proxy — non-streaming only
- HTTPRoute + RateLimitPolicy reconcilers — NGINX integration in Month 2
- OpenAI proxy — Anthropic only for demo
- Collector mTLS — plain HTTP for Weekend 1; mTLS in Month 2
- pgx pipeline bulk insert — simple `INSERT` for MVP
- HPA, ArgoCD, Sealed Secrets — Month 2 infrastructure hardening
- wg-ai-gateway BackendSpec alignment — Month 2

### Month 2 Sprint Targets

| Week | Focus | Key stories |
|---|---|---|
| Week 1 | Streaming proxy + OpenAI + collector mTLS | PUL-034, PUL-036, PUL-043 (mTLS) |
| Week 2 | HTTPRoute + RateLimitPolicy + NGINX GF integration | PUL-015, PUL-016, PUL-060 |
| Week 3 | Dashboard UI — metric cards, charts, trace explorer | PUL-051 to PUL-057 |
| Week 4 | pgx pipeline + HPA + ArgoCD + wg-ai-gateway alignment | PUL-046, PUL-050, PUL-066, PUL-067 to PUL-071 |

---

## 9. Technology Stack

| Layer | Technology | Version | Justification |
|---|---|---|---|
| Operator framework | controller-runtime | v0.19+ | Industry standard for K8s operators (cert-manager, Prometheus Operator, crossplane). Reconciliation loop, leader election, webhook server, status conditions. |
| CRD generation | controller-gen | v0.16+ | Generates CRD YAML and DeepCopy methods from Go struct annotations |
| Language | Go | 1.22+ | Required for K8s operator ecosystem; high throughput for proxy and collector |
| HTTP router | Chi | v5 | Lightweight, idiomatic Go, middleware composable |
| Postgres driver | pgx | v5 | Fastest Go Postgres driver; pipeline mode for bulk writes |
| K8s API gateway | NGINX Gateway Fabric | 1.x | NGF expertise; K8s-native CRDs; Gateway API compliant `HTTPRoute` + `RateLimitPolicy` |
| TLS management | cert-manager | v1.14+ | Auto-rotating certs for dashboard ingress and proxy-to-collector mTLS |
| Prometheus integration | Prometheus Operator CRDs | v0.72+ | Operator creates `ServiceMonitor` + `PrometheusRule`; plugs into existing `kube-prometheus-stack` |
| Grafana integration | Grafana Operator CRDs | v5+ | Operator creates `GrafanaDashboard`; plugs into existing Grafana Operator |
| Frontend | Next.js 14 (App Router) | 14.x | Full-stack in one repo; SSR for fast initial load |
| Auth | Clerk | Latest | Org-level B2B auth with RBAC; K8s OIDC integration for cluster-level identity |
| Charts | Chart.js | 4.x | Lightweight line and bar charts for dashboard |
| Frontend deploy | Vercel | Managed | Zero-config GitHub deploys; preview URLs per PR |
| Database | PostgreSQL 16 | 16.x | RLS for namespace isolation; table partitioning; `pg_cron` for rollups |
| Cache | Redis 7 | 7.x | Rate limit counters; session cache; pricing table cache |
| Migrations | golang-migrate | v4 | Versioned SQL migrations; CLI + library |
| Secrets | Kubernetes Sealed Secrets | Latest | Safe Git storage of encrypted secrets; no Vault dependency at MVP |
| GitOps | ArgoCD | Latest | Declarative K8s deployments; self-healing; auditable history |
| Observability | Prometheus + Grafana + Loki + Promtail | Latest | OSS stack; K8s native |
| Load testing | k6 | Latest | Scripted scenarios for proxy and collector load tests |
| Packaging | Helm v3 | 3.x | Primary distribution format for operator |
| Edge security | Cloudflare | Managed | DDoS, WAF, DNS for dashboard public endpoint |
| Email (alerts) | Resend | Latest | Transactional email for alert notifications |
| Payments | Stripe | Latest | Subscription billing for open-core paid tier |

---

## 10. Iteration Roadmap

| Iteration | Timeline | Theme | Key deliverables | wg-ai-gateway milestone |
|---|---|---|---|---|
| **MVP** | Weekend 1 | Core operator + sidecar | Operator installs via `kubectl apply`. `LLMBackend` CRD. Mutating webhook. Proxy sidecar (non-streaming, Anthropic). Collector (basic writes). `ServiceMonitor` + `GrafanaDashboard` auto-create. Status conditions. 3-min demo. | CRD field naming aligned with Backend GEP. `processors[]` reserved. |
| **Iteration 1** | Month 2 | Production hardening | Streaming proxy (SSE). OpenAI support. Collector mTLS. pgx pipeline. `HTTPRoute` + `RateLimitPolicy` reconcilers. Dashboard UI v1. HPA. Sealed Secrets. ArgoCD. | `HTTPRoute` + `RateLimitPolicy` via `gateway.networking.k8s.io` — full Gateway API compliance. |
| **Iteration 2** | Month 3–4 | wg-ai-gateway alignment + eBPF | Internal `BackendSpec` type. `backendRef` field. Credential injection via Secret ref. eBPF `DaemonSet` (`captureMethod: ebpf`). Python + Node.js provider support. | Submit Pulse as reference implementation to wg-ai-gateway. BackendSpec compatible with Backend GEP draft. |
| **Iteration 3** | Month 5–6 | Payload processing + multi-gateway | `PayloadProcessingPipeline` GEP integration. Kong gateway support. Envoy gateway support. MCP protocol in proxy sidecar. Multi-cluster federation. | `processors[]` wired to real pipeline. MCP aligned with `BackendProtocol` enum. |
| **Iteration 4** | Month 7+ | Enterprise + ecosystem | AWS API Gateway. Browser snippet for client-side LLM calls. Official `Backend` CRD swap when Gateway API stable. SOC 2 Type II. OCI operator bundle. | Full `Backend` CRD adoption. Contribute to wg-ai-gateway conformance tests. |

---

*Velorai Pulse PRD v2.0 — April 2026*
