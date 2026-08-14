// Package gateway is the tenant-facing data plane. It resolves the request Host
// against the control plane's route table to find a workload, wakes it on demand
// (scale-to-zero resume), and reverse proxies every path through to it. A route
// is an exact hostname match, so both convention subdomains
// (<ref>-<role>.<base-domain>) and custom domains work the same way.
package gateway

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"

	"github.com/tinbase/tinbase-cloud/orchestrator/internal/manager"
	"github.com/tinbase/tinbase-cloud/orchestrator/internal/runtime"
	"github.com/tinbase/tinbase-cloud/orchestrator/internal/store"
)

type Gateway struct {
	mgr *manager.Manager
}

func New(mgr *manager.Manager) *Gateway {
	return &Gateway{mgr: mgr}
}

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	workload, err := g.resolve(r)
	if err != nil {
		http.Error(w, "no route for request", http.StatusNotFound)
		return
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

	// Tinbase dashboard auto-login: inject a script into the /_/ HTML page
	// that fills the service-role key and clicks login automatically.
	isDashboard := workload.Type == runtime.WorkloadTinbaseProject &&
		r.Method == "GET" &&
		(r.URL.Path == "/_/" || r.URL.Path == "/_")
	if isDashboard {
		proxy.ModifyResponse = autoLoginModifier(workload.SvcKey)
	}

	proxy.ServeHTTP(w, r)
}

// resolve finds the workload for a request, first by Host (subdomain routing),
// then by a /w/<key>/... path prefix (subroute routing, the interim before
// wildcard subdomains). Subroute matches rewrite the path to strip /w/<key> so
// the upstream sees a normal root-relative path.
func (g *Gateway) resolve(r *http.Request) (*store.Workload, error) {
	if wl, err := g.mgr.ResolveHost(hostOnly(r.Host)); err == nil {
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

// autoLoginModifier returns an httputil.ReverseProxy ModifyResponse that
// injects a tiny auto-login script into Tinbase Studio's dashboard HTML.
// If svcKey is non-empty, the script sets sessionStorage directly (instant).
// Otherwise it reads the ?_key= URL param, fills the input, and clicks login.
func autoLoginModifier(svcKey string) func(*http.Response) error {
	return func(resp *http.Response) error {
		ct := resp.Header.Get("Content-Type")
		if !strings.Contains(ct, "text/html") {
			return nil
		}

		raw, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return err
		}

		// Decompress if needed.
		body, encoding := raw, resp.Header.Get("Content-Encoding")
		switch encoding {
		case "gzip":
			r, err := gzip.NewReader(bytes.NewReader(raw))
			if err == nil {
				body, err = io.ReadAll(r)
				r.Close()
			}
			if err != nil {
				resp.Body = io.NopCloser(bytes.NewReader(raw))
				return nil
			}
		case "deflate":
			r := flate.NewReader(bytes.NewReader(raw))
			body, err = io.ReadAll(r)
			r.Close()
			if err != nil {
				resp.Body = io.NopCloser(bytes.NewReader(raw))
				return nil
			}
		}

		var script string
		if svcKey != "" {
			script = fmt.Sprintf(`<script>sessionStorage.setItem("tinbase_service_key",%q)</script>`, svcKey)
		} else {
			script = `<script>
(function(){
  var k = new URLSearchParams(location.search).get('_key');
  if (!k) return;
  function tryFill() {
    var input = document.querySelector('input[placeholder*="service_role"]');
    if (!input) return setTimeout(tryFill, 200);
    var nativeSetter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value').set;
    nativeSetter.call(input, k);
    input.dispatchEvent(new Event('input', { bubbles: true }));
    var btn = document.querySelector('button');
    if (btn) setTimeout(function(){ btn.click(); }, 100);
  }
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', tryFill);
  else tryFill();
})();
</script>`
		}

		html := strings.Replace(string(body), "</body>", script+"</body>", 1)
		out := []byte(html)

		resp.Header.Del("Content-Encoding")
		resp.Header.Del("Transfer-Encoding")
		resp.Header.Set("Content-Length", strconv.Itoa(len(out)))
		resp.Body = io.NopCloser(bytes.NewReader(out))
		return nil
	}
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
