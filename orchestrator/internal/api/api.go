// Package api is the platform / service-level control plane API: the surface an
// admin panel or RapidNative calls to provision and manage projects, workloads,
// and routes. It is separate from the tenant-facing gateway (different port,
// different audience) and returns plain JSON.
package api

import (
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tinbase/tinbase-cloud/orchestrator/internal/backup"
	"github.com/tinbase/tinbase-cloud/orchestrator/internal/config"
	"github.com/tinbase/tinbase-cloud/orchestrator/internal/manager"
	"github.com/tinbase/tinbase-cloud/orchestrator/internal/runtime"
	"github.com/tinbase/tinbase-cloud/orchestrator/internal/store"
)

type API struct {
	mgr     *manager.Manager
	cfg     config.Config
	apiKey  string // when non-empty, /v1/* requires it
	limiter *rateLimiter
}

func New(mgr *manager.Manager, cfg config.Config, apiKey string) *API {
	return &API{mgr: mgr, cfg: cfg, apiKey: apiKey, limiter: newRateLimiter(cfg.RateLimitPerMin)}
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
	mux.HandleFunc("DELETE /v1/routes", a.removeRoute)
	mux.HandleFunc("POST /v1/workloads/{id}/keepwarm", a.setKeepWarm)
	mux.HandleFunc("PUT /v1/workloads/{id}/env", a.setWorkloadEnv)
	mux.HandleFunc("POST /v1/workloads/{id}/start", a.workloadStart)
	mux.HandleFunc("POST /v1/workloads/{id}/stop", a.workloadStop)
	mux.HandleFunc("POST /v1/workloads/{id}/restart", a.workloadRestart)
	mux.HandleFunc("POST /v1/workloads/{id}/reset", a.workloadReset)
	mux.HandleFunc("POST /v1/projects/{id}/start", a.projectStart)
	mux.HandleFunc("POST /v1/projects/{id}/stop", a.projectStop)
	mux.HandleFunc("POST /v1/projects/{id}/restart", a.projectRestart)
	mux.HandleFunc("POST /v1/projects/{id}/reset", a.projectReset)
	mux.HandleFunc("PUT /v1/projects/{id}/env", a.setProjectEnv)
	mux.HandleFunc("GET /v1/workloads/{id}/stats", a.workloadStats)
	mux.HandleFunc("GET /v1/workloads/{id}/logs", a.workloadLogs)
	mux.HandleFunc("GET /v1/backups", a.listBackups)
	mux.HandleFunc("GET /v1/workloads/{id}/backups", a.listWorkloadBackups)
	mux.HandleFunc("POST /v1/workloads/{id}/backups", a.createBackup)
	mux.HandleFunc("POST /v1/projects/{id}/backups", a.backupProject)
	mux.HandleFunc("POST /v1/workloads/{id}/restore", a.restoreWorkload)
	mux.HandleFunc("DELETE /v1/backups/{id}", a.deleteBackup)
	mux.HandleFunc("GET /v1/presets", a.listPresets)
	mux.HandleFunc("GET /v1/info", a.info)
	mux.HandleFunc("GET /v1/keys", a.listKeys)
	mux.HandleFunc("POST /v1/keys", a.createKey)
	mux.HandleFunc("DELETE /v1/keys/{id}", a.deleteKey)
	mux.HandleFunc("GET /v1/regions", a.listRegions)
	mux.HandleFunc("POST /v1/regions", a.createRegion)
	mux.HandleFunc("DELETE /v1/regions/{id}", a.deleteRegion)
	mux.HandleFunc("POST /v1/regions/{id}/default", a.setDefaultRegion)
	mux.HandleFunc("GET /v1/images", a.listImages)
	mux.HandleFunc("POST /v1/images/pull", a.pullImage)
	mux.HandleFunc("DELETE /v1/images", a.removeImage)
	mux.HandleFunc("GET /v1/templates", a.listTemplates)
	mux.HandleFunc("PUT /v1/templates", a.setTemplate)
	mux.HandleFunc("GET /v1/templates/{name}", a.getTemplate)
	mux.HandleFunc("GET /v1/templates/{name}/files", a.templateFiles)
	mux.HandleFunc("GET /v1/templates/{name}/bundle", a.templateBundle)
	mux.HandleFunc("POST /v1/templates/{name}/build", a.buildImage)
	mux.HandleFunc("GET /v1/built-images", a.listBuiltImages)
	mux.HandleFunc("GET /v1/built-images/{name}/{version}/bundle", a.imageBundle)
	mux.HandleFunc("DELETE /v1/built-images/{name}/{version}", a.deleteBuiltImage)
	mux.HandleFunc("POST /v1/built-images/{name}/{version}/push", a.pushBuiltImage)
	mux.HandleFunc("GET /v1/built-images/{name}/{version}/spec", a.builtImageSpec)
	mux.HandleFunc("POST /v1/built-images/import", a.importBuiltImage)
	mux.HandleFunc("GET /v1/workloads/{id}/fs", a.workloadFiles)
	mux.HandleFunc("GET /v1/workloads/{id}/fs/file", a.workloadFileGet)
	mux.HandleFunc("PUT /v1/workloads/{id}/fs/file", a.workloadFilePut)
	mux.HandleFunc("PUT /v1/workloads/{id}/fs/batch", a.workloadFileBatch)
	mux.HandleFunc("DELETE /v1/workloads/{id}/fs/file", a.workloadFileDelete)
	mux.HandleFunc("GET /v1/settings", a.getSettings)
	mux.HandleFunc("PUT /v1/settings/name", a.setInstanceName)
	mux.HandleFunc("PUT /v1/settings/registry", a.setRegistry)
	mux.HandleFunc("POST /v1/system/backup", a.backupState)
	mux.HandleFunc("PUT /v1/settings/backup", a.setBackupTarget)
	mux.HandleFunc("PUT /v1/settings/webhook", a.setWebhook)
	mux.HandleFunc("PUT /v1/settings/lru", a.setLRU)
	mux.HandleFunc("PUT /v1/settings/metrics", a.setMetrics)
	mux.HandleFunc("GET /v1/metrics", a.metrics)
	mux.HandleFunc("GET /v1/events", a.events)
	// on-demand TLS gate for Caddy: only mint certs for hosts we actually serve.
	mux.HandleFunc("GET /internal/tls-allow", a.tlsAllow)
	return a.auth(mux)
}

