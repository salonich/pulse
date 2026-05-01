package trace

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// SenderOptions configures a Sender. Zero values pick the documented defaults.
type SenderOptions struct {
	// CollectorURL is required: where to POST trace JSON.
	CollectorURL string
	// BufferSize is the channel capacity. Default 1024.
	BufferSize int
	// Workers is the number of POST goroutines. Default 4.
	Workers int
	// PostTimeout caps a single collector POST. Default 5s.
	PostTimeout time.Duration
}

// Sender is a bounded, async-batched emitter for traces.
//
// Submit is non-blocking; if the buffer is full, the trace is dropped and
// Dropped() is incremented. This trades completeness for stable memory and
// goroutine bounds — at production traffic, a hung collector cannot back up
// the application's request path.
type Sender struct {
	url     string
	timeout time.Duration

	ch     chan Trace
	wg     sync.WaitGroup
	client *http.Client
	log    *slog.Logger

	dropped atomic.Uint64
	sent    atomic.Uint64
	failed  atomic.Uint64
}

// NewSender constructs a Sender and starts its worker pool. Call Close to drain.
func NewSender(opts SenderOptions, log *slog.Logger) *Sender {
	if opts.BufferSize <= 0 {
		opts.BufferSize = 1024
	}
	if opts.Workers <= 0 {
		opts.Workers = 4
	}
	if opts.PostTimeout <= 0 {
		opts.PostTimeout = 5 * time.Second
	}

	s := &Sender{
		url:     opts.CollectorURL,
		timeout: opts.PostTimeout,
		ch:      make(chan Trace, opts.BufferSize),
		client:  &http.Client{Timeout: opts.PostTimeout},
		log:     log,
	}

	s.wg.Add(opts.Workers)
	for i := 0; i < opts.Workers; i++ {
		go s.worker()
	}
	return s
}

// Submit enqueues a trace. Returns false if the buffer was full and the trace
// was dropped. Never blocks.
func (s *Sender) Submit(t Trace) bool {
	select {
	case s.ch <- t:
		return true
	default:
		s.dropped.Add(1)
		return false
	}
}

// Close drains in-flight traces with the given deadline.
func (s *Sender) Close(deadline time.Duration) {
	close(s.ch)
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(deadline):
		s.log.Warn("sender close timeout, dropping in-flight traces",
			"queued", len(s.ch))
	}
}

// Dropped returns the cumulative number of traces dropped due to full buffer.
func (s *Sender) Dropped() uint64 { return s.dropped.Load() }

// Sent returns the cumulative number of traces successfully accepted by the
// collector (HTTP 2xx).
func (s *Sender) Sent() uint64 { return s.sent.Load() }

// Failed returns the cumulative number of POSTs that errored or returned non-2xx.
func (s *Sender) Failed() uint64 { return s.failed.Load() }

func (s *Sender) worker() {
	defer s.wg.Done()
	for t := range s.ch {
		s.post(t)
	}
}

func (s *Sender) post(t Trace) {
	body, err := json.Marshal(t)
	if err != nil {
		s.failed.Add(1)
		s.log.Warn("marshal trace", "error", err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		s.url+"/v1/traces", bytes.NewReader(body))
	if err != nil {
		s.failed.Add(1)
		s.log.Warn("build collector request", "error", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		s.failed.Add(1)
		s.log.Warn("post trace", "error", err)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 300 {
		s.failed.Add(1)
		s.log.Warn("collector non-2xx", "status", resp.StatusCode)
		return
	}
	s.sent.Add(1)
}
