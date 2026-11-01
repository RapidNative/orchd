package metering

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// ingest is a mock of the website's POST /api/internal/usage.
type ingest struct {
	mu       sync.Mutex
	fail     int // answer this many requests with failStatus first
	failCode int
	requests []ingestBody
	auth     []string
}

type ingestBody struct {
	Source     string            `json:"source"`
	Aggregates []json.RawMessage `json:"aggregates"`
}

func (s *ingest) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.Method != http.MethodPost || r.URL.Path != "/api/internal/usage" {
		http.Error(w, "wrong endpoint", http.StatusNotFound)
		return
	}
	if s.fail > 0 {
		s.fail--
		http.Error(w, "nope", s.failCode)
		return
	}
	raw, _ := io.ReadAll(r.Body)
	var b ingestBody
	if err := json.Unmarshal(raw, &b); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.requests = append(s.requests, b)
	s.auth = append(s.auth, r.Header.Get("X-Webhook-Key"))
	fmt.Fprintf(w, `{"accepted":%d,"duplicates":0}`, len(b.Aggregates))
}

func (s *ingest) aggregates(t *testing.T) []map[string]any {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []map[string]any
	for _, r := range s.requests {
		for _, a := range r.Aggregates {
			var m map[string]any
			if err := json.Unmarshal(a, &m); err != nil {
				t.Fatal(err)
			}
			out = append(out, m)
		}
	}
	return out
}

func newTestReporter(t *testing.T, s *ingest) (*Reporter, *Meter, *clock) {
	t.Helper()
	m, clk := newClockedMeter()
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	r := NewReporter(m, ReporterConfig{
		Target: func() Target {
			return Target{URL: srv.URL + "/api/internal/usage", Header: "X-Webhook-Key", Value: "secret"}
		},
		Resolve: func(id string) (string, bool) {
			if id == "orchd-p1" {
				return "11111111-2222-4333-8444-555555555555", true
			}
			return "", false
		},
	})
	r.now = clk.now
	return r, m, clk
}

func TestReporterSendsAggregates(t *testing.T) {
	s := &ingest{}
	r, m, clk := newTestReporter(t, s)
	m.Record("app", "orchd-p1", 200, 100, 2000)
	m.Record("app", "orchd-p1", 404, 0, 10)
	m.Record("other", "orchd-unknown", 503, 5, 0)
	clk.advance(time.Minute)
	m.Record("app", "orchd-p1", 200, 1, 1) // the current minute stays open

	r.collect(clk.now())
	r.send(context.Background(), false)

	if len(s.requests) != 1 || s.requests[0].Source != "orchd" || s.auth[0] != "secret" {
		t.Fatalf("requests = %+v auth = %v", s.requests, s.auth)
	}
	byWorkload := map[string]map[string]any{}
	for _, a := range s.aggregates(t) {
		byWorkload[a["workload"].(string)] = a
	}
	app, other := byWorkload["app"], byWorkload["other"]
	if len(byWorkload) != 2 || app == nil || other == nil {
		t.Fatalf("aggregates = %v", byWorkload)
	}
	if app["minute"] != "2026-10-31T10:00:00Z" || app["project_id"] != "11111111-2222-4333-8444-555555555555" {
		t.Fatalf("app = %v", app)
	}
	if !isUUID(app["id"].(string)) || app["id"] == other["id"] {
		t.Fatalf("ids %v %v", app["id"], other["id"])
	}
	meters, _ := json.Marshal(app["meters"])
	status, _ := json.Marshal(app["status"])
	if string(meters) != `{"traffic_egress":2010,"traffic_ingress":100,"traffic_requests":2}` ||
		string(status) != `{"2xx":1,"3xx":0,"4xx":1,"5xx":0}` {
		t.Fatalf("app meters %s status %s", meters, status)
	}
	if v, ok := other["project_id"]; !ok || v != nil {
		t.Fatalf("an unresolved project must be sent as null, got %v (present %v)", v, ok)
	}
	if r.Pending() != 0 {
		t.Fatalf("pending = %d", r.Pending())
	}
	if b, _ := m.Open(); b != 1 {
		t.Fatalf("the open minute was drained (buckets left %d)", b)
	}
}