// tlsAllow answers Caddy's on-demand-TLS "ask": 200 => issue a cert for this
// host, anything else => refuse. We allow admin/api on the base domain and every
// alt domain, plus any host in the route table, so random subdomains cannot
// trigger certificate issuance.
func (a *API) tlsAllow(w http.ResponseWriter, r *http.Request) {
	domain := strings.ToLower(r.URL.Query().Get("domain"))
	for _, base := range append([]string{a.cfg.BaseDomain}, a.cfg.AltDomains...) {
		base = strings.ToLower(base)
		if domain == "admin."+base || domain == "api."+base {
			w.WriteHeader(http.StatusOK)
			return
		}
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
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Expose-Headers", "X-Total-Count")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.URL.Path == "/healthz" || strings.HasPrefix(r.URL.Path, "/internal/") || a.apiKey == "" {
			next.ServeHTTP(w, r)
			return
		}
		presented := presentedKey(r)
		role, ok := a.resolveKey(presented)
		if !ok {
			writeErr(w, http.StatusUnauthorized, errors.New("missing or invalid API key"))
			return
		}
		if a.limiter != nil && !a.limiter.allow(presented) {
			w.Header().Set("Retry-After", "1")
			writeErr(w, http.StatusTooManyRequests, errors.New("rate limit exceeded"))
			return
		}
		if role == "readonly" && !isReadMethod(r.Method) {
			writeErr(w, http.StatusForbidden, errors.New("read-only key: this action requires an admin key"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func presentedKey(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	return r.Header.Get("X-API-Key")
}

// resolveKey returns the role for a presented key: the bootstrap file key is
// always admin; otherwise a stored key's role, if it matches.
func (a *API) resolveKey(presented string) (string, bool) {
	if presented == "" {
		return "", false
	}
	if subtle.ConstantTimeCompare([]byte(presented), []byte(a.apiKey)) == 1 {
		return "admin", true
	}
	return a.mgr.ValidateStoredKey(presented)
}

func isReadMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

func (a *API) listPresets(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, presetNames())
}

func presetNames() []string {
	names := make([]string, 0, len(manager.Catalog))
	for k := range manager.Catalog {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
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
	Env      map[string]string    `json:"env"`
}

func (r workloadSpecReq) toSpec() manager.WorkloadSpec {
	return manager.WorkloadSpec{
		Preset: r.Preset, Type: r.Type, Name: r.Name, Image: r.Image, Port: r.Port,
		MemoryMB: r.MemoryMB, CPUs: r.CPUs, Env: r.Env,
	}
}

type workloadView struct {
	*store.Workload
	Routes    []string `json:"routes"`              // hostnames
	Endpoints []string `json:"endpoints"`           // subdomain endpoints (local dev)
	Subroutes []string `json:"subroutes"`           // public subroute endpoints (<PublicURL>/w/<key>)
	LastSeen  string   `json:"last_seen,omitempty"` // last request served, RFC3339
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
	// Port-addressing mode: the workload is reached directly at its stable port,
	// not through the gateway. That direct URL is the primary endpoint.
	if w.HostPort > 0 {
		endpoints = append(endpoints, fmt.Sprintf("http://localhost:%d", w.HostPort))
	}
	for _, r := range routes {
		hosts = append(hosts, r.Host)
		if w.HostPort == 0 {
			// Shared with env interpolation (manager.EndpointForHost) so what
			// the admin shows and what sibling workloads dial agree.
			endpoints = append(endpoints, a.mgr.EndpointForHost(r.Host))
		}
		if a.cfg.PublicURL != "" {
			subroutes = append(subroutes, strings.TrimRight(a.cfg.PublicURL, "/")+"/w/"+r.Key)
		}
	}
	v := workloadView{Workload: w, Routes: hosts, Endpoints: endpoints, Subroutes: subroutes}
	if t, ok := a.mgr.LastSeen(w.ID); ok {
		v.LastSeen = t.UTC().Format(time.RFC3339)
	}
	return v
}

func (a *API) projectView(p *store.Project) projectView {
	// Never nil: a project with no workloads (mid-create, or one whose workloads
	// were deleted) must still serialize `"workloads": []`. A JSON null here
	// crashes every consumer that maps over the field, the admin panel included.
	wl := []workloadView{}
	for _, w := range a.mgr.Store().ListWorkloads(p.ID) {
		wl = append(wl, a.workloadView(w))
	}
	return projectView{Project: p, Workloads: wl}
}

// ---- handlers ----

// createNameLocks serializes creates per project name (see createProject).
var createNameLocks sync.Map

func (a *API) createProject(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name      string            `json:"name"`
		Region    string            `json:"region"`
		Template  string            `json:"template"`            // create from a registered template
		Image     string            `json:"image,omitempty"`     // create from a frozen image ("template@version")
		Delta     map[string]string `json:"delta,omitempty"`     // text files to overlay on the base (path -> content)
		DeltaB64  map[string]string `json:"delta_b64,omitempty"` // binary files to overlay (path -> base64 bytes)
		Deleted   []string          `json:"deleted,omitempty"`   // base paths to remove
		Workloads []workloadSpecReq `json:"workloads"`
	}
	if r.ContentLength > 0 {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	// Named projects are unique, enforced here because this is the only create
	// entry point. Callers use the name as their own foreign key (one project
	// per app), and clients running on several instances race their
	// check-then-create: both list, neither sees the other, and the app ends up
	// split-brained across twin projects — half its hostnames serving one
	// workspace, half the other. That race shipped three separate incidents
	// before this guard. The per-name lock is held across the whole create so
	// two concurrent requests serialize; the loser gets a 409 naming the
	// winner, which is exactly what an adopting client needs.
	if body.Name != "" {
		lk, _ := createNameLocks.LoadOrStore(body.Name, &sync.Mutex{})
		lk.(*sync.Mutex).Lock()
		defer lk.(*sync.Mutex).Unlock()
		for _, p := range a.mgr.Store().ListProjects() {
			if p.Name == body.Name {
				writeErr(w, http.StatusConflict, fmt.Errorf("project name %q already exists as %s", body.Name, p.ID))
				return
			}
		}
	}
	delta, err := mergeDelta(body.Delta, body.DeltaB64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	var proj *store.Project
	if body.Image != "" {
		tmpl, ver, ok := strings.Cut(body.Image, "@")
		if !ok {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("image must be \"template@version\""))
			return
		}
		proj, _, err = a.mgr.CreateFromImage(r.Context(), tmpl, ver, body.Name, body.Region, delta, body.Deleted)
	} else if body.Template != "" {
		proj, _, err = a.mgr.CreateFromTemplate(r.Context(), body.Template, body.Name, body.Region, delta, body.Deleted)
	} else {
		specs := make([]manager.WorkloadSpec, 0, len(body.Workloads))
		for _, ws := range body.Workloads {
			specs = append(specs, ws.toSpec())
		}
		proj, _, err = a.mgr.CreateProject(r.Context(), body.Name, body.Region, specs)
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, a.projectView(proj))
}

// mergeDelta combines the text (`delta`) and base64 (`delta_b64`) overlay maps
// into raw bytes keyed by path. Base64 entries win on a path collision.
func mergeDelta(text, b64 map[string]string) (map[string][]byte, error) {
	if len(text) == 0 && len(b64) == 0 {
		return nil, nil
	}
	out := make(map[string][]byte, len(text)+len(b64))
	for p, c := range text {
		out[p] = []byte(c)
	}
	for p, c := range b64 {
		raw, err := base64.StdEncoding.DecodeString(c)
		if err != nil {
			return nil, fmt.Errorf("delta_b64[%q]: invalid base64: %w", p, err)
		}
		out[p] = raw
	}
	return out, nil
}

// listProjects returns projects, optionally filtered/sorted/paginated. With no
// query params it returns every project (unchanged; the orphan-scan path relies
// on it). `q` filters by id/name substring; `sort=recent` orders by last-active
// (the LRU clock, falling back to created); `offset`/`limit` page the result.
// The pre-pagination total is returned in the X-Total-Count header.
func (a *API) listProjects(w http.ResponseWriter, r *http.Request) {
	list := a.mgr.Store().ListProjects()

	if q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q"))); q != "" {
		kept := make([]*store.Project, 0, len(list))
		for _, p := range list {
			if strings.Contains(strings.ToLower(p.ID), q) || strings.Contains(strings.ToLower(p.Name), q) {
				kept = append(kept, p)
			}
		}
		list = kept
	}

	if r.URL.Query().Get("sort") == "recent" {
		sort.SliceStable(list, func(i, j int) bool {
			return projectActiveKey(list[i]).After(projectActiveKey(list[j]))
		})
	}

	w.Header().Set("X-Total-Count", strconv.Itoa(len(list)))

	offset := atoiClamp(r.URL.Query().Get("offset"), 0, 0, len(list))
	list = list[offset:]
	if limit := atoiClamp(r.URL.Query().Get("limit"), 0, 0, len(list)); limit > 0 {
		list = list[:limit]
	}

	views := make([]projectView, 0, len(list))
	for _, p := range list {
		views = append(views, a.projectView(p))
	}
	writeJSON(w, http.StatusOK, views)
}

// projectActiveKey is the recency key: LastActiveAt when set, else CreatedAt.
func projectActiveKey(p *store.Project) time.Time {
	if !p.LastActiveAt.IsZero() {
		return p.LastActiveAt
	}
	return p.CreatedAt
}

// atoiClamp parses s (def on empty/invalid) and clamps it to [lo, hi].
func atoiClamp(s string, def, lo, hi int) int {
	n := def
	if v, err := strconv.Atoi(s); err == nil {
		n = v
	}
	if n < lo {
		n = lo
	}
	if n > hi {
		n = hi
	}
	return n
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

// lifecycle handlers. ok is a small helper for {"status": "..."}.
func lifecycle(w http.ResponseWriter, a *API, err error, status string) {
	if err != nil {
		a.writeLookupErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": status})
}

func (a *API) workloadStart(w http.ResponseWriter, r *http.Request) {
	lifecycle(w, a, a.mgr.StartWorkload(r.Context(), r.PathValue("id")), "started")
}
func (a *API) workloadStop(w http.ResponseWriter, r *http.Request) {
	lifecycle(w, a, a.mgr.StopWorkload(r.Context(), r.PathValue("id")), "stopped")
}
func (a *API) workloadRestart(w http.ResponseWriter, r *http.Request) {
	lifecycle(w, a, a.mgr.RestartWorkload(r.Context(), r.PathValue("id")), "restarted")
}
func (a *API) projectStart(w http.ResponseWriter, r *http.Request) {
	lifecycle(w, a, a.mgr.StartProject(r.Context(), r.PathValue("id")), "started")
}

// resetBody is the optional payload of a reset: a fresh delta to overlay on
// the pristine base, so a caller can reset straight to a new state in one call.
func resetBody(r *http.Request) (map[string][]byte, []string, error) {
	var body struct {
		Delta    map[string]string `json:"delta,omitempty"`
		DeltaB64 map[string]string `json:"delta_b64,omitempty"`
		Deleted  []string          `json:"deleted,omitempty"`
	}
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			return nil, nil, err
		}
	}
	delta, err := mergeDelta(body.Delta, body.DeltaB64)
	return delta, body.Deleted, err
}

// workloadReset re-materializes one workload from its pristine template/image
// (identity — id, keys, port, routes — preserved).
func (a *API) workloadReset(w http.ResponseWriter, r *http.Request) {
	delta, deleted, err := resetBody(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	lifecycle(w, a, a.mgr.ResetWorkload(r.Context(), r.PathValue("id"), delta, deleted), "reset")
}

// projectReset resets every workload of a project to pristine state.
func (a *API) projectReset(w http.ResponseWriter, r *http.Request) {
	delta, deleted, err := resetBody(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	lifecycle(w, a, a.mgr.ResetProject(r.Context(), r.PathValue("id"), delta, deleted), "reset")
}
func (a *API) projectStop(w http.ResponseWriter, r *http.Request) {
	lifecycle(w, a, a.mgr.StopProject(r.Context(), r.PathValue("id")), "stopped")
}
func (a *API) projectRestart(w http.ResponseWriter, r *http.Request) {
	lifecycle(w, a, a.mgr.RestartProject(r.Context(), r.PathValue("id")), "restarted")
}

func (a *API) setProjectEnv(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Env map[string]string `json:"env"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := a.mgr.SetProjectEnv(r.Context(), r.PathValue("id"), body.Env); err != nil {
		a.writeLookupErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"env": body.Env})
}

func (a *API) setWorkloadEnv(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Env map[string]string `json:"env"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := a.mgr.SetWorkloadEnv(r.Context(), r.PathValue("id"), body.Env); err != nil {
		a.writeLookupErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"env": body.Env})
}

func (a *API) setKeepWarm(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := a.mgr.SetKeepWarm(r.Context(), r.PathValue("id"), body.Enabled); err != nil {
		a.writeLookupErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"keep_warm": body.Enabled})
}

func (a *API) workloadStats(w http.ResponseWriter, r *http.Request) {
	s, err := a.mgr.Stats(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusNotFound, err)
			return
		}
		// A stopped/suspended instance has no live stats; report empty, not error.
		writeJSON(w, http.StatusOK, runtime.Stats{})
		return
	}
	writeJSON(w, http.StatusOK, s)
}

func (a *API) workloadLogs(w http.ResponseWriter, r *http.Request) {
	tail := 200
	if v := r.URL.Query().Get("tail"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			tail = n
		}
	}
	logs, err := a.mgr.Logs(r.Context(), r.PathValue("id"), tail)
	if err != nil {
		a.writeLookupErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"logs": logs})
}

