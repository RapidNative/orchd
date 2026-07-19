// Package api is the platform / service-level control plane API: the surface an
// admin panel or RapidNative calls to provision and manage projects, workloads,
// and routes. It is separate from the tenant-facing gateway (different port,
// different audience) and returns plain JSON.
package api

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/tinbase/tinbase-cloud/orchestrator/internal/config"
	"github.com/tinbase/tinbase-cloud/orchestrator/internal/manager"
	"github.com/tinbase/tinbase-cloud/orchestrator/internal/runtime"
	"github.com/tinbase/tinbase-cloud/orchestrator/internal/store"
)

type API struct {
	mgr    *manager.Manager
	cfg    config.Config
	apiKey string // when non-empty, /v1/* requires it
}

func New(mgr *manager.Manager, cfg config.Config, apiKey string) *API {
	return &API{mgr: mgr, cfg: cfg, apiKey: apiKey}
}

func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "auth": a.apiKey != ""})
	})
	mux.HandleFunc("POST /v1/projects", a.createProject)
	mux.HandleFunc("GET /v1/projects", a.listProjects)
	mux.HandleFunc("GET /v1/projects/{id}", a.getProject)
	mux.HandleFunc("DELETE /v1/projects/{id}", a.deleteProject)
	mux.HandleFunc("POST /v1/projects/{id}/workloads", a.addWorkload)
	mux.HandleFunc("GET /v1/workloads/{id}", a.getWorkload)
	mux.HandleFunc("DELETE /v1/workloads/{id}", a.deleteWorkload)
	mux.HandleFunc("POST /v1/workloads/{id}/routes", a.addRoute)
	mux.HandleFunc("GET /v1/presets", a.listPresets)
	// on-demand TLS gate for Caddy: only mint certs for hosts we actually serve.
	mux.HandleFunc("GET /internal/tls-allow", a.tlsAllow)
	return a.auth(mux)
}

// tlsAllow answers Caddy's on-demand-TLS "ask": 200 => issue a cert for this
// host, anything else => refuse. We allow admin/api and any host in the route
// table, so random subdomains cannot trigger certificate issuance.
func (a *API) tlsAllow(w http.ResponseWriter, r *http.Request) {
	domain := strings.ToLower(r.URL.Query().Get("domain"))
	base := strings.ToLower(a.cfg.BaseDomain)
	if domain == "admin."+base || domain == "api."+base {
		w.WriteHeader(http.StatusOK)
		return
	}
	if _, err := a.mgr.ResolveHost(domain); err == nil {
		w.WriteHeader(http.StatusOK)
		return
	}
	w.WriteHeader(http.StatusForbidden)
}

// auth gates every endpoint except /healthz behind the API key. If no key is
// configured (local dev), everything is open. CORS is permitted so the admin UI
// can call the API from the browser with the key.
func (a *API) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, X-API-Key, Content-Type")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.URL.Path == "/healthz" || strings.HasPrefix(r.URL.Path, "/internal/") || a.apiKey == "" {
			next.ServeHTTP(w, r)
			return
		}
		if !a.validKey(r) {
			writeErr(w, http.StatusUnauthorized, errors.New("missing or invalid API key"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *API) validKey(r *http.Request) bool {
	presented := r.Header.Get("X-API-Key")
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		presented = strings.TrimPrefix(h, "Bearer ")
	}
	if presented == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(presented), []byte(a.apiKey)) == 1
}

func (a *API) listPresets(w http.ResponseWriter, r *http.Request) {
	names := make([]string, 0, len(manager.Catalog))
	for k := range manager.Catalog {
		names = append(names, k)
	}
	writeJSON(w, http.StatusOK, names)
}

// ---- views ----

type workloadSpecReq struct {
	Preset   string               `json:"preset"`
	Type     runtime.WorkloadType `json:"type"`
	Name     string               `json:"name"`
	Image    string               `json:"image"`
	Port     int                  `json:"port"`
	MemoryMB int                  `json:"memory_mb"`
	CPUs     float64              `json:"cpus"`
}

func (r workloadSpecReq) toSpec() manager.WorkloadSpec {
	return manager.WorkloadSpec{
		Preset: r.Preset, Type: r.Type, Name: r.Name, Image: r.Image, Port: r.Port,
		MemoryMB: r.MemoryMB, CPUs: r.CPUs,
	}
}

