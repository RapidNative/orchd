// Package gateway is the tenant-facing data plane. It maps <ref>.<base-domain>
// to a project, wakes the instance on demand (scale-to-zero resume), and reverse
// proxies every Supabase-shaped path (/rest, /auth, /storage, /realtime, /_/)
// through to it. WebSocket upgrades (realtime) pass through transparently.
package gateway

import (
	"context"
	"errors"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/tinbase/tinbase-cloud/orchestrator/internal/manager"
	"github.com/tinbase/tinbase-cloud/orchestrator/internal/store"
)

type Gateway struct {
	mgr        *manager.Manager
	baseDomain string
}

func New(mgr *manager.Manager, baseDomain string) *Gateway {
	return &Gateway{mgr: mgr, baseDomain: baseDomain}
}

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ref, ok := g.refFromHost(r.Host)
	if !ok {
		http.Error(w, "no project ref in host: "+r.Host, http.StatusNotFound)
		return
	}

	addr, err := g.mgr.EnsureRunning(r.Context(), ref)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.Error(w, "project not found: "+ref, http.StatusNotFound)
			return
		}
		log.Printf("gateway: wake %s failed: %v", ref, err)
		http.Error(w, "project unavailable", http.StatusBadGateway)
		return
	}

	target := &url.URL{Scheme: "http", Host: addr}
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, e error) {
		log.Printf("gateway: proxy %s -> %s error: %v", ref, addr, e)
		http.Error(w, "upstream error", http.StatusBadGateway)
	}
	proxy.ServeHTTP(w, r)
}

// refFromHost extracts the leftmost label as the project ref, requiring the rest
// of the host to equal the configured base domain. Host may carry a port.
func (g *Gateway) refFromHost(host string) (string, bool) {
	if i := strings.IndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	host = strings.TrimSuffix(host, ".")
	suffix := "." + g.baseDomain
	if !strings.HasSuffix(host, suffix) {
		return "", false
	}
	label := strings.TrimSuffix(host, suffix)
	if label == "" || strings.Contains(label, ".") {
		// Require exactly one label to the left of the base domain.
		if idx := strings.IndexByte(label, '.'); idx >= 0 {
			label = label[:idx]
		} else {
			return "", false
		}
	}
	return label, true
}

// Serve runs the gateway HTTP server until ctx is cancelled.
func (g *Gateway) Serve(ctx context.Context, addr string) error {
	srv := &http.Server{Addr: addr, Handler: g}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	log.Printf("gateway listening on %s (routing *.%s)", addr, g.baseDomain)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
