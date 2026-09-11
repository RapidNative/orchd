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

	"github.com/tinbase/tinbase-cloud/orchestrator/internal/config"
	"github.com/tinbase/tinbase-cloud/orchestrator/internal/manager"
	"github.com/tinbase/tinbase-cloud/orchestrator/internal/store"
)

type Gateway struct {
	mgr *manager.Manager
	// allDomains is [BaseDomain] + AltDomains. When an exact host lookup
	// misses, the gateway tries swapping the suffix with each other domain
	// in this list so routes registered under an old base domain still
	// resolve after a domain rename.
	allDomains []string
}

func New(mgr *manager.Manager, cfg ...config.Config) *Gateway {
	g := &Gateway{mgr: mgr}
	if len(cfg) > 0 {
		c := cfg[0]
		seen := map[string]bool{}
		for _, d := range append([]string{c.BaseDomain}, c.AltDomains...) {
			d = strings.ToLower(d)
			if d != "" && !seen[d] {
				g.allDomains = append(g.allDomains, d)
				seen[d] = true
			}
		}
	}
	return g
}

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Handle CORS preflight at the gateway so OPTIONS never reaches the
	// upstream. For non-preflight requests the upstream sets its own CORS
	// headers — we must NOT duplicate them or the browser sees "*, *".
	if r.Method == http.MethodOptions {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, X-API-Key, Content-Type, apikey, Prefer, Range, x-client-info, x-supabase-api-version, accept-profile, content-profile, x-upsert, cache-control")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Expose-Headers", "Content-Range, Range")
		w.WriteHeader(http.StatusNoContent)
		return
	}

	workload, err := g.resolve(r)
	if err != nil {
		// No route for this host. Ask the reprovision webhook (if configured)
		// to re-create the missing workload, holding the request until its
		// route reappears, then continue proxying below.
		workload, err = g.mgr.RequestReprovision(r.Context(), hostOnly(r.Host))
		if err != nil {
			if errors.Is(err, manager.ErrReprovisionTimeout) {
				http.Error(w, "reprovision timed out", http.StatusGatewayTimeout)
				return
			}
			http.Error(w, "no route for request", http.StatusNotFound)
			return
		}
	}

	addr, err := g.mgr.EnsureRunning(r.Context(), workload.ID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.Error(w, "workload not found", http.StatusNotFound)
			return
		}
		log.Printf("gateway: wake %s (%s) failed: %v", workload.ID, r.Host, err)
		http.Error(w, "workload unavailable", http.StatusBadGateway)
		return
	}

	target := &url.URL{Scheme: "http", Host: addr}
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, e error) {
		log.Printf("gateway: proxy %s -> %s error: %v", workload.ID, addr, e)
		http.Error(w, "upstream error", http.StatusBadGateway)
	}
	proxy.ServeHTTP(w, r)
}

// resolve finds the workload for a request, first by Host (subdomain routing),
// then by trying domain alias substitutions (so routes registered under an old
// base domain still match after a rename), then by a /w/<key>/... path prefix
// (subroute routing). Subroute matches rewrite the path to strip /w/<key> so
// the upstream sees a normal root-relative path.
func (g *Gateway) resolve(r *http.Request) (*store.Workload, error) {
	host := hostOnly(r.Host)
	if wl, err := g.mgr.ResolveHost(host); err == nil {
		return wl, nil
	}
	// Try swapping the domain suffix with each known alias.
	if wl, ok := g.resolveAlias(host); ok {
		return wl, nil
	}
	if key, rest, ok := parseSubroute(r.URL.Path); ok {
		wl, err := g.mgr.ResolveKey(key)
		if err != nil {
			return nil, err
		}
		r.URL.Path = rest
		r.URL.RawPath = "" // let net/http re-derive from Path
		return wl, nil
	}
	return nil, store.ErrNotFound
}

// resolveAlias tries replacing the domain suffix in host with each known
// domain alias. For example, if host is "slug.rapidnative.dev" and
// allDomains contains ["rapidnative.dev", "rapidnative.app"], it tries
// looking up "slug.rapidnative.app" in the route table.
func (g *Gateway) resolveAlias(host string) (*store.Workload, bool) {
	for _, d := range g.allDomains {
		if !strings.HasSuffix(host, "."+d) {
			continue
		}
		prefix := host[:len(host)-len(d)] // includes trailing dot
		for _, alt := range g.allDomains {
			if alt == d {
				continue
			}
			if wl, err := g.mgr.ResolveHost(prefix + alt); err == nil {
				return wl, true
			}
		}
	}
	return nil, false
}

// parseSubroute splits "/w/<key>/rest..." into ("<key>", "/rest...", true).
// "/w/<key>" (no trailing slash) yields ("<key>", "/", true).
func parseSubroute(path string) (key, rest string, ok bool) {
	const p = "/w/"
	if !strings.HasPrefix(path, p) {
		return "", "", false
	}
	tail := path[len(p):]
	if tail == "" {
		return "", "", false
	}
	if i := strings.IndexByte(tail, '/'); i >= 0 {
		key = tail[:i]
		rest = tail[i:]
	} else {
		key = tail
		rest = "/"
	}
	if key == "" {
		return "", "", false
	}
	return key, rest, true
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
