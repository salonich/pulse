// Command ebpf-agent is the node-local eBPF capture DaemonSet for Pulse.
// It loads BPF programs that track TCP connections to configured ports,
// captures HTTP request/response data, and posts traces to the collector.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	pulseebpf "github.com/velorai/pulse/internal/ebpf"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg := pulseebpf.Config{
		CollectorURL:        envOr("PULSE_COLLECTOR_URL", "http://pulse-collector.pulse-system:9091"),
		TargetPorts:         parsePorts(envOr("PULSE_TARGET_PORTS", "443,8080")),
		LLMBackendNamespace: envOr("PULSE_NAMESPACE", "default"),
		LLMBackendName:      envOr("PULSE_LLMBACKEND_NAME", "ebpf-capture"),
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	agent := pulseebpf.New(cfg, log)
	if err := agent.Start(ctx); err != nil {
		log.Error("ebpf agent failed", "error", err)
		os.Exit(1)
	}
	log.Info("shutdown complete")
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func parsePorts(s string) []uint16 {
	var out []uint16
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		n, err := strconv.ParseUint(p, 10, 16)
		if err != nil {
			continue
		}
		out = append(out, uint16(n))
	}
	return out
}
