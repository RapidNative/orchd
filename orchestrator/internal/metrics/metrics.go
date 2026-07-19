// Package metrics publishes a periodic platform snapshot to a pluggable Sink.
// Sinks are adaptors: NopSink (off), LogSink (structured log line), HTTPSink
// (POST JSON to a collector). A statsd/prometheus sink drops in behind the same
// interface.
package metrics

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"
)

// Snapshot is a point-in-time view of the fleet, cheap to compute from the store.
type Snapshot struct {
	Time           time.Time `json:"time"`
	Projects       int       `json:"projects"`
	Workloads      int       `json:"workloads"`
	Running        int       `json:"running"`
	Suspended      int       `json:"suspended"`
	MemMBAllocated int       `json:"mem_mb_allocated"` // sum of memory caps of running workloads
}

// Sink receives snapshots. Implementations must not block the caller for long.
type Sink interface {
	Publish(Snapshot)
	Name() string
}

type NopSink struct{}

func (NopSink) Publish(Snapshot) {}
func (NopSink) Name() string     { return "nop" }

type LogSink struct{}

func (LogSink) Name() string { return "log" }
func (LogSink) Publish(s Snapshot) {
	log.Printf("metrics projects=%d workloads=%d running=%d suspended=%d mem_mb=%d",
		s.Projects, s.Workloads, s.Running, s.Suspended, s.MemMBAllocated)
}

// HTTPSink POSTs each snapshot as JSON to a collector URL (async, best-effort).
type HTTPSink struct {
	url    string
	client *http.Client
}

func NewHTTPSink(url string) *HTTPSink {
	return &HTTPSink{url: url, client: &http.Client{Timeout: 10 * time.Second}}
}

func (h *HTTPSink) Name() string { return "http" }
func (h *HTTPSink) Publish(s Snapshot) {
	go func() {
		body, _ := json.Marshal(s)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.url, bytes.NewReader(body))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := h.client.Do(req)
		if err == nil {
			resp.Body.Close()
		}
	}()
}
