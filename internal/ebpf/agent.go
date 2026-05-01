package ebpf

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/link"
	"github.com/cilium/ebpf/ringbuf"
	"github.com/cilium/ebpf/rlimit"

	"github.com/velorai/pulse/internal/provider"
	"github.com/velorai/pulse/internal/trace"
)

// Config controls the eBPF capture agent.
type Config struct {
	// CollectorURL is where extracted traces are POSTed via the shared sender.
	CollectorURL string
	// TargetPorts are the TCP destination ports to monitor (host byte order).
	TargetPorts []uint16
	// LLMBackendNamespace / Name tag every trace — in a real deployment these
	// are resolved per-connection from pod metadata; for MVP they come from env.
	LLMBackendNamespace string
	LLMBackendName      string
}

// Agent loads the BPF programs, attaches them, and drains the ring buffer.
type Agent struct {
	cfg   Config
	log   *slog.Logger
	objs  captureObjects
	links []link.Link
	rb    *ringbuf.Reader

	mu       sync.Mutex
	inflight map[connID]*exchange

	sender *trace.Sender
}

type connID struct {
	PID uint32
	FD  uint32
}

type exchange struct {
	startNs    uint64
	provider   string
	host       string
	reqSeen    bool
	respSeen   bool
	respBuf    bytes.Buffer
	respStatus int
	dstIP      uint32
	dstPort    uint16
}

// New constructs an Agent. Call Start to load BPF and begin capture.
func New(cfg Config, log *slog.Logger) *Agent {
	return &Agent{
		cfg:      cfg,
		log:      log,
		inflight: make(map[connID]*exchange),
		sender: trace.NewSender(trace.SenderOptions{
			CollectorURL: cfg.CollectorURL,
		}, log),
	}
}

// Start loads BPF objects, attaches tracepoints, and begins reading events.
// Blocks until ctx is cancelled.
func (a *Agent) Start(ctx context.Context) error {
	if err := rlimit.RemoveMemlock(); err != nil {
		return fmt.Errorf("remove memlock: %w", err)
	}

	if err := loadCaptureObjects(&a.objs, nil); err != nil {
		return fmt.Errorf("load BPF objects: %w", err)
	}
	defer func() { _ = a.objs.Close() }()

	for _, p := range a.cfg.TargetPorts {
		one := uint8(1)
		if err := a.objs.TargetPorts.Put(p, one); err != nil {
			return fmt.Errorf("set target port %d: %w", p, err)
		}
	}

	attachments := []struct {
		name string
		prog *ebpf.Program
	}{
		{"sys_enter_connect", a.objs.TraceConnectEnter},
		{"sys_exit_connect", a.objs.TraceConnectExit},
		{"sys_enter_write", a.objs.TraceWriteEnter},
		{"sys_enter_read", a.objs.TraceReadEnter},
		{"sys_exit_read", a.objs.TraceReadExit},
		{"sys_enter_close", a.objs.TraceCloseEnter},
	}
	for _, at := range attachments {
		l, err := link.Tracepoint("syscalls", at.name, at.prog, nil)
		if err != nil {
			a.closeLinks()
			return fmt.Errorf("attach tracepoint syscalls/%s: %w", at.name, err)
		}
		a.links = append(a.links, l)
	}
	defer a.closeLinks()

	rb, err := ringbuf.NewReader(a.objs.Events)
	if err != nil {
		return fmt.Errorf("open ringbuf: %w", err)
	}
	a.rb = rb
	defer func() { _ = rb.Close() }()

	a.log.Info("ebpf agent started",
		"ports", a.cfg.TargetPorts,
		"collector", a.cfg.CollectorURL,
	)

	go func() {
		<-ctx.Done()
		_ = rb.Close()
	}()

	// Periodic stats log so operators can see drop rate without scraping metrics.
	statsCtx, cancelStats := context.WithCancel(ctx)
	defer cancelStats()
	go a.logStats(statsCtx)

	for {
		rec, err := rb.Read()
		if err != nil {
			if ctx.Err() != nil {
				a.sender.Close(5 * time.Second)
				return nil
			}
			a.log.Warn("ringbuf read error", "error", err)
			continue
		}
		a.handleRecord(rec.RawSample)
	}
}

