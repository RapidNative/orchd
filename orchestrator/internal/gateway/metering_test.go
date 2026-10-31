package gateway

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tinbase/tinbase-cloud/orchestrator/internal/metering"
)

const rnProject = "11111111-2222-4333-8444-555555555555"

func drainOne(t *testing.T, m *metering.Meter) metering.Bucket {
	t.Helper()
	bs := m.Drain(time.Time{})
	if len(bs) != 1 {
		t.Fatalf("buckets = %+v, want one", bs)
	}
	return bs[0]
}

// Proxied requests are counted against their workload, with the RapidNative
// project id read from the orchd project's name.
func TestGatewayMetersProxiedTraffic(t *testing.T) {
	g, st := newTestGateway(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if r.URL.Path == "/missing" {
			http.Error(w, "nope", http.StatusNotFound) // "nope\n"
			return
		}
		_, _ = io.WriteString(w, "0123456789")
	}), rnProject+"-prod")
	srv := httptest.NewServer(g)
	defer srv.Close()

	do := func(method, path, body string) {
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		req.Host = "app.test.local"
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.ReadAll(res.Body)
		res.Body.Close()
	}
	do("POST", "/rest/v1/items", `{"a":1}`) // 7 in, 10 out
	do("GET", "/missing", "")               // 5 out
	do("OPTIONS", "/rest/v1/items", "")     // preflight: answered by the gateway, not metered

	b := drainOne(t, g.Meter())
	if b.Workload != "w" || b.ProjectID != "p" || b.Requests != 2 || b.In != 7 || b.Out != 15 || b.Status != [4]int64{1, 0, 1, 0} {
		t.Fatalf("bucket = %+v", b)
	}
	if id, ok := g.ResolveProject("p"); !ok || id != rnProject {
		t.Fatalf("ResolveProject = %q %v", id, ok)
	}
	if _, ok := g.ResolveProject("gone"); ok {
		t.Fatal("resolved a missing project")
	}
	p, _ := st.GetProject("p")
	p.Name = "letsdeployit-thing"
	_ = st.PutProject(p)
	if _, ok := g.ResolveProject("p"); ok {
		t.Fatal("a name that isn't a RapidNative id resolved")
	}
}

// A WebSocket-style upgrade goes through the reverse proxy's hijack, keeps
// working, and its bytes are counted both ways until it closes.
func TestGatewayMetersUpgradedConnection(t *testing.T) {
	g, _ := newTestGateway(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") != "echo" {
			http.Error(w, "upgrade required", http.StatusUpgradeRequired)
			return
		}
		conn, brw, err := http.NewResponseController(w).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		_, _ = brw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: echo\r\nConnection: Upgrade\r\n\r\n")
		_ = brw.Flush()
		buf := make([]byte, 64)
		for {
			n, err := brw.Read(buf)
			if err != nil {
				return
			}
			if _, err := conn.Write(buf[:n]); err != nil {
				return
			}
		}
	}), rnProject)
	srv := httptest.NewServer(g)
	defer srv.Close()

	c, err := net.Dial("tcp", strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(c, "GET /socket HTTP/1.1\r\nHost: app.test.local\r\nUpgrade: echo\r\nConnection: Upgrade\r\n\r\n")
	br := bufio.NewReader(c)
	res, err := http.ReadResponse(br, nil)
	if err != nil || res.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("upgrade through the gateway: %v %v", res, err)
	}
	for _, msg := range []string{"hello", "world!"} {
		_, _ = io.WriteString(c, msg)
		got := make([]byte, len(msg))
		if _, err := io.ReadFull(br, got); err != nil || string(got) != msg {
			t.Fatalf("echo %q: %q %v", msg, got, err)
		}
	}
	if _, conns := g.Meter().Open(); conns != 1 {
		t.Fatalf("live conns = %d, want the open socket", conns)
	}
	c.Close()

	deadline := time.Now().Add(5 * time.Second)
	for {
		if b, conns := g.Meter().Open(); conns == 0 && b > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the closed socket was never booked")
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond) // Finish runs once ServeHTTP returns
	b := drainOne(t, g.Meter())
	if b.Requests != 1 || b.Status[0] != 1 || b.In != 11 || b.Out < 11 {
		t.Fatalf("bucket = %+v, want 1 request (2xx), 11 bytes in, 11+ out (the 101 head too)", b)
	}
}

// BenchmarkServeHTTPUnmetered is BenchmarkServeHTTP with the meter off, the
// gateway as it was before metering.
func BenchmarkServeHTTPUnmetered(b *testing.B) {
	g, _ := newTestGateway(b, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = io.WriteString(w, `{"ok":true,"items":[1,2,3,4,5,6,7,8,9,10]}`)
	}), "")
	g.meter = nil
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest("POST", "http://app.test.local/rest/v1/items", serverBody(`{"name":"x"}`))
		rec := httptest.NewRecorder()
		g.ServeHTTP(rec, req)
		if rec.Code != 200 {
			b.Fatalf("status %d: %s", rec.Code, rec.Body)
		}
	}
}

// Traffic goes to the website behind the webhook, with the webhook's key,
// read live; ORCHD_WEBSITE_URL overrides it with a Bearer token.
func TestGatewayUsageTarget(t *testing.T) {
	g, _ := newTestGateway(t, http.NotFoundHandler(), "")
	if tg := g.UsageTarget(); tg.URL != "" {
		t.Fatalf("no webhook, no override: %+v, want off", tg)
	}
	if err := g.mgr.SetWebhook("https://www.rapidnative.com/api/webhook/orchd?x=1", "whk"); err != nil {
		t.Fatal(err)
	}
	want := metering.Target{URL: "https://www.rapidnative.com/api/internal/usage", Header: "X-Webhook-Key", Value: "whk"}
	if tg := g.UsageTarget(); tg != want {
		t.Fatalf("from the webhook: %+v, want %+v", tg, want)
	}
	_ = g.mgr.SetWebhook("http://localhost:3000/api/webhook/orchd", "other")
	want = metering.Target{URL: "http://localhost:3000/api/internal/usage", Header: "X-Webhook-Key", Value: "other"}
	if tg := g.UsageTarget(); tg != want {
		t.Fatalf("after SetWebhook, without a restart: %+v, want %+v", tg, want)
	}
	_ = g.mgr.SetWebhook("not a url", "k")
	if tg := g.UsageTarget(); tg.URL != "" {
		t.Fatalf("a bad webhook URL: %+v, want off", tg)
	}

	g.websiteURL, g.websiteToken = "https://staging.rapidnative.com/", "tok"
	want = metering.Target{URL: "https://staging.rapidnative.com/api/internal/usage", Header: "Authorization", Value: "Bearer tok"}
	if tg := g.UsageTarget(); tg != want {
		t.Fatalf("override: %+v, want %+v", tg, want)
	}
}