func TestReporterBatches(t *testing.T) {
	s := &ingest{}
	r, m, clk := newTestReporter(t, s)
	for i := 0; i < 1203; i++ {
		m.Record(fmt.Sprintf("w%d", i), "orchd-p1", 200, 1, 1)
	}
	clk.advance(time.Minute)
	r.collect(clk.now())
	r.send(context.Background(), false)
	var sizes []int
	for _, req := range s.requests {
		sizes = append(sizes, len(req.Aggregates))
	}
	if fmt.Sprint(sizes) != "[500 500 203]" {
		t.Fatalf("batch sizes = %v, want [500 500 203]", sizes)
	}
	seen := map[string]bool{}
	for _, a := range s.aggregates(t) {
		if seen[a["id"].(string)] {
			t.Fatal("duplicate id")
		}
		seen[a["id"].(string)] = true
	}
}

func TestReporterRetriesWithBackoff(t *testing.T) {
	s := &ingest{fail: 2, failCode: http.StatusServiceUnavailable}
	r, m, clk := newTestReporter(t, s)
	m.Record("app", "orchd-p1", 200, 1, 1)
	clk.advance(time.Minute)
	r.collect(clk.now())

	r.send(context.Background(), false) // fails: backoff 5s
	if r.Pending() != 1 || r.backoff != minBackoff {
		t.Fatalf("after one failure: pending %d backoff %s", r.Pending(), r.backoff)
	}
	r.send(context.Background(), false) // inside the backoff: not even tried
	if s.fail != 1 {
		t.Fatal("retried before the backoff ran out")
	}
	clk.advance(minBackoff)
	r.send(context.Background(), false) // fails again: backoff doubles
	if r.backoff != 2*minBackoff || r.Pending() != 1 {
		t.Fatalf("after two failures: pending %d backoff %s", r.Pending(), r.backoff)
	}
	clk.advance(2 * minBackoff)
	r.send(context.Background(), false)
	if r.Pending() != 0 || len(s.requests) != 1 || r.backoff != 0 || r.Status().LastError != "" {
		t.Fatalf("after recovery: pending %d requests %d backoff %s", r.Pending(), len(s.requests), r.backoff)
	}
	for i := 0; i < 20; i++ {
		r.backoff = min(max(r.backoff*2, minBackoff), maxBackoff)
	}
	if r.backoff != maxBackoff {
		t.Fatalf("backoff is not capped: %s", r.backoff)
	}
}

func TestReporterDropsMalformedBatch(t *testing.T) {
	s := &ingest{fail: 1, failCode: http.StatusBadRequest}
	r, m, clk := newTestReporter(t, s)
	m.Record("app", "orchd-p1", 200, 1, 1)
	clk.advance(time.Minute)
	r.collect(clk.now())
	r.send(context.Background(), false)
	if st := r.Status(); st.Pending != 0 || st.Dropped != 1 || r.backoff != 0 {
		t.Fatalf("a 400 must drop the batch without backing off: %+v", st)
	}
}

func TestReporterDropsOldestPastTheBuffer(t *testing.T) {
	s := &ingest{fail: 1 << 30, failCode: http.StatusBadGateway}
	r, m, clk := newTestReporter(t, s)
	r.cfg.Buffer = 3 * time.Minute
	for i := 0; i < 6; i++ {
		m.Record("app", "orchd-p1", 200, 1, 1)
		clk.advance(time.Minute)
		r.collect(clk.now())
		r.send(context.Background(), true)
	}
	// Minutes 10:00..10:05 collected; at 10:06:30 only those starting after
	// 10:03:30 survive.
	st := r.Status()
	if st.Pending != 2 || st.Dropped != 4 || st.LastError == "" {
		t.Fatalf("status = %+v, want 2 kept and 4 dropped", st)
	}
	if first := r.outbox[0].Minute; first.Format("15:04") != "10:04" {
		t.Fatalf("oldest kept = %s", first)
	}

	r.cfg.MaxPending = 1
	r.pruneLocked()
	if r.Pending() != 1 || r.outbox[0].Minute.Format("15:04") != "10:05" {
		t.Fatalf("count cap: pending %d", r.Pending())
	}
}

