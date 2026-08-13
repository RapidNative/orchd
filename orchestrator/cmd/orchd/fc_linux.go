//go:build linux

package main

import (
	"log"

	"github.com/tinbase/tinbase-cloud/orchestrator/internal/config"
	"github.com/tinbase/tinbase-cloud/orchestrator/internal/runtime"
)

// wrapFC layers the firecracker Mux over the default driver when the pilot
// allowlist (ORCHD_FC_WORKLOADS) is set. Any construction failure falls back
// to the default runtime — the microVM path must never take the box down.
func wrapFC(cfg config.Config, def runtime.Runtime) runtime.Runtime {
	if cfg.FCWorkloads == "" {
		return def
	}
	fc, err := runtime.NewFirecrackerDriver(runtime.FirecrackerConfig{
		Bin:    cfg.FCBin,
		Kernel: cfg.FCKernel,
		Root:   cfg.FCRoot,
		Pool:   cfg.FCPool,
	})
	if err != nil {
		log.Printf("firecracker disabled (%v); workloads stay on %s", err, def.Name())
		return def
	}
	log.Printf("firecracker pilot: %s", cfg.FCWorkloads)
	return runtime.NewMux(def, fc, cfg.FCWorkloads)
}
