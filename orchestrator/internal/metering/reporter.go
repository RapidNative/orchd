package metering

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Aggregate is one workload's traffic in one minute, as the website's
// POST /api/internal/usage takes it (the "aggregates" array).
type Aggregate struct {
	ID        string       `json:"id"`
	Minute    time.Time    `json:"minute"`
	ProjectID *string      `json:"project_id"`
	Workload  string       `json:"workload"`
	Meters    Meters       `json:"meters"`
	Status    StatusCounts `json:"status"`
}

type Meters struct {
	Requests int64 `json:"traffic_requests"`
	Egress   int64 `json:"traffic_egress"`
	Ingress  int64 `json:"traffic_ingress"`
}

type StatusCounts struct {
	S2xx int64 `json:"2xx"`
	S3xx int64 `json:"3xx"`
	S4xx int64 `json:"4xx"`
	S5xx int64 `json:"5xx"`
}

// MaxBatch is the most aggregates one request carries (the contract's cap).
const MaxBatch = 500

// ReporterConfig configures a Reporter. An empty URL turns reporting off: the
// meter is still drained every interval (so it never grows), and the last
// minute stays visible through Status.
type ReporterConfig struct {
	// URL is the full ingest endpoint, e.g. https://www.rapidnative.com/api/internal/usage.
	URL   string
	Token string
	// Interval between drains; 0 means a minute. Drains run just after each
	// interval boundary so a minute is complete when it is sent.
	Interval time.Duration
	// Buffer is how old an unsent aggregate may get before it is dropped
	// (website down); 0 means an hour. MaxPending caps the count the same way.
	Buffer     time.Duration
	MaxPending int
	// Resolve maps an orchd project id to its RapidNative project id; false
	// reports the aggregate with project_id null. Called once per project per drain.
	Resolve func(orchdProjectID string) (string, bool)
	Client  *http.Client
}

// Reporter drains a Meter every interval and POSTs the aggregates to the
// website, keeping what fails in memory and retrying with backoff.
type Reporter struct {
	m   *Meter
	cfg ReporterConfig
	now func() time.Time

	mu       sync.Mutex
	outbox   []Aggregate
	backoff  time.Duration
	nextTry  time.Time
	sent     int64
	dropped  int64
	lastErr  string
	lastSent time.Time
	last     []Aggregate        // the newest minute drained, for Status
	resolved map[string]*string // orchd project id -> RapidNative id (nil: none), from the last drain

	done chan struct{}
}

const (
	minBackoff   = 5 * time.Second
	maxBackoff   = 5 * time.Minute
	drainSettle  = 2 * time.Second // after a boundary, so requests ending on it are in
	shutdownWait = 10 * time.Second
)

func NewReporter(m *Meter, cfg ReporterConfig) *Reporter {
	if cfg.Interval <= 0 {
		cfg.Interval = time.Minute
	}
	if cfg.Buffer <= 0 {
		cfg.Buffer = time.Hour
	}
	if cfg.MaxPending <= 0 {
		cfg.MaxPending = 100_000
	}
	if cfg.Client == nil {
		cfg.Client = &http.Client{Timeout: 15 * time.Second}
	}
	if cfg.Resolve == nil {
		cfg.Resolve = func(string) (string, bool) { return "", false }
	}
	return &Reporter{m: m, cfg: cfg, now: time.Now, resolved: map[string]*string{}, done: make(chan struct{})}
}

// Enabled reports whether aggregates are sent anywhere.
func (r *Reporter) Enabled() bool { return r.cfg.URL != "" }

// Done is closed when Run has returned, after its shutdown flush.
func (r *Reporter) Done() <-chan struct{} { return r.done }