// Shutdown drains everything, the open minute and open connections too, and
// sends it before Run returns.
func TestReporterFlushesOnShutdown(t *testing.T) {
	s := &ingest{}
	r, m, _ := newTestReporter(t, s)
	m.Record("app", "orchd-p1", 200, 1, 1) // the current minute
	ctx, cancel := context.WithCancel(context.Background())
	go r.Run(ctx)
	cancel()
	select {
	case <-r.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return")
	}
	if a := s.aggregates(t); len(a) != 1 || a[0]["workload"] != "app" {
		t.Fatalf("shutdown sent %v", a)
	}
}

func TestReporterDisabledStillDrains(t *testing.T) {
	m, clk := newClockedMeter()
	r := NewReporter(m, ReporterConfig{})
	r.now = clk.now
	m.Record("app", "p", 200, 1, 1)
	clk.advance(time.Minute)
	r.collect(clk.now())
	r.send(context.Background(), true)
	st := r.Status()
	if st.Enabled || st.Pending != 0 || st.OpenBuckets != 0 || len(st.LastMinute) != 1 || st.LastMinute[0].ProjectID != nil {
		t.Fatalf("status = %+v", st)
	}
}

func TestRapidNativeProjectID(t *testing.T) {
	cases := map[string]string{
		"11111111-2222-4333-8444-555555555555":      "11111111-2222-4333-8444-555555555555",
		"11111111-2222-4333-8444-555555555555-prod": "11111111-2222-4333-8444-555555555555",
		"AAAAAAAA-2222-4333-8444-555555555555":      "aaaaaaaa-2222-4333-8444-555555555555",
		"":                                          "",
		"my-app":                                    "",
		"11111111-2222-4333-8444-55555555555g":      "",
		"11111111-2222-4333-8444-555555555555-staging": "",
	}
	for name, want := range cases {
		got, ok := RapidNativeProjectID(name)
		if got != want || ok != (want != "") {
			t.Errorf("RapidNativeProjectID(%q) = %q %v, want %q", name, got, ok, want)
		}
	}
	if id := newUUID(); !isUUID(id) || id[14] != '4' {
		t.Fatalf("newUUID = %q", id)
	}
}

// The target is read at each drain and send: minutes drained while it is off
// are discarded, and a new target (and key) applies at once.
func TestReporterFollowsTheTarget(t *testing.T) {
	s := &ingest{}
	srv := httptest.NewServer(s)
	defer srv.Close()
	m, clk := newClockedMeter()
	var mu sync.Mutex
	target := Target{}
	r := NewReporter(m, ReporterConfig{Target: func() Target {
		mu.Lock()
		defer mu.Unlock()
		return target
	}})
	r.now = clk.now

	m.Record("app", "p", 200, 1, 1)
	clk.advance(time.Minute)
	r.collect(clk.now())
	r.send(context.Background(), false)
	if r.Enabled() || r.Pending() != 0 || len(s.requests) != 0 {
		t.Fatal("sent or kept traffic while off")
	}

	mu.Lock()
	target = Target{URL: srv.URL + "/api/internal/usage", Header: "X-Webhook-Key", Value: "k2"}
	mu.Unlock()
	m.Record("app", "p", 200, 1, 1)
	clk.advance(time.Minute)
	r.collect(clk.now())
	r.send(context.Background(), false)
	if len(s.requests) != 1 || len(s.requests[0].Aggregates) != 1 || s.auth[0] != "k2" {
		t.Fatalf("after turning on: %d requests, auth %v", len(s.requests), s.auth)
	}
	if st := r.Status(); !st.Enabled || st.Target != target.URL {
		t.Fatalf("status = %+v", st)
	}
}
