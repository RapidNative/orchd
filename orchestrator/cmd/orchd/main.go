// Command orchd is the tinbase-cloud orchestrator daemon. It runs two servers:
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

	st, err := store.Open(filepath.Join(cfg.DataRoot, "state", "projects.json"))
	if err != nil {
		log.Fatalf("open store: %v", err)
	}

	var rt runtime.Runtime
	switch cfg.Driver {
	case "docker":
		d := runtime.NewDockerDriver(cfg.Image, cfg.DockerRuntime)
		d.DockerHost = cfg.DockerHost
		rt = d
	default:
		rt = runtime.NewLocalDriver(cfg.TinbaseBin, cfg.Engine)
	}
	mgr := manager.New(cfg, st, rt)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go mgr.RunReaper(ctx)

	// Control plane API.
	apiSrv := &http.Server{Addr: cfg.APIAddr, Handler: api.New(mgr, cfg).Handler()}
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

	log.Printf("driver=%s region=%s base-domain=%s idle-timeout=%s data-root=%s tinbase=%s",
		rt.Name(), cfg.Region, cfg.BaseDomain, cfg.IdleTimeout, cfg.DataRoot, cfg.TinbaseBin)

	// Data plane gateway (blocks until shutdown).
	gw := gateway.New(mgr, cfg.BaseDomain)
	if err := gw.Serve(ctx, cfg.GatewayAddr); err != nil {
		log.Fatalf("gateway: %v", err)
	}
	_ = os.Stdout.Sync()
}