func (a *Agent) closeLinks() {
	for _, l := range a.links {
		_ = l.Close()
	}
	a.links = nil
}

// logStats periodically reports sender counters. The eBPF agent has no
// Prometheus endpoint today, so this is the operator's signal of drop rate.
func (a *Agent) logStats(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.log.Info("trace sender stats",
				"sent", a.sender.Sent(),
				"failed", a.sender.Failed(),
				"dropped", a.sender.Dropped(),
			)
		}
	}
}

// handleRecord decodes a ring buffer record and updates per-connection state.
// Submission is non-blocking via the shared sender so a slow collector cannot
// stall the ringbuf reader.
func (a *Agent) handleRecord(raw []byte) {
	if len(raw) < int(binary.Size(DataEvent{})) {
		return
	}
	var evt DataEvent
	if err := binary.Read(bytes.NewReader(raw), binary.LittleEndian, &evt); err != nil {
		return
	}

	id := connID{PID: evt.PID, FD: evt.FD}
	data := evt.Data[:evt.DataLen]

	a.mu.Lock()
	defer a.mu.Unlock()

	ex, ok := a.inflight[id]
	if !ok {
		ex = &exchange{
			startNs: evt.Timestamp,
			dstIP:   evt.DstIP,
			dstPort: evt.DstPort,
		}
		a.inflight[id] = ex
	}

	if evt.Direction == DirEgress && !ex.reqSeen {
		ex.startNs = evt.Timestamp
		parsed := parseHTTP(data)
		if parsed.Kind == Request {
			ex.reqSeen = true
			ex.host = parsed.Host
			ex.provider = provider.FromHost(parsed.Host)
		}
		return
	}

	if evt.Direction == DirIngress {
		if !ex.respSeen {
			parsed := parseHTTP(data)
			if parsed.Kind == Response {
				ex.respSeen = true
				ex.respStatus = parsed.StatusCode
				ex.respBuf.Write(parsed.Body)
			}
		} else {
			ex.respBuf.Write(data)
		}

		if ex.respSeen && a.exchangeLooksDone(ex) {
			a.emitTrace(id, evt.Timestamp, ex)
			delete(a.inflight, id)
		}
	}
}

func (a *Agent) exchangeLooksDone(ex *exchange) bool {
	b := ex.respBuf.Bytes()
	if len(b) == 0 {
		return false
	}
	if bytes.Contains(b, []byte("\"usage\"")) && bytes.LastIndexByte(b, '}') > 0 {
		return true
	}
	return len(b) >= 8192
}

// emitTrace builds a canonical trace and submits it to the shared sender.
func (a *Agent) emitTrace(id connID, endNs uint64, ex *exchange) {
	if ex.provider == "" && ex.host == "" {
		return
	}

	model, promptTokens, completionTokens := provider.ExtractUsage(ex.respBuf.Bytes())

	t := trace.Trace{
		LLMBackendNamespace: a.cfg.LLMBackendNamespace,
		LLMBackendName:      a.cfg.LLMBackendName,
		Provider:            ex.provider,
		Model:               model,
		PromptTokens:        promptTokens,
		CompletionTokens:    completionTokens,
		LatencyMS:           int((endNs - ex.startNs) / 1_000_000),
		Status:              ex.respStatus,
		CreatedAt:           time.Now().UTC(),
	}

	if !a.sender.Submit(t) {
		// Buffer was full — already counted by sender.Dropped(). Don't log
		// per-trace at this rate; logStats will surface it.
		return
	}

	a.log.Debug("trace captured",
		"pid", id.PID,
		"host", ex.host,
		"provider", ex.provider,
		"model", model,
		"latency_ms", t.LatencyMS,
		"status", ex.respStatus,
	)
}

