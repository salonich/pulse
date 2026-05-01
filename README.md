# Velorai Pulse

LLM Observability Operator for Kubernetes. Zero application code changes — the operator captures every LLM call (via sidecar **or** eBPF), auto-creates Prometheus `ServiceMonitor` and Grafana dashboards, and tracks cost and latency per service.

## How it works

1. Install the operator once per cluster
2. (Optional) Apply a `PulseConfig` selecting `captureMethod: sidecar` (default) or `ebpf`
3. Apply a single `LLMBackend` YAML describing which service calls LLMs
4. The operator provisions observability resources and either injects `pulse-proxy` into matching pods (sidecar mode) or runs an eBPF DaemonSet across all nodes (ebpf mode)

### Sidecar mode (default)

```
                                   ┌─► api.anthropic.com  (/anthropic/*)
App pod ──► pulse-proxy:8888 ──────┤
                 │                 └─► api.openai.com      (/openai/*)
                 │
                 └──► (async) collector:9091 ──► Postgres ──► Grafana
```

### eBPF mode

```
App pod ──────────────────────────► api.anthropic.com / api.openai.com
   │     (no sidecar, no env rewrites)
   │
Node kernel: tracepoint on connect/read/write
   │
pulse-ebpf-agent (DaemonSet) ──► (async) collector:9091 ──► Postgres ──► Grafana
```

The eBPF agent observes traffic passively at the kernel level — no code changes, no env vars, no sidecar memory overhead. Trade-off: HTTPS payload capture requires uprobes on TLS libraries (planned); the current MVP works against plaintext upstreams (e.g. an in-cluster gateway terminating TLS).

## Quick start

```bash
# Install operator (CRDs + RBAC + Deployment)
kubectl apply -f https://raw.githubusercontent.com/velorai/pulse/main/deploy/helm/pulse/

# Or via Helm
helm install pulse deploy/helm/pulse -n pulse-system --create-namespace

# Apply your first LLMBackend
kubectl apply -f docs/examples/llmbackend.yaml
```

Within ~60 seconds the operator will:
- Label `my-app` namespace for sidecar injection
- Inject `pulse-proxy` into pods backing `checkout-api`
- Create a `ServiceMonitor` so Prometheus scrapes metrics
- Create a `GrafanaDashboard` with cost/latency panels

## Repository layout

```
cmd/
  operator/     # K8s operator (controller-runtime reconciliation loop + webhook)
  proxy/        # Proxy sidecar (intercepts LLM calls at localhost:8888)
  collector/    # Trace collector (receives traces, writes to Postgres)
  ebpf-agent/   # Node-local eBPF capture DaemonSet (captureMethod: ebpf)
  mockllm/      # Local Anthropic-compatible mock for end-to-end testing
internal/
  controller/   # LLMBackend + PulseConfig reconcilers, OwnedResource driver
  webhook/      # Mutating webhook — sidecar injection (gated by capture mode)
  proxy/        # Forwarding + Prometheus metrics
  ebpf/         # BPF program loader, ringbuf reader, HTTP framing parser
  collector/    # HTTP server, trace validation, cost enrichment
  trace/        # Canonical Trace type + bounded async Sender (shared by proxy and eBPF)
  provider/     # Provider classification (anthropic/openai) + usage extraction
  observability/# ServiceMonitor + GrafanaDashboard builders
  pricing/      # Per-token cost table (loaded from ConfigMap, applied server-side)
  store/        # Postgres data access layer (pgx v5, RLS enforced via SET LOCAL)
api/v1alpha1/   # CRD Go types (controller-gen)
config/
  crd/bases/    # Generated CRD YAML (LLMBackend + PulseConfig)
  rbac/         # ClusterRole + ClusterRoleBinding
  webhook/      # MutatingWebhookConfiguration
deploy/
  helm/pulse/   # Helm chart (primary install method)
  migrations/   # Postgres schema (golang-migrate); creates pulse_runtime non-owner role
docs/examples/  # Example LLMBackend YAML
docker-compose.yaml # Local dev rig: postgres + collector + proxy + mockllm + ebpf-agent
```

## Capture modes

The cluster's capture mode is selected via a single cluster-scoped `PulseConfig` resource. The operator caches the active mode and the webhook + LLMBackend reconciler both read from that cache.

