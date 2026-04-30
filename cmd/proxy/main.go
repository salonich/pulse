// Command proxy is the LLM intercepting sidecar injected into customer pods by the operator.
// It listens on :8888, forwards LLM requests to upstream providers, captures traces,
// and exposes Prometheus metrics on :9090.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/velorai/pulse/internal/proxy"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfg := proxy.ConfigFromEnv()
	srv := proxy.New(cfg, log)

	proxySrv := &http.Server{
		Addr:         cfg.ProxyAddr,
		Handler:      srv.ProxyHandler(),
		ReadTimeout:  120 * time.Second,
		WriteTimeout: 120 * time.Second,
	}
	metricsSrv := &http.Server{
		Addr:    cfg.MetricsAddr,
		Handler: srv.MetricsHandler(),
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	go func() {
		log.Info("proxy listening", "addr", cfg.ProxyAddr)
		if err := proxySrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("proxy server error", "error", err)
			os.Exit(1)
		}
	}()
	go func() {
		log.Info("metrics listening", "addr", cfg.MetricsAddr)
		if err := metricsSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("metrics server error", "error", err)
		}
	}()

	<-ctx.Done()
	log.Info("shutting down")

	shutCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = proxySrv.Shutdown(shutCtx)
	_ = metricsSrv.Shutdown(shutCtx)
	srv.Shutdown(shutCtx)
}