func (a *API) info(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"instance_name":      a.mgr.GetInstanceName(),
		"region":             a.cfg.Region,
		"driver":             a.cfg.Driver,
		"base_domain":        a.cfg.BaseDomain,
		"public_url":         a.cfg.PublicURL,
		"idle_timeout":       a.cfg.IdleTimeout.String(),
		"image":              a.cfg.Image,
		"rate_limit_per_min": a.cfg.RateLimitPerMin,
		"limits": map[string]any{
			"tinbase_mem_mb": a.cfg.TinbaseMemMB,
			"tinbase_cpus":   a.cfg.TinbaseCPUs,
			"dev_mem_mb":     a.cfg.DevMemMB,
			"dev_cpus":       a.cfg.DevCPUs,
			"pids_limit":     a.cfg.PidsLimit,
		},
		"backups": map[string]any{
			"enabled":  a.mgr.BackupsEnabled(),
			"interval": a.cfg.BackupInterval.String(),
			"retain":   a.cfg.BackupRetain,
		},
		"images_supported": a.mgr.ImagesSupported(),
		"port_addressing":  a.cfg.PortBase > 0,
		"presets":          presetNames(),
	})
}

func (a *API) listBackups(w http.ResponseWriter, r *http.Request) {
	a.writeBackups(w, "")
}

