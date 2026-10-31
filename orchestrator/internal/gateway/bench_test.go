package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// serverBody hides strings.Reader's WriterTo, as a real server's request body
// does, so the transport copies it the way it copies production traffic.
func serverBody(s string) io.Reader { return struct{ io.Reader }{strings.NewReader(s)} }

// BenchmarkServeHTTP proxies a small POST through the whole gateway to a real
// upstream: resolve, wake check, reverse proxy.
func BenchmarkServeHTTP(b *testing.B) {
	g, _ := newTestGateway(b, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = io.WriteString(w, `{"ok":true,"items":[1,2,3,4,5,6,7,8,9,10]}`)
	}), "")
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
