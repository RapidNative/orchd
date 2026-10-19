package gateway

import (
	"net/http"
	"testing"

	"github.com/tinbase/tinbase-cloud/orchestrator/internal/store"
)

func routes(m map[string]string) func(string) (*store.Workload, bool) {
	return func(host string) (*store.Workload, bool) {
		p, ok := m[host]
		if !ok {
			return nil, false
		}
		return &store.Workload{ProjectID: p}, true
	}
}

func TestAllowOwnOrigin(t *testing.T) {
	lookup := routes(map[string]string{
		"p1.rapidnative.dev":     "p1",
		"p1-api.rapidnative.dev": "p1",
		"app.example.com":        "p1", // custom domain
		"p2.rapidnative.dev":     "p2",
	})
	cases := []struct {
		name, origin, upstream, want string
	}{
		{"own screens", "https://p1.rapidnative.dev", "", "https://p1.rapidnative.dev"},
		{"own custom domain", "https://app.example.com", "", "https://app.example.com"},
		{"origin with port", "http://p1.rapidnative.dev:8080", "", "http://p1.rapidnative.dev:8080"},
		{"another project", "https://p2.rapidnative.dev", "", ""},
		{"unrouted site", "https://evil.example", "", ""},
		{"null origin", "null", "", ""},
		{"no origin (native app)", "", "", ""},
		{"upstream sets its own", "https://p2.rapidnative.dev", "*", "*"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := http.Header{}
			if c.upstream != "" {
				h.Set("Access-Control-Allow-Origin", c.upstream)
			}
			allowOwnOrigin(h, c.origin, "p1", lookup)
			if v := h.Values("Access-Control-Allow-Origin"); len(v) > 1 || h.Get("Access-Control-Allow-Origin") != c.want {
				t.Fatalf("Access-Control-Allow-Origin = %q, want %q", v, c.want)
			}
			if h.Get("Access-Control-Allow-Credentials") != "" {
				t.Fatal("credentials must never be allowed")
			}
			if c.upstream == "" && h.Get("Vary") != "Origin" {
				t.Fatalf("Vary = %q, want Origin", h.Get("Vary"))
			}
		})
	}
}