func (a *API) listWorkloadBackups(w http.ResponseWriter, r *http.Request) {
	a.writeBackups(w, r.PathValue("id"))
}

func (a *API) writeBackups(w http.ResponseWriter, workloadID string) {
	bs, err := a.mgr.ListBackups(workloadID)
	if err != nil {
		a.writeBackupErr(w, err)
		return
	}
	if bs == nil {
		bs = []backup.Backup{}
	}
	writeJSON(w, http.StatusOK, bs)
}

func (a *API) createBackup(w http.ResponseWriter, r *http.Request) {
	b, err := a.mgr.BackupWorkload(r.Context(), r.PathValue("id"))
	if err != nil {
		a.writeBackupErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, b)
}

func (a *API) backupProject(w http.ResponseWriter, r *http.Request) {
	bs, err := a.mgr.BackupProject(r.Context(), r.PathValue("id"))
	if err != nil && len(bs) == 0 {
		a.writeBackupErr(w, err)
		return
	}
	if bs == nil {
		bs = []backup.Backup{}
	}
	writeJSON(w, http.StatusCreated, bs)
}

func (a *API) restoreWorkload(w http.ResponseWriter, r *http.Request) {
	var body struct {
		BackupID string `json:"backup_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.BackupID == "" {
		writeErr(w, http.StatusBadRequest, errors.New("backup_id required"))
		return
	}
	if err := a.mgr.RestoreWorkload(r.Context(), r.PathValue("id"), body.BackupID); err != nil {
		a.writeBackupErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "restored"})
}

func (a *API) deleteBackup(w http.ResponseWriter, r *http.Request) {
	if err := a.mgr.DeleteBackup(r.PathValue("id")); err != nil {
		a.writeBackupErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) listKeys(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.mgr.ListAPIKeys())
}

func (a *API) createKey(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
		Role string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	meta, key, err := a.mgr.CreateAPIKey(body.Name, body.Role)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	// Plaintext key is returned exactly once.
	writeJSON(w, http.StatusCreated, map[string]any{"key": key, "meta": meta})
}

func (a *API) deleteKey(w http.ResponseWriter, r *http.Request) {
	if err := a.mgr.DeleteAPIKey(r.PathValue("id")); err != nil {
		a.writeLookupErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) listRegions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.mgr.ListRegions())
}

func (a *API) createRegion(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name       string `json:"name"`
		DockerHost string `json:"docker_host"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	rg, err := a.mgr.CreateRegion(body.Name, body.DockerHost)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, rg)
}