// Run drains and reports until ctx is cancelled, then drains everything left
// (the current minute too) and makes one last attempt to send it.
func (r *Reporter) Run(ctx context.Context) {
	defer close(r.done)
	if r.Enabled() {
		log.Printf("usage: reporting gateway traffic to %s every %s", r.cfg.URL, r.cfg.Interval)
	}
	for {
		now := r.now()
		next := now.Truncate(r.cfg.Interval).Add(r.cfg.Interval + drainSettle)
		if next.Sub(now) > r.cfg.Interval {
			next = next.Add(-r.cfg.Interval)
		}
		r.mu.Lock()
		if len(r.outbox) > 0 && !r.nextTry.IsZero() && r.nextTry.Before(next) {
			next = r.nextTry
		}
		r.mu.Unlock()
		t := time.NewTimer(max(next.Sub(now), 0))
		select {
		case <-ctx.Done():
			t.Stop()
			r.collect(time.Time{})
			sctx, cancel := context.WithTimeout(context.Background(), shutdownWait)
			r.send(sctx, true)
			cancel()
			if n := r.Pending(); n > 0 {
				log.Printf("usage: shutting down with %d traffic aggregates unsent", n)
			}
			return
		case <-t.C:
			r.collect(r.m.now())
			r.send(ctx, false)
		}
	}
}

// collect drains the meter's buckets for minutes before `before` (the zero
// time drains all) into the outbox, resolving project ids, then drops what
// is past the buffer.
func (r *Reporter) collect(before time.Time) {
	buckets := r.m.Drain(before)
	r.mu.Lock()
	defer r.mu.Unlock()

	resolved := make(map[string]*string, len(r.resolved)+1)
	var newest time.Time
	aggs := make([]Aggregate, 0, len(buckets))
	for _, b := range buckets {
		pid, ok := resolved[b.ProjectID]
		if !ok {
			if pid, ok = r.resolved[b.ProjectID]; !ok {
				if id, found := r.cfg.Resolve(b.ProjectID); found {
					pid = &id
				}
			}
			resolved[b.ProjectID] = pid
		}
		aggs = append(aggs, Aggregate{
			ID:        newUUID(),
			Minute:    b.Minute,
			ProjectID: pid,
			Workload:  b.Workload,
			Meters:    Meters{Requests: b.Requests, Egress: b.Out, Ingress: b.In},
			Status:    StatusCounts{S2xx: b.Status[0], S3xx: b.Status[1], S4xx: b.Status[2], S5xx: b.Status[3]},
		})
		if b.Minute.After(newest) {
			newest = b.Minute
		}
	}
	// Resolutions live for the next drain too: a project deleted while its
	// last minute was open still reports under its id.
	if len(buckets) > 0 {
		r.resolved = resolved
	}
	if len(aggs) > 0 {
		r.last = r.last[:0]
		for _, a := range aggs {
			if a.Minute.Equal(newest) {
				r.last = append(r.last, a)
			}
		}
	}
	if !r.Enabled() {
		return
	}
	r.outbox = append(r.outbox, aggs...)
	r.pruneLocked()
}

// pruneLocked drops the oldest aggregates past the buffer age or count.
func (r *Reporter) pruneLocked() {
	cutoff := r.now().Add(-r.cfg.Buffer)
	drop := 0
	for drop < len(r.outbox) && (r.outbox[drop].Minute.Before(cutoff) || len(r.outbox)-drop > r.cfg.MaxPending) {
		drop++
	}
	if drop == 0 {
		return
	}
	log.Printf("usage: dropping %d unsent traffic aggregates (minutes %s to %s); the website has not taken them (last error: %s)",
		drop, r.outbox[0].Minute.Format(time.RFC3339), r.outbox[drop-1].Minute.Format(time.RFC3339), r.lastErr)
	r.dropped += int64(drop)
	r.outbox = append(r.outbox[:0:0], r.outbox[drop:]...)
}

