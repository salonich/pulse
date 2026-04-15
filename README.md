# Velorai Pulse

LLM Observability Operator for Kubernetes. Zero application code changes — the operator injects a proxy sidecar, auto-creates Prometheus `ServiceMonitor` and Grafana dashboards, and tracks cost and latency per service.

## How it works

1. Install the operator once per cluster
2. Apply a single `LLMBackend` YAML describing which service calls LLMs
3. The operator labels the namespace, injects `pulse-proxy` into matching pods, and provisions observability resources automatically

```
                                   ┌─► api.anthropic.com  (/anthropic/*)
App pod ──► pulse-proxy:8888 ──────┤
                 │                 └─► api.openai.com      (/openai/*)
                 │
                 └──► (async) collector:9090 ──► Postgres ──► Grafana
```

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
internal/
  controller/   # LLMBackend reconciler
  webhook/      # Mutating webhook — pod sidecar injection
  proxy/        # Forwarding, trace extraction, cost calculation
  collector/    # HTTP server, trace validation
  observability/# ServiceMonitor + GrafanaDashboard builders
  pricing/      # Per-token cost table (loaded from ConfigMap)
  store/        # Postgres data access layer (pgx v5)
api/v1alpha1/   # CRD Go types (controller-gen)
config/
  crd/bases/    # Generated CRD YAML
  rbac/         # ClusterRole + ClusterRoleBinding
  webhook/      # MutatingWebhookConfiguration
deploy/
  helm/pulse/   # Helm chart (primary install method)
  migrations/   # Postgres schema (golang-migrate)
docs/examples/  # Example LLMBackend YAML
```

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

## Architecture

| Component | Binary | Purpose |
|---|---|---|
| Operator | `cmd/operator` | Watches `LLMBackend` CRDs; reconciles cluster state; runs mutating webhook |
| Proxy Sidecar | `cmd/proxy` | Intercepted at `localhost:8888`; forwards to upstream LLM API; captures traces |
| Collector | `cmd/collector` | Receives traces over HTTP; writes to Postgres |

## Iteration roadmap

| Iteration | Status | Theme |
|---|---|---|
| **MVP (Weekend 1)** | ✅ | Operator, sidecar (non-streaming, Anthropic), collector, ServiceMonitor, GrafanaDashboard |
| **Iteration 1** | Planned | Streaming proxy, OpenAI, HTTPRoute + RateLimitPolicy, Dashboard UI, mTLS |
| **Iteration 2** | Planned | wg-ai-gateway alignment, eBPF capture, BackendSpec type |
| **Iteration 3** | Planned | PayloadProcessingPipeline, Kong/Envoy gateways, MCP protocol |

## Standards alignment

CRD field naming is explicitly forward-compatible with the [Kubernetes wg-ai-gateway](https://github.com/kubernetes-sigs/wg-ai-gateway) Backend GEP. `providers[].backendRef` will reference the official `Backend` CRD when it reaches stable — zero customer-facing changes required.

## License

Apache 2.0 — see [LICENSE](LICENSE).