```yaml
apiVersion: pulse.velorai.com/v1alpha1
kind: PulseConfig
metadata:
  name: cluster-config
  namespace: pulse-system
spec:
  captureMethod: ebpf            # sidecar (default) | ebpf
  ebpfAgentImage: ghcr.io/velorai/pulse-ebpf-agent:latest
  collectorEndpoint: http://pulse-collector.pulse-system:9091
```

| Mode | Webhook behaviour | What runs on the node | Trade-off |
|---|---|---|---|
| `sidecar` (default) | Injects `pulse-proxy` into pods backing each `LLMBackend.targetService` and rewrites `ANTHROPIC_BASE_URL` / `OPENAI_BASE_URL` | Per-pod proxy container | Per-pod memory + a TLS-terminating proxy you control |
| `ebpf` | **Skips injection entirely** — applications call providers directly | One DaemonSet pod per node observing kernel syscalls | Zero per-pod overhead; HTTPS bodies require uprobes (MVP captures plaintext only) |

Switching modes (e.g. `sidecar` → `ebpf`) is a single edit of the `PulseConfig`. The operator deletes the eBPF DaemonSet on the way back to `sidecar`, and the webhook stops injecting on the way to `ebpf`. There is no double-capture window.

The reconciler reports per-resource conditions on every `LLMBackend`:

```
$ kubectl describe llmbackend checkout-anthropic
Status:
  Conditions:
    Type                       Status  Reason
    PricingConfigMapReady      True    Reconciled
    ServiceMonitorReady        True    Reconciled
    GrafanaDashboardReady      True    Reconciled
    SidecarsInjected           True    Injected      (3 of 3 target pods have pulse-proxy)
    Ready                      True    AllResourcesReady
  Sidecar Injected Pods:       3
  Observed Generation:         2
```

`Ready=True` requires every owned resource to be reconciled **and** (in sidecar mode) at least one target pod to actually carry the proxy container — no more lying conditions.

### eBPF limitations (MVP)

The eBPF path works end-to-end against plaintext upstreams today, but pick `ebpf` mode with these gaps in mind:

- **HTTPS bodies are opaque.** The agent attaches to the `read`/`write` syscall tracepoints and only sees plaintext. Direct calls to `api.anthropic.com:443` will yield zero usage data. The supported deployment is an in-cluster gateway that terminates TLS *inside* the pod (or against the local `mockllm`). uprobes on OpenSSL `SSL_read`/`SSL_write` and the Go TLS runtime are on the next-iteration list.
- **Attribution is node-scoped, not pod-scoped.** Every trace emitted from a node carries the single `PULSE_LLMBACKEND_NAME` set on the DaemonSet. Real per-pod attribution requires PID → cgroup → pod → `LLMBackend` resolution; planned, not built.
- **Streaming responses aren't parsed.** The "exchange complete" heuristic looks for the `"usage"` JSON key or an 8 KiB cap. SSE / chunked responses will mis-bucket until chunk-by-chunk parsing lands.
- **Kernel requirements.** Needs Linux ≥ 5.8 with BTF (`/sys/kernel/btf/vmlinux` present) and the `BPF`, `SYS_ADMIN`, `SYS_RESOURCE`, `NET_ADMIN` capabilities. macOS Docker Desktop is not supported; some managed-K8s node images strip BTF and will refuse to load the program.
- **Validated in unit tests, not on a real kernel.** The HTTP parser and provider extraction have full unit coverage; the BPF load + tracepoint attach path has not been exercised in CI.

If any of those is a blocker for your environment, stay on `captureMethod: sidecar` until the corresponding row moves into "✅" on the roadmap.

## LLMBackend CRD

```yaml
apiVersion: pulse.velorai.com/v1alpha1
kind: LLMBackend
metadata:
  name: checkout-anthropic
  namespace: my-app
spec:
  # The service that *calls* LLMs — not the LLM itself.
  # The operator injects pulse-proxy into its pods.
  targetService:
    name: checkout-api
    namespace: my-app
    port: 8080
  providers:
    - name: anthropic
      backendRef:
        name: anthropic-backend
        namespace: pulse-system
      credentialRef:
        name: anthropic-api-key   # K8s Secret with key `api-key`
  observability:
    prometheus: true   # auto-create ServiceMonitor
    grafana: true      # auto-create GrafanaDashboard
```

