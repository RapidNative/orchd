// Package api is the platform / service-level control plane API: the surface an
// admin panel or RapidNative will call to provision and manage projects. It is
// deliberately separate from the tenant-facing gateway (different port, different
// audience) and returns plain JSON.
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"

	"github.com/tinbase/tinbase-cloud/orchestrator/internal/config"
	"github.com/tinbase/tinbase-cloud/orchestrator/internal/manager"
	"github.com/tinbase/tinbase-cloud/orchestrator/internal/runtime"
	"github.com/tinbase/tinbase-cloud/orchestrator/internal/store"
)

type API struct {
	mgr *manager.Manager
	cfg config.Config
}

func New(mgr *manager.Manager, cfg config.Config) *API {
	return &API{mgr: mgr, cfg: cfg}
}

func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST /v1/projects", a.createProject)
	mux.HandleFunc("GET /v1/projects", a.listProjects)
	mux.HandleFunc("GET /v1/projects/{ref}", a.getProject)
	mux.HandleFunc("DELETE /v1/projects/{ref}", a.deleteProject)
	return logging(mux)
}

type projectView struct {
	*store.Project
	Endpoint string `json:"endpoint"`
}

func (a *API) view(p *store.Project) projectView {
	return projectView{Project: p, Endpoint: a.endpoint(p.Ref)}
}

func (a *API) endpoint(ref string) string {
	// The address a supabase-js client points at, routed by the gateway.
	return fmt.Sprintf("http://%s.%s", ref, hostOnly(a.cfg.BaseDomain, a.cfg.GatewayAddr))
}

func (a *API) createProject(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Type runtime.WorkloadType `json:"type"`
	}
	if r.ContentLength > 0 {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	p, err := a.mgr.CreateProject(r.Context(), body.Type)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, a.view(p))
}

func (a *API) listProjects(w http.ResponseWriter, r *http.Request) {
	list := a.mgr.Store().List()
	views := make([]projectView, 0, len(list))
	for _, p := range list {
		views = append(views, a.view(p))
	}
	writeJSON(w, http.StatusOK, views)
}

func (a *API) getProject(w http.ResponseWriter, r *http.Request) {
	p, err := a.mgr.Store().Get(r.PathValue("ref"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, a.view(p))
}

func (a *API) deleteProject(w http.ResponseWriter, r *http.Request) {
	if err := a.mgr.DeleteProject(r.Context(), r.PathValue("ref")); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusNotFound, err)
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

func logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("api %s %s", r.Method, r.URL.Path)
		next.ServeHTTP(w, r)
	})
}

// hostOnly derives the host portion clients should use. For local dev the
// gateway listens on a port, so we append it to the base domain.
func hostOnly(baseDomain, gatewayAddr string) string {
	// gatewayAddr is host:port; carry the port through so links are clickable.
	if i := lastColon(gatewayAddr); i >= 0 {
		return baseDomain + gatewayAddr[i:]
	}
	return baseDomain
}

func lastColon(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == ':' {
			return i
		}
	}
	return -1
}
