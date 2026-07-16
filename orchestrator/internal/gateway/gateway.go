// Package gateway is the tenant-facing data plane. It resolves the request Host
// against the control plane's route table to find a workload, wakes it on demand
// (scale-to-zero resume), and reverse proxies every path through to it. A route
// is an exact hostname match, so both convention subdomains
// (<ref>-<role>.<base-domain>) and custom domains work the same way.
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
	mgr *manager.Manager
}

func New(mgr *manager.Manager) *Gateway {
	return &Gateway{mgr: mgr}
}

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	host := hostOnly(r.Host)

	workload, err := g.mgr.ResolveHost(host)
	if err != nil {
		http.Error(w, "no route for host: "+host, http.StatusNotFound)
		return
	}

	addr, err := g.mgr.EnsureRunning(r.Context(), workload.ID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.Error(w, "workload not found", http.StatusNotFound)
			return
		}
		log.Printf("gateway: wake %s (%s) failed: %v", workload.ID, host, err)
		http.Error(w, "workload unavailable", http.StatusBadGateway)
		return
	}

	target := &url.URL{Scheme: "http", Host: addr}
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, e error) {
		log.Printf("gateway: proxy %s -> %s error: %v", host, addr, e)
		http.Error(w, "upstream error", http.StatusBadGateway)
	}
	proxy.ServeHTTP(w, r)
}

// hostOnly strips any port from the Host header and lowercases it.
func hostOnly(host string) string {
	if i := strings.IndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	return strings.ToLower(strings.TrimSuffix(host, "."))
}

// Serve runs the gateway HTTP server until ctx is cancelled.
func (g *Gateway) Serve(ctx context.Context, addr string) error {
	srv := &http.Server{Addr: addr, Handler: g}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	log.Printf("gateway listening on %s (route-table resolution)", addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
