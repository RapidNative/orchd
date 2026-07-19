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
	"sort"
	"strconv"
	"strings"
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
	mux.HandleFunc("GET /v1/settings", a.getSettings)
	mux.HandleFunc("PUT /v1/settings/name", a.setInstanceName)
	mux.HandleFunc("POST /v1/system/backup", a.backupState)
	mux.HandleFunc("PUT /v1/settings/backup", a.setBackupTarget)
	mux.HandleFunc("PUT /v1/settings/webhook", a.setWebhook)
	mux.HandleFunc("PUT /v1/settings/metrics", a.setMetrics)
	mux.HandleFunc("GET /v1/metrics", a.metrics)
	mux.HandleFunc("GET /v1/events", a.events)
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
}

func (r workloadSpecReq) toSpec() manager.WorkloadSpec {
	return manager.WorkloadSpec{
		Preset: r.Preset, Type: r.Type, Name: r.Name, Image: r.Image, Port: r.Port,
		MemoryMB: r.MemoryMB, CPUs: r.CPUs,
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
	v := workloadView{Workload: w, Routes: hosts, Endpoints: endpoints, Subroutes: subroutes}
	if t, ok := a.mgr.LastSeen(w.ID); ok {
		v.LastSeen = t.UTC().Format(time.RFC3339)
	}
	return v
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
		Region    string            `json:"region"`
		Workloads []workloadSpecReq `json:"workloads"`
	}
	if r.ContentLength > 0 {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	specs := make([]manager.WorkloadSpec, 0, len(body.Workloads))
	for _, ws := range body.Workloads {
		specs = append(specs, ws.toSpec())
	}
	proj, _, err := a.mgr.CreateProject(r.Context(), body.Name, body.Region, specs)
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
	writeJSON(w, http.StatusOK, map[string]any{
		"instance_name":     a.mgr.GetInstanceName(),
		"backup":            t,
		"backup_secret_set": secretSet,
		"webhook":           map[string]string{"url": a.mgr.GetWebhook()},
		"metrics":           a.mgr.GetMetricsTarget(),
	})
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
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := a.mgr.SetWebhook(body.URL); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"url": body.URL})
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
func gatewayPort(gatewayAddr string) string {
	for i := len(gatewayAddr) - 1; i >= 0; i-- {
		if gatewayAddr[i] == ':' {
			return gatewayAddr[i:]
		}
	}
	return ""
}
