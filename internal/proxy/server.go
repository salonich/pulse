package proxy

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/velorai/pulse/internal/trace"
)

// lastDropped tracks the most recently observed sender Dropped() value so the
// /metrics handler can compute the delta and Add() it to the counter.
var lastDropped atomic.Uint64

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

	tracesDropped = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "pulse_traces_dropped_total",
		Help: "Traces dropped because the async sender buffer was full.",
	})
)

func init() {
	prometheus.MustRegister(requestsTotal, latencyHistogram, tracesDropped)
}

// Config holds proxy startup configuration from environment variables.
type Config struct {
	ProxyAddr           string
	MetricsAddr         string
	CollectorURL        string
	LLMBackendNamespace string
	LLMBackendName      string
	SampleRate          float64
	UpstreamOverride    string
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
		CollectorURL:        envOr("PULSE_COLLECTOR_URL", "http://pulse-collector.pulse-system:9091"),
		LLMBackendNamespace: os.Getenv("PULSE_NAMESPACE"),
		LLMBackendName:      os.Getenv("PULSE_LLMBACKEND_NAME"),
		SampleRate:          sampleRate,
		UpstreamOverride:    os.Getenv("PULSE_UPSTREAM_OVERRIDE"),
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// Server wires together the proxy router, metrics server, and async trace sender.
type Server struct {
	cfg    Config
	log    *slog.Logger
	sender *trace.Sender
}

// New creates a Server and starts the trace sender's worker pool.
// Call Shutdown to drain.
func New(cfg Config, log *slog.Logger) *Server {
	sender := trace.NewSender(trace.SenderOptions{
		CollectorURL: cfg.CollectorURL,
	}, log)
	return &Server{cfg: cfg, log: log, sender: sender}
}

// ProxyHandler returns the HTTP handler for the proxy listen address (:8888).
func (s *Server) ProxyHandler() http.Handler {
	fwd := newForwarder(s.sender, s.cfg.LLMBackendNamespace, s.cfg.LLMBackendName, s.cfg.UpstreamOverride, s.log)

	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(s.metricsMiddleware)
	r.Handle("/anthropic/*", fwd)
	r.Handle("/openai/*", fwd)

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	return r
}

// MetricsHandler returns the HTTP handler for the metrics listen address (:9090).
// It also reflects the sender's drop counter into Prometheus.
func (s *Server) MetricsHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		// Reconcile the sender's atomic counter into the Prom counter on every scrape.
		// Cheap and avoids a custom collector.
		current := s.sender.Dropped()
		// prometheus.Counter has no Set; we only ever add the delta.
		// Track last-seen via a closure-local variable.
		delta := current - lastDropped.Load()
		if delta > 0 {
			tracesDropped.Add(float64(delta))
			lastDropped.Store(current)
		}
		promhttp.Handler().ServeHTTP(w, r)
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	return mux
}

// Shutdown drains the trace sender with the given deadline.
func (s *Server) Shutdown(ctx context.Context) {
	deadline := 5 * time.Second
	if dl, ok := ctx.Deadline(); ok {
		if d := time.Until(dl); d < deadline {
			deadline = d
		}
	}
	s.sender.Close(deadline)
}

// metricsMiddleware updates Prometheus counters/histograms after each proxied request.
func (s *Server) metricsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		_, prov, _ := upstreamURL(r.URL.Path, "")

		timer := prometheus.NewTimer(prometheus.ObserverFunc(func(v float64) {
			latencyHistogram.WithLabelValues(prov, "").Observe(v * 1000)
		}))
		next.ServeHTTP(ww, r)
		timer.ObserveDuration()

		requestsTotal.WithLabelValues(prov, "", fmt.Sprintf("%d", ww.Status())).Inc()
	})
}
