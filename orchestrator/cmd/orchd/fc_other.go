//go:build !linux

package main

import (
	"github.com/tinbase/tinbase-cloud/orchestrator/internal/config"
	"github.com/tinbase/tinbase-cloud/orchestrator/internal/runtime"
)

// wrapFC is a no-op off Linux; the firecracker runtime is linux-only.
func wrapFC(_ config.Config, def runtime.Runtime) runtime.Runtime { return def }
