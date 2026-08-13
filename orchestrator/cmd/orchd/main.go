// Command orchd is the ORCHD orchestration control-plane daemon. It runs two servers:
//
//	API      (control plane) — provision and manage projects
//	Gateway  (data plane)     — route <ref>.<base-domain> to a project, waking it
//
// v0 drives the LocalDriver (tinbase as OS processes) so the whole control loop
// runs on macOS. The FirecrackerDriver will slot in behind the same interface on
// Linux bare metal without touching anything above the runtime package.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/tinbase/tinbase-cloud/orchestrator/internal/api"
	"github.com/tinbase/tinbase-cloud/orchestrator/internal/config"
	"github.com/tinbase/tinbase-cloud/orchestrator/internal/gateway"
	"github.com/tinbase/tinbase-cloud/orchestrator/internal/manager"
	"github.com/tinbase/tinbase-cloud/orchestrator/internal/runtime"
	"github.com/tinbase/tinbase-cloud/orchestrator/internal/store"
)

func main() {
	log.SetFlags(log.Ltime)
	cfg := config.Load()

	var st store.Store
	var err error
	switch {
	case cfg.StateDSN != "":
		st, err = store.OpenPostgres(cfg.StateDSN)
		log.Printf("state store: postgres")
	case cfg.StateSQLite != "":
		st, err = store.OpenSQLite(cfg.StateSQLite)
		log.Printf("state store: sqlite (%s)", cfg.StateSQLite)
	default:
		st, err = store.Open(filepath.Join(cfg.DataRoot, "state", "projects.json"))
		log.Printf("state store: json file")
	}
	if err != nil {
		log.Fatalf("open store: %v", err)
	}

	var rt runtime.Runtime
	switch cfg.Driver {
	case "docker":
		d := runtime.NewDockerDriver(cfg.Image, cfg.DockerRuntime)
		d.DockerHost = cfg.DockerHost
		rt = d
	case "mock":
		// In-memory driver: runs the full control plane (incl. image management)
		// without Docker. For local dev and browser E2E.
		rt = runtime.NewMockDriver()
	default:
		rt = runtime.NewLocalDriver(cfg.TinbaseBin, cfg.Engine)
	}
	rt = wrapFC(cfg, rt)
	mgr := manager.New(cfg, st, rt)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go mgr.RunReaper(ctx)
	go mgr.RunBackupScheduler(ctx)
	go mgr.RunMetricsPublisher(ctx)
	go mgr.WakeKeepWarm(ctx)

	// Load the control-plane API key (if configured). Never logged.
	apiKey := loadAPIKey(cfg.APIKeyFile)
	authState := "OPEN (no key)"
	if apiKey != "" {
		authState = "key required"
	}

	// Control plane API.
	apiSrv := &http.Server{Addr: cfg.APIAddr, Handler: api.New(mgr, cfg, apiKey).Handler()}
	go func() {
		<-ctx.Done()
		_ = apiSrv.Close()
	}()
	go func() {
		log.Printf("api listening on %s", cfg.APIAddr)
		if err := apiSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("api server: %v", err)
		}
	}()

	log.Printf("driver=%s region=%s base-domain=%s idle-timeout=%s data-root=%s api-auth=%s",
		rt.Name(), cfg.Region, cfg.BaseDomain, cfg.IdleTimeout, cfg.DataRoot, authState)

	// Data plane gateway (blocks until shutdown).
	gw := gateway.New(mgr)
	if err := gw.Serve(ctx, cfg.GatewayAddr); err != nil {
		log.Fatalf("gateway: %v", err)
	}
	_ = os.Stdout.Sync()
}

// loadAPIKey reads the API key from a file, trimming whitespace. Missing file or
// empty path means no key (open, local dev).
func loadAPIKey(path string) string {
	if path == "" {
		return ""
	}
	b, err := os.ReadFile(path)
	if err != nil {
		log.Printf("api key file %s not readable (%v); API is OPEN", path, err)
		return ""
	}
	return strings.TrimSpace(string(b))
}