func (a *API) deleteRegion(w http.ResponseWriter, r *http.Request) {
	if err := a.mgr.DeleteRegion(r.PathValue("id")); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusNotFound, err)
			return
		}
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) setDefaultRegion(w http.ResponseWriter, r *http.Request) {
	if err := a.mgr.SetDefaultRegion(r.PathValue("id")); err != nil {
		a.writeLookupErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"default": r.PathValue("id")})
}

// ---- images ----

// writeImageErr maps image-management errors to HTTP status codes.
func (a *API) writeImageErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, manager.ErrImagesUnsupported):
		writeErr(w, http.StatusNotImplemented, err)
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, http.StatusNotFound, err)
	default:
		writeErr(w, http.StatusBadRequest, err)
	}
}

func (a *API) listImages(w http.ResponseWriter, r *http.Request) {
	imgs, err := a.mgr.ListImages(r.Context(), r.URL.Query().Get("region"))
	if err != nil {
		a.writeImageErr(w, err)
		return
	}
	if imgs == nil {
		imgs = []runtime.ImageInfo{}
	}
	writeJSON(w, http.StatusOK, imgs)
}

func (a *API) pullImage(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Ref    string `json:"ref"`
		Region string `json:"region"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(body.Ref) == "" {
		writeErr(w, http.StatusBadRequest, errors.New("ref required"))
		return
	}
	out, err := a.mgr.PullImage(r.Context(), body.Region, strings.TrimSpace(body.Ref))
	if err != nil {
		a.writeImageErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"ref": body.Ref, "output": out})
}

func (a *API) removeImage(w http.ResponseWriter, r *http.Request) {
	ref := r.URL.Query().Get("ref")
	if ref == "" {
		writeErr(w, http.StatusBadRequest, errors.New("ref query param required"))
		return
	}
	force := r.URL.Query().Get("force") == "true"
	if err := a.mgr.RemoveImage(r.Context(), r.URL.Query().Get("region"), ref, force); err != nil {
		a.writeImageErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) getSettings(w http.ResponseWriter, r *http.Request) {
	t := a.mgr.GetBackupTarget()
	secretSet := t.SecretKey != ""
	t.SecretKey = "" // never return the secret
	wh := a.mgr.GetWebhookConfig()
	webhookKeySet := wh.APIKey != ""
	writeJSON(w, http.StatusOK, map[string]any{
		"instance_name":     a.mgr.GetInstanceName(),
		"backup":            t,
		"backup_secret_set": secretSet,
		"webhook":           map[string]string{"url": wh.URL},
		"webhook_key_set":   webhookKeySet,
		"metrics":           a.mgr.GetMetricsTarget(),
		"registry":          a.mgr.GetRegistry(),
		"lru_keep_max":      a.mgr.GetLRUKeepMax(),
	})
}

func (a *API) listTemplates(w http.ResponseWriter, r *http.Request) {
	t := a.mgr.GetTemplates()
	if t == nil {
		t = map[string]string{}
	}
	writeJSON(w, http.StatusOK, t)
}

func (a *API) setTemplate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(body.Name) == "" {
		writeErr(w, http.StatusBadRequest, errors.New("name required"))
		return
	}
	if err := a.mgr.SetTemplate(body.Name, body.Path); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, a.mgr.GetTemplates())
}

func (a *API) getTemplate(w http.ResponseWriter, r *http.Request) {
	man, err := a.mgr.TemplateManifest(r.PathValue("name"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, man)
}

func (a *API) templateFiles(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if path := r.URL.Query().Get("path"); path != "" {
		b, err := a.mgr.TemplateFile(name, path)
		if err != nil {
			writeErr(w, http.StatusNotFound, err)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write(b)
		return
	}
	files, err := a.mgr.TemplateFiles(name)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, files)
}

func (a *API) templateBundle(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`.tar.gz"`)
	if err := a.mgr.TemplateBundle(name, w); err != nil {
		// Headers may already be sent; log-level only.
		return
	}
}