type workloadView struct {
	*store.Workload
	Routes    []string `json:"routes"`    // hostnames
	Endpoints []string `json:"endpoints"` // subdomain endpoints (local dev)
	Subroutes []string `json:"subroutes"` // public subroute endpoints (<PublicURL>/w/<key>)
}

type projectView struct {
	*store.Project
	Workloads []workloadView `json:"workloads"`
}

func (a *API) workloadView(w *store.Workload) workloadView {
	routes := a.mgr.Store().ListRoutesForWorkload(w.ID)
	hosts := make([]string, 0, len(routes))
	endpoints := make([]string, 0, len(routes))
	subroutes := make([]string, 0, len(routes))
	for _, r := range routes {
		hosts = append(hosts, r.Host)
		if a.cfg.PublicScheme == "https" {
			endpoints = append(endpoints, "https://"+r.Host) // public subdomain, TLS on 443
		} else {
			endpoints = append(endpoints, "http://"+r.Host+gatewayPort(a.cfg.GatewayAddr))
		}
		if a.cfg.PublicURL != "" {
			subroutes = append(subroutes, strings.TrimRight(a.cfg.PublicURL, "/")+"/w/"+r.Key)
		}
	}
	return workloadView{Workload: w, Routes: hosts, Endpoints: endpoints, Subroutes: subroutes}
}

func (a *API) projectView(p *store.Project) projectView {
	var wl []workloadView
	for _, w := range a.mgr.Store().ListWorkloads(p.ID) {
		wl = append(wl, a.workloadView(w))
	}
	return projectView{Project: p, Workloads: wl}
}

// ---- handlers ----

func (a *API) createProject(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name      string            `json:"name"`
		Workloads []workloadSpecReq `json:"workloads"`
	}
	if r.ContentLength > 0 {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	specs := make([]manager.WorkloadSpec, 0, len(body.Workloads))
	for _, ws := range body.Workloads {
		specs = append(specs, ws.toSpec())
	}
	proj, _, err := a.mgr.CreateProject(r.Context(), body.Name, specs)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, a.projectView(proj))
}

func (a *API) listProjects(w http.ResponseWriter, r *http.Request) {
	list := a.mgr.Store().ListProjects()
	views := make([]projectView, 0, len(list))
	for _, p := range list {
		views = append(views, a.projectView(p))
	}
	writeJSON(w, http.StatusOK, views)
}

func (a *API) getProject(w http.ResponseWriter, r *http.Request) {
	p, err := a.mgr.Store().GetProject(r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, a.projectView(p))
}

func (a *API) deleteProject(w http.ResponseWriter, r *http.Request) {
	if err := a.mgr.DeleteProject(r.Context(), r.PathValue("id")); err != nil {
		a.writeLookupErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) addWorkload(w http.ResponseWriter, r *http.Request) {
	var ws workloadSpecReq
	if err := json.NewDecoder(r.Body).Decode(&ws); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	wl, err := a.mgr.AddWorkload(r.Context(), r.PathValue("id"), ws.toSpec())
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusNotFound, err)
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, a.workloadView(wl))
}

func (a *API) getWorkload(w http.ResponseWriter, r *http.Request) {
	wl, err := a.mgr.Store().GetWorkload(r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, a.workloadView(wl))
}

func (a *API) deleteWorkload(w http.ResponseWriter, r *http.Request) {
	if err := a.mgr.DeleteWorkload(r.Context(), r.PathValue("id")); err != nil {
		a.writeLookupErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) addRoute(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Host string `json:"host"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Host == "" {
		writeErr(w, http.StatusBadRequest, errors.New("host required"))
		return
	}
	if err := a.mgr.AddRoute(body.Host, r.PathValue("id")); err != nil {
		a.writeLookupErr(w, err)
		return
	}
	wl, _ := a.mgr.Store().GetWorkload(r.PathValue("id"))
	writeJSON(w, http.StatusCreated, a.workloadView(wl))
}

// ---- helpers ----

func (a *API) writeLookupErr(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	writeErr(w, http.StatusInternalServerError, err)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

// gatewayPort returns the ":port" suffix (if any) so displayed endpoints are
// clickable in local dev where the gateway does not run on :80.
func gatewayPort(gatewayAddr string) string {
	for i := len(gatewayAddr) - 1; i >= 0; i-- {
		if gatewayAddr[i] == ':' {
			return gatewayAddr[i:]
		}
	}
	return ""
}
