// Command collector receives traces from proxy sidecars and writes them to Postgres.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/velorai/pulse/internal/collector"
	"github.com/velorai/pulse/internal/pricing"
	"github.com/velorai/pulse/internal/store"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		log.Error("DATABASE_URL is required")
		os.Exit(1)
	}

	addr := os.Getenv("COLLECTOR_ADDR")
	if addr == "" {
		addr = ":9090"
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	s, err := store.New(ctx, dsn)
	if err != nil {
		log.Error("failed to connect to postgres", "error", err)
		os.Exit(1)
	}
	defer s.Close()

	pt := pricing.New()
	if path := os.Getenv("PULSE_PRICING_PATH"); path != "" {
		if err := pt.Load(path); err != nil {
			log.Warn("could not load pricing file", "error", err)
		}
	}

	srv := collector.New(s, pt, log)
	if err := srv.ListenAndServe(ctx, addr); err != nil {
		log.Error("collector exited with error", "error", err)
		os.Exit(1)
	}
}
