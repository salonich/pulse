package proxy

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/velorai/pulse/internal/pricing"
)

// Metrics exposed on :9090/metrics, scraped by the ServiceMonitor.
var (
	requestsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "pulse_llm_requests_total",
		Help: "Total number of LLM requests proxied.",
	}, []string{"provider", "model", "status"})

	latencyHistogram = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "pulse_llm_latency_ms",
		Help:    "LLM request latency in milliseconds.",
		Buckets: []float64{50, 100, 200, 500, 1000, 2000, 5000, 10000},
	}, []string{"provider", "model"})
)

func init() {
	prometheus.MustRegister(requestsTotal, latencyHistogram)
}

// Config holds proxy startup configuration from environment variables.
type Config struct {
	// ProxyAddr is the listen address for the LLM proxy (default :8888).
	ProxyAddr string
	// MetricsAddr is the listen address for Prometheus metrics (default :9090).
	MetricsAddr string
	// CollectorURL is the trace collector endpoint.
	CollectorURL string
	// PricingPath is the path to pricing.json ConfigMap mount.
	PricingPath string
	// LLMBackendNamespace is the namespace of the owning LLMBackend.
	LLMBackendNamespace string
	// LLMBackendName is the name of the owning LLMBackend.
	LLMBackendName string
	// SampleRate is the fraction of traces to capture (0.0–1.0).
	SampleRate float64
}

// ConfigFromEnv builds Config from environment variables set by the webhook injection patch.
func ConfigFromEnv() Config {
	sampleRate := 1.0
	if s := os.Getenv("PULSE_SAMPLE_RATE"); s != "" {
		if v, err := strconv.ParseFloat(s, 64); err == nil {
			sampleRate = v
		}
	}
	return Config{
		ProxyAddr:           envOr("PULSE_PROXY_ADDR", ":8888"),
		MetricsAddr:         envOr("PULSE_METRICS_ADDR", ":9090"),
		CollectorURL:        envOr("PULSE_COLLECTOR_URL", "http://pulse-collector.pulse-system:9090"),
		PricingPath:         envOr("PULSE_PRICING_PATH", "/etc/pulse/pricing.json"),
		LLMBackendNamespace: os.Getenv("PULSE_NAMESPACE"),
		LLMBackendName:      os.Getenv("PULSE_LLMBACKEND_NAME"),
		SampleRate:          sampleRate,
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// Server wires together the proxy router and the metrics server.
type Server struct {
	cfg        Config
	pricingTable *pricing.Table
	log        *slog.Logger
}

// New creates a Server and loads pricing from disk (falls back to defaults on error).
func New(cfg Config, log *slog.Logger) *Server {
	pt := pricing.New()
	if cfg.PricingPath != "" {
		if err := pt.Load(cfg.PricingPath); err != nil {
			log.Warn("could not load pricing file, using defaults", "error", err, "path", cfg.PricingPath)
		}
	}
	return &Server{cfg: cfg, pricingTable: pt, log: log}
}

// ProxyHandler returns the HTTP handler for the proxy listen address (:8888).
func (s *Server) ProxyHandler() http.Handler {
	tracer := newTracer(s.cfg.CollectorURL, s.cfg.LLMBackendNamespace, s.cfg.LLMBackendName, s.log)
	fwd := newForwarder(s.pricingTable, tracer, s.log)

	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(s.metricsMiddleware)
	r.Handle("/anthropic/*", fwd)
	r.Handle("/openai/*", fwd) // forwarding wired; extraction in Iteration 1

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	return r
}

// MetricsHandler returns the HTTP handler for the metrics listen address (:9090).
func (s *Server) MetricsHandler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	return mux
}

// metricsMiddleware updates Prometheus counters/histograms after each proxied request.
func (s *Server) metricsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		_, provider, _ := upstreamURL(r.URL.Path)

		// Use a timer to capture latency via the histogram.
		timer := prometheus.NewTimer(prometheus.ObserverFunc(func(v float64) {
			latencyHistogram.WithLabelValues(provider, "").Observe(v * 1000)
		}))
		next.ServeHTTP(ww, r)
		timer.ObserveDuration()

		requestsTotal.WithLabelValues(provider, "", fmt.Sprintf("%d", ww.Status())).Inc()
	})
}
