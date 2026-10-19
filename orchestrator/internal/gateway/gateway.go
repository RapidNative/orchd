// Package gateway is the tenant-facing data plane. It resolves the request Host
// against the control plane's route table to find a workload, wakes it on demand
// (scale-to-zero resume), and reverse proxies every path through to it. A route
// is an exact hostname match, so both convention subdomains
// (<ref>-<role>.<base-domain>) and custom domains work the same way.
package gateway

import (
	"context"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"syscall"
	"time"

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
	// startupWait: see config.StartupWait.
	startupWait time.Duration
}

func New(mgr *manager.Manager, cfg ...config.Config) *Gateway {
	g := &Gateway{mgr: mgr}
	if len(cfg) > 0 {
		c := cfg[0]
		g.startupWait = c.StartupWait
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
				w.Header().Set("X-Orchd-No-Route", "1")
				corsError(w, "reprovision timed out", http.StatusGatewayTimeout)
				return
			}
			w.Header().Set("X-Orchd-No-Route", "1")
			corsError(w, "no route for request", http.StatusNotFound)
			return
		}
	}

	addr, err := g.mgr.EnsureRunning(r.Context(), workload.ID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			w.Header().Set("X-Orchd-No-Route", "1")
			corsError(w, "workload not found", http.StatusNotFound)
			return
		}
		// The host IS routed here; the workload just failed to start. Do not
		// set X-Orchd-No-Route: the front door treats that header as "this
		// project lives on another instance" and redirects to staging, which
		// would send a production project's users to the wrong environment.
		// Surface the failure as a plain 502 instead.
		log.Printf("gateway: wake %s (%s) failed: %v", workload.ID, r.Host, err)
		corsError(w, "workload unavailable", http.StatusBadGateway)
		return
	}

	target := &url.URL{Scheme: "http", Host: addr}
	proxy := httputil.NewSingleHostReverseProxy(target)
	if g.startupWait > 0 {
		if until := g.mgr.StartedAt(workload.ID).Add(g.startupWait); time.Now().Before(until) {
			proxy.Transport = &startupTransport{base: http.DefaultTransport, until: until}
		}
	}
	origin := r.Header.Get("Origin")
	proxy.ModifyResponse = func(resp *http.Response) error {
		resp.Header.Del("X-Frame-Options")
		resp.Header.Set("Content-Security-Policy", "frame-ancestors *")
		allowOwnOrigin(resp.Header, origin, workload.ProjectID, g.lookupHost)
		return nil
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, e error) {
		log.Printf("gateway: proxy %s -> %s error: %v", workload.ID, addr, e)
		corsError(w, "upstream error", http.StatusBadGateway)
	}
	proxy.ServeHTTP(w, r)
}

// startupTransport retries a GET or HEAD that a just-started server refuses or
// resets, until it answers or the startup window ends. A dev server takes
// seconds to listen after its container starts, and Docker's port proxy
// accepts the connection and then resets it meanwhile. Without this the first
// request after every start (an editor open, a QR scan) got a 502, and Expo Go
// reported a timeout. Requests with a body are never retried.
type startupTransport struct {
	base  http.RoundTripper
	until time.Time
}

const startupRetryEvery = 500 * time.Millisecond

func (t *startupTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	for {
		resp, err := t.base.RoundTrip(req)
		if err == nil || !retryableStartup(req, err) || time.Now().Add(startupRetryEvery).After(t.until) {
			return resp, err
		}
		select {
		case <-req.Context().Done():
			return nil, req.Context().Err()
		case <-time.After(startupRetryEvery):
		}
	}
}

// retryableStartup: a body-less GET or HEAD that failed because nothing was
// listening yet (refused, or accepted then reset or closed before a response).
func retryableStartup(req *http.Request, err error) bool {
	if req.Method != http.MethodGet && req.Method != http.MethodHead {
		return false
	}
	if req.Body != nil && req.Body != http.NoBody {
		return false
	}
	var op *net.OpError
	return errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		(errors.As(err, &op) && op.Op == "dial")
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

// lookupHost finds the workload a hostname routes to, by exact host or a
// domain alias. Unlike resolve it never looks at a path or asks for a
// reprovision.
func (g *Gateway) lookupHost(host string) (*store.Workload, bool) {
	if wl, err := g.mgr.ResolveHost(host); err == nil {
		return wl, true
	}
	return g.resolveAlias(host)
}

// allowOwnOrigin lets a project's own pages read its responses. An app's
// screens and its API are separate routes (<ref>.<domain> and
// <ref>-api.<domain>, or custom domains), so every call between them is
// cross-origin, and the app's server rarely sets CORS itself. The preflight is
// answered above, so the request runs, and then the browser drops the
// response ("Load failed" in Safari, "Failed to fetch" in Chrome). An Origin
// that routes to a workload of the same project gets it echoed back in
// Access-Control-Allow-Origin; any other site still can't read the response,
// and no credentials are allowed. An upstream that sets its own CORS header is
// left alone.
func allowOwnOrigin(h http.Header, origin, projectID string, lookup func(host string) (*store.Workload, bool)) {
	if h.Get("Access-Control-Allow-Origin") != "" {
		return
	}
	h.Add("Vary", "Origin")
	if origin == "" || projectID == "" {
		return
	}
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return
	}
	if wl, ok := lookup(hostOnly(u.Host)); ok && wl.ProjectID == projectID {
		h.Set("Access-Control-Allow-Origin", origin)
	}
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

// corsError writes an error response with Access-Control-Allow-Origin so
// browsers don't mask the real status code behind a CORS failure.
func corsError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	http.Error(w, msg, code)
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