// imageBundle streams a built image's base tarball (base.tar.gz) — the frozen
// source tree, e.g. to hydrate a client VFS from the base.
func (a *API) imageBundle(w http.ResponseWriter, r *http.Request) {
	name, version := r.PathValue("name"), r.PathValue("version")
	path, err := a.mgr.ImageTarball(name, version)
	if err != nil {
		a.writeLookupErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`-`+version+`.tar.gz"`)
	http.ServeFile(w, r, path)
}

// buildImage freezes the named template into a new, auto-versioned image.
func (a *API) buildImage(w http.ResponseWriter, r *http.Request) {
	im, err := a.mgr.BuildImage(r.Context(), r.PathValue("name"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, im)
}

// listBuiltImages returns all frozen template images.
func (a *API) listBuiltImages(w http.ResponseWriter, r *http.Request) {
	list := a.mgr.BuiltImages()
	if list == nil {
		list = []*store.Image{}
	}
	writeJSON(w, http.StatusOK, list)
}

// deleteBuiltImage removes one frozen image (tarball + record).
func (a *API) deleteBuiltImage(w http.ResponseWriter, r *http.Request) {
	if err := a.mgr.DeleteImage(r.PathValue("name"), r.PathValue("version")); err != nil {
		a.writeLookupErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// pushBuiltImage re-tags an image's docker images under the configured registry
// prefix and pushes them, so another instance can pull and run them.
func (a *API) pushBuiltImage(w http.ResponseWriter, r *http.Request) {
	pushed, err := a.mgr.PushImage(r.Context(), r.PathValue("name"), r.PathValue("version"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"registry": pushed})
}

// builtImageSpec returns the self-contained import descriptor (registry refs +
// workload shape) an operator copies to a target ORCHD instance.
func (a *API) builtImageSpec(w http.ResponseWriter, r *http.Request) {
	spec, err := a.mgr.ImageImportSpec(r.PathValue("name"), r.PathValue("version"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, spec)
}

// importBuiltImage records an image described by an ImportSpec (from another
// instance), so projects here can boot from it (docker driver pulls the refs).
func (a *API) importBuiltImage(w http.ResponseWriter, r *http.Request) {
	var spec manager.ImportSpec
	if err := json.NewDecoder(r.Body).Decode(&spec); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	im, err := a.mgr.ImportImage(spec)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, im)
}

func (a *API) workloadFiles(w http.ResponseWriter, r *http.Request) {
	files, err := a.mgr.WorkloadFiles(r.PathValue("id"))
	if err != nil {
		a.writeLookupErr(w, err)
		return
	}
	if files == nil {
		files = []string{}
	}
	writeJSON(w, http.StatusOK, files)
}

func (a *API) workloadFileGet(w http.ResponseWriter, r *http.Request) {
	b, err := a.mgr.WorkloadFile(r.PathValue("id"), r.URL.Query().Get("path"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write(b)
}

func (a *API) workloadFilePut(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		writeErr(w, http.StatusBadRequest, errors.New("path query param required"))
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 16<<20)) // 16 MiB cap
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := a.mgr.WriteWorkloadFile(r.PathValue("id"), path, body); err != nil {
		a.writeLookupErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": path, "bytes": len(body)})
}

// workloadFileBatch applies many file writes/deletes in one call — the file
// sync hot path for callers pushing a burst of changes (an AI edit touching
// several files). Applied sequentially; the first failure aborts with the
// failing path, and the caller retries the whole batch (writes are idempotent).
func (a *API) workloadFileBatch(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Files []struct {
			Path       string `json:"path"`
			Content    string `json:"content"`
			ContentB64 string `json:"content_b64"`
			Delete     bool   `json:"delete"`
		} `json:"files"`
	}
	// Same cap as single-file puts, over the whole batch.
	if err := json.NewDecoder(io.LimitReader(r.Body, 16<<20)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if len(body.Files) == 0 {
		writeErr(w, http.StatusBadRequest, errors.New("files required"))
		return
	}
	ops := make([]manager.FileOp, 0, len(body.Files))
	for _, f := range body.Files {
		if f.Path == "" {
			writeErr(w, http.StatusBadRequest, errors.New("every file needs a path"))
			return
		}
		op := manager.FileOp{Path: f.Path, Delete: f.Delete}
		if !f.Delete {
			if f.ContentB64 != "" {
				raw, err := base64.StdEncoding.DecodeString(f.ContentB64)
				if err != nil {
					writeErr(w, http.StatusBadRequest, fmt.Errorf("files[%q]: invalid base64: %w", f.Path, err))
					return
				}
				op.Content = raw
			} else {
				op.Content = []byte(f.Content)
			}
		}
		ops = append(ops, op)
	}
	written, deleted, err := a.mgr.ApplyWorkloadFiles(r.PathValue("id"), ops)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			a.writeLookupErr(w, err)
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"written": written, "deleted": deleted})
}

func (a *API) workloadFileDelete(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		writeErr(w, http.StatusBadRequest, errors.New("path query param required"))
		return
	}
	if err := a.mgr.DeleteWorkloadFile(r.PathValue("id"), path); err != nil {
		a.writeLookupErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) setInstanceName(w http.ResponseWriter, r *http.Request) {
	var body struct {
		InstanceName string `json:"instance_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := a.mgr.SetInstanceName(body.InstanceName); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"instance_name": a.mgr.GetInstanceName()})
}

func (a *API) setRegistry(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Registry string `json:"registry"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := a.mgr.SetRegistry(body.Registry); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"registry": a.mgr.GetRegistry()})
}

func (a *API) backupState(w http.ResponseWriter, r *http.Request) {
	b, err := a.mgr.BackupState(r.Context())
	if err != nil {
		a.writeBackupErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, b)
}

func (a *API) setMetrics(w http.ResponseWriter, r *http.Request) {
	var t store.MetricsTarget
	if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := a.mgr.SetMetricsTarget(t); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (a *API) metrics(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.mgr.Snapshot())
}

func (a *API) setWebhook(w http.ResponseWriter, r *http.Request) {
	var body struct {
		URL    string `json:"url"`
		APIKey string `json:"api_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	// Blank api_key keeps the existing one, so the admin can edit the URL
	// without re-entering the secret (mirrors the backup secret behaviour).
	if body.APIKey == "" {
		body.APIKey = a.mgr.GetWebhookConfig().APIKey
	}
	if err := a.mgr.SetWebhook(body.URL, body.APIKey); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"url": body.URL, "webhook_key_set": body.APIKey != ""})
}

func (a *API) setLRU(w http.ResponseWriter, r *http.Request) {
	var body struct {
		KeepMax int `json:"keep_max"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := a.mgr.SetLRUKeepMax(body.KeepMax); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"lru_keep_max": a.mgr.GetLRUKeepMax()})
}

