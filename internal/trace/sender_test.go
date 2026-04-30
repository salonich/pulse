package trace

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestSender_HappyPath(t *testing.T) {
	var got atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.Add(1)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	s := NewSender(SenderOptions{CollectorURL: srv.URL}, discardLogger())
	for i := 0; i < 25; i++ {
		if !s.Submit(Trace{LLMBackendNamespace: "ns", LLMBackendName: "n", Provider: "anthropic"}) {
			t.Fatal("submit returned false on empty buffer")
		}
	}
	s.Close(2 * time.Second)

	if g := got.Load(); g != 25 {
		t.Errorf("collector received %d traces, want 25", g)
	}
	if s.Sent() != 25 {
		t.Errorf("Sent()=%d, want 25", s.Sent())
	}
	if s.Dropped() != 0 {
		t.Errorf("Dropped()=%d, want 0", s.Dropped())
	}
}

func TestSender_DropsOnFullBuffer(t *testing.T) {
	// Hang the server forever so workers stay busy and the buffer fills up.
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block
	}))
	defer srv.Close()
	defer close(block)

	s := NewSender(SenderOptions{
		CollectorURL: srv.URL,
		BufferSize:   2,
		Workers:      1,
	}, discardLogger())

	// Submit many more than buffer + workers can hold.
	const total = 50
	dropped := 0
	for i := 0; i < total; i++ {
		if !s.Submit(Trace{LLMBackendNamespace: "ns", LLMBackendName: "n", Provider: "anthropic"}) {
			dropped++
		}
	}

	if dropped == 0 {
		t.Errorf("expected drops with full buffer + blocked workers, got 0")
	}
	if s.Dropped() != uint64(dropped) {
		t.Errorf("Dropped()=%d, dropped count=%d", s.Dropped(), dropped)
	}
	// Don't call Close — the workers are blocked. The test framework will GC.
}

func TestSender_FailedOnNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	s := NewSender(SenderOptions{CollectorURL: srv.URL, Workers: 1}, discardLogger())
	for i := 0; i < 5; i++ {
		s.Submit(Trace{LLMBackendNamespace: "ns", LLMBackendName: "n", Provider: "anthropic"})
	}
	s.Close(2 * time.Second)

	if s.Failed() != 5 {
		t.Errorf("Failed()=%d, want 5", s.Failed())
	}
	if s.Sent() != 0 {
		t.Errorf("Sent()=%d, want 0", s.Sent())
	}
}
