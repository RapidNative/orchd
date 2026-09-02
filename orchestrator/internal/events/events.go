// Package events is a small pluggable event/audit pipeline. Control-plane actions
// (project created, backup done, restore, …) are emitted to a Sink. Sinks are
// adaptors: MemorySink powers the admin activity feed, WebhookSink forwards to an
// external URL, and MultiSink fans out to several at once. New sinks (a log file,
// a message queue, an analytics endpoint) drop in behind the same interface.
package events

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

type Event struct {
	ID         string    `json:"id"`
	Time       time.Time `json:"time"`
	Type       string    `json:"type"` // e.g. "project.created", "backup.restored"
	ProjectID  string    `json:"project_id,omitempty"`
	WorkloadID string    `json:"workload_id,omitempty"`
	Message    string    `json:"message,omitempty"`
}

// Sink receives events. Implementations must be safe for concurrent use and must
// not block the caller for long (webhook delivery is async).
type Sink interface {
	Emit(Event)
}

// MemorySink keeps the most recent events in a ring buffer for the activity feed.
type MemorySink struct {
	mu  sync.Mutex
	buf []Event
	max int
}

func NewMemorySink(max int) *MemorySink {
	if max <= 0 {
		max = 500
	}
	return &MemorySink{max: max}
}

func (m *MemorySink) Emit(e Event) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.buf = append(m.buf, e)
	if len(m.buf) > m.max {
		m.buf = m.buf[len(m.buf)-m.max:]
	}
}

// Recent returns up to n events, newest first.
func (m *MemorySink) Recent(n int) []Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	if n <= 0 || n > len(m.buf) {
		n = len(m.buf)
	}
	out := make([]Event, n)
	for i := 0; i < n; i++ {
		out[i] = m.buf[len(m.buf)-1-i]
	}
	return out
}

// WebhookSink POSTs each event as JSON to a URL. Delivery is best-effort and async
// so it never blocks a control-plane action.
type WebhookSink struct {
	url    string
	apiKey string
	client *http.Client
}

func NewWebhookSink(url, apiKey string) *WebhookSink {
	return &WebhookSink{url: url, apiKey: apiKey, client: &http.Client{Timeout: 10 * time.Second}}
}

func (w *WebhookSink) Emit(e Event) {
	go func() {
		body, _ := json.Marshal(e)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.url, bytes.NewReader(body))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		if w.apiKey != "" {
			req.Header.Set("X-Webhook-Key", w.apiKey)
		}
		resp, err := w.client.Do(req)
		if err == nil {
			resp.Body.Close()
		}
	}()
}

// MultiSink fans an event out to several sinks.
type MultiSink struct{ sinks []Sink }

func NewMultiSink(sinks ...Sink) *MultiSink { return &MultiSink{sinks: sinks} }

func (m *MultiSink) Emit(e Event) {
	for _, s := range m.sinks {
		if s != nil {
			s.Emit(e)
		}
	}
}

// NewID returns a short random event id.
func NewID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