## Development

```bash
# Install tools + generate CRD YAML and deepcopy
make dev

# Build all binaries
make build

# Run tests
make test

# Apply CRDs to current cluster
make install
```

Requires Go 1.22+, `controller-gen` (installed by `make dev`), and a running Kubernetes cluster for integration tests.

### Local end-to-end with docker-compose

The `docker-compose.yaml` rig wires Postgres + collector + proxy + a mock Anthropic-compatible LLM into a self-contained loop — useful for verifying changes without a cluster.

```bash
# Sidecar / proxy path (default profile)
docker compose up --build
curl -X POST localhost:8888/anthropic/v1/messages -d '{"model":"claude-sonnet-4-20250514"}'
curl 'localhost:9091/v1/traces?namespace=default'

# eBPF path (Linux host only — requires BTF + privileged + host PID/network)
docker compose --profile ebpf up --build
# Hit mockllm directly; the agent observes the syscalls
curl -X POST localhost:8080/v1/messages -d '{"model":"claude-sonnet-4-20250514"}'
curl 'localhost:9091/v1/traces?namespace=default'
```

The collector connects to Postgres as the non-owner `pulse_runtime` role, so RLS is genuinely enforced: `psql` as the bootstrap user sees every namespace; the collector sees only what `app.namespace` is bound to.

## Architecture

| Component | Binary | Purpose |
|---|---|---|
| Operator | `cmd/operator` | Watches `LLMBackend` and `PulseConfig`; reconciles cluster state via the `OwnedResource` driver; runs mutating webhook |
| Proxy Sidecar | `cmd/proxy` | Used in `sidecar` mode. Intercepted at `localhost:8888`; forwards to upstream LLM API; submits traces via the bounded async sender |
| eBPF Agent | `cmd/ebpf-agent` | Used in `ebpf` mode. Loads BPF programs on tracepoints, reassembles HTTP exchanges from the ring buffer, submits traces via the bounded async sender |
| Collector | `cmd/collector` | Receives traces over HTTP; enriches cost from the cluster pricing table (single source of truth); writes to Postgres |

### Cross-cutting concerns

- **Canonical trace shape**: every emitter (proxy, eBPF agent) marshals `internal/trace.Trace` — there is no per-package duplicate.
- **Bounded async submission**: `internal/trace.Sender` runs a fixed worker pool drained from a buffered channel. A hung collector cannot back up the application's request path; full-buffer events increment `pulse_traces_dropped_total`.
- **Cost is server-side only**: proxies emit token counts; the collector applies pricing. Updating `pricing.json` no longer requires rolling sidecars.
- **Postgres RLS is enforced**: the collector connects as the non-owner `pulse_runtime` role, every read/write is wrapped in a transaction that runs `SET LOCAL app.namespace = $ns`. Cross-namespace queries return zero rows.

## Iteration roadmap

| Iteration | Status | Theme |
|---|---|---|
| **MVP (Weekend 1)** | ✅ | Operator, sidecar (non-streaming, Anthropic), collector, ServiceMonitor, GrafanaDashboard |
| **eBPF iteration** | ✅ | `PulseConfig` capture mode, kernel-level capture DaemonSet, webhook capture-mode gate, canonical trace types, server-side cost, RLS enforcement, per-resource status conditions |
| **Next** | Planned | Streaming proxy (SSE), OpenAI extraction in eBPF, HTTPRoute + RateLimitPolicy, mTLS, per-pod attribution in eBPF (PID→pod), uprobes for HTTPS payloads |
| **Later** | Planned | Dashboard UI, PayloadProcessingPipeline, Kong/Envoy gateways, MCP protocol, wg-ai-gateway BackendSpec swap |

## Standards alignment

CRD field naming is explicitly forward-compatible with the [Kubernetes wg-ai-gateway](https://github.com/kubernetes-sigs/wg-ai-gateway) Backend GEP. `providers[].backendRef` will reference the official `Backend` CRD when it reaches stable — zero customer-facing changes required.

## License

Apache 2.0 — see [LICENSE](LICENSE).