// send posts the outbox in batches of MaxBatch, oldest first, until it is
// empty or a batch fails. A failure backs off (doubling, capped) unless force.
func (r *Reporter) send(ctx context.Context, force bool) {
	if !r.Enabled() {
		return
	}
	for {
		r.mu.Lock()
		if len(r.outbox) == 0 || (!force && r.now().Before(r.nextTry)) {
			r.mu.Unlock()
			return
		}
		batch := r.outbox[:min(len(r.outbox), MaxBatch)]
		r.mu.Unlock()

		status, err := r.post(ctx, batch)

		r.mu.Lock()
		switch {
		case err == nil || status == http.StatusBadRequest:
			// Sent, or refused as malformed: the website rejects the whole
			// request and a retry would only be refused again.
			if err != nil {
				log.Printf("usage: website refused %d traffic aggregates, dropping them: %v", len(batch), err)
				r.dropped += int64(len(batch))
			} else {
				r.sent += int64(len(batch))
				r.lastSent = r.now()
				r.lastErr = ""
			}
			r.outbox = r.outbox[len(batch):]
			if len(r.outbox) == 0 {
				r.outbox = nil
			}
			r.backoff, r.nextTry = 0, time.Time{}
			r.mu.Unlock()
		default:
			r.backoff = min(max(r.backoff*2, minBackoff), maxBackoff)
			r.nextTry = r.now().Add(r.backoff)
			if r.lastErr == "" {
				log.Printf("usage: reporting traffic failed, keeping %d aggregates and retrying with backoff: %v", len(r.outbox), err)
			}
			r.lastErr = err.Error()
			r.mu.Unlock()
			return
		}
	}
}

func (r *Reporter) post(ctx context.Context, batch []Aggregate) (int, error) {
	body, err := json.Marshal(struct {
		Source     string      `json:"source"`
		Aggregates []Aggregate `json:"aggregates"`
	}{"orchd", batch})
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.cfg.URL, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+r.cfg.Token)
	resp, err := r.cfg.Client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	if resp.StatusCode/100 != 2 {
		return resp.StatusCode, fmt.Errorf("POST %s: %d %s", r.cfg.URL, resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	return resp.StatusCode, nil
}

// Pending is how many aggregates are waiting to be sent.
func (r *Reporter) Pending() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.outbox)
}

// Status is the reporter's state for the control API.
type Status struct {
	Enabled     bool        `json:"enabled"`
	Pending     int         `json:"pending"`
	Sent        int64       `json:"sent"`
	Dropped     int64       `json:"dropped"`
	LastError   string      `json:"last_error,omitempty"`
	LastSentAt  *time.Time  `json:"last_sent_at,omitempty"`
	OpenBuckets int         `json:"open_buckets"`
	LiveConns   int         `json:"live_conns"`
	LastMinute  []Aggregate `json:"last_minute"`
}

func (r *Reporter) Status() Status {
	buckets, conns := r.m.Open()
	r.mu.Lock()
	defer r.mu.Unlock()
	s := Status{
		Enabled: r.Enabled(), Pending: len(r.outbox), Sent: r.sent, Dropped: r.dropped,
		LastError: r.lastErr, OpenBuckets: buckets, LiveConns: conns,
		LastMinute: append([]Aggregate{}, r.last...),
	}
	if !r.lastSent.IsZero() {
		t := r.lastSent
		s.LastSentAt = &t
	}
	return s
}

// newUUID returns a random (v4) UUID.
func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	var s [36]byte
	hex.Encode(s[0:8], b[0:4])
	s[8] = '-'
	hex.Encode(s[9:13], b[4:6])
	s[13] = '-'
	hex.Encode(s[14:18], b[6:8])
	s[18] = '-'
	hex.Encode(s[19:23], b[8:10])
	s[23] = '-'
	hex.Encode(s[24:], b[10:])
	return string(s[:])
}

// RapidNativeProjectID reads the RapidNative project id out of an orchd
// project name. The website names the projects it provisions after its own
// project id, "<uuid>" for staging and "<uuid>-prod" for production
// (OrchdProvisioningService); any other name isn't a RapidNative project.
func RapidNativeProjectID(name string) (string, bool) {
	id := strings.TrimSuffix(name, "-prod")
	if !isUUID(id) {
		return "", false
	}
	return strings.ToLower(id), true
}

func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
				return false
			}
		}
	}
	return true
}