func (a *API) events(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	writeJSON(w, http.StatusOK, a.mgr.Events(limit))
}

func (a *API) setBackupTarget(w http.ResponseWriter, r *http.Request) {
	var t store.BackupTarget
	if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	// If the secret is left blank on an s3 target, keep the existing one (so the
	// admin can edit other fields without re-entering it).
	if t.Type == "s3" && t.SecretKey == "" {
		if cur := a.mgr.GetBackupTarget(); cur.Type == "s3" {
			t.SecretKey = cur.SecretKey
		}
	}
	if err := a.mgr.SetBackupTarget(t); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	t.SecretKey = ""
	writeJSON(w, http.StatusOK, map[string]any{"backup": t})
}

func (a *API) writeBackupErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, manager.ErrBackupsDisabled):
		writeErr(w, http.StatusConflict, err)
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, http.StatusNotFound, err)
	default:
		writeErr(w, http.StatusInternalServerError, err)
	}
}

func (a *API) removeRoute(w http.ResponseWriter, r *http.Request) {
	host := r.URL.Query().Get("host")
	if host == "" {
		writeErr(w, http.StatusBadRequest, errors.New("host query param required"))
		return
	}
	if err := a.mgr.RemoveRoute(host); err != nil {
		a.writeLookupErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
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
