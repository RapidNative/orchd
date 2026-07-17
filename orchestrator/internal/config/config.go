// Package config holds orchestrator runtime configuration, sourced from
// environment variables with sensible local-development defaults.
package config

import (
	"os"
	"path/filepath"
	"strconv"
	"time"
)

type Config struct {
	// APIAddr is where the control plane / platform API listens.
	APIAddr string
	// GatewayAddr is where the tenant-facing router listens. Requests arrive as
	// <ref>.<BaseDomain> and are proxied to the project's instance.
	GatewayAddr string
	// BaseDomain is the suffix stripped to extract a project ref from the Host
	// header. lvh.me resolves *.lvh.me to 127.0.0.1, ideal for local dev.
	BaseDomain string

	// DataRoot is where per-project data dirs and the control plane state file
	// live.
	DataRoot string

	// Driver selects the runtime substrate: "local" (tinbase as OS processes,
	// for macOS dev) or "docker" (containers, optionally gVisor-isolated).
	Driver string

	// TinbaseBin is the tinbase executable the LocalDriver spawns.
	TinbaseBin string
	// Engine selects the tinbase engine (empty = tinbase default).
	Engine string

	// Image is the container image the DockerDriver runs.
	Image string
	// DockerRuntime is the Docker runtime for tenant containers. "runsc" selects
	// gVisor isolation; empty uses runc.
	DockerRuntime string
	// DockerHost points the docker CLI at a remote daemon; empty uses local.
	DockerHost string

	// IdleTimeout is how long an instance may sit without a request before the
	// orchestrator scales it to zero (suspends it).
	IdleTimeout time.Duration
	// Region is the single region this orchestrator serves for now.
	Region string

	// APIKeyFile is a path to a file holding the control-plane API key. When set
	// and non-empty, mutating control endpoints require it. Empty = open (local
	// dev). The key never lives in config or logs, only in this file.
	APIKeyFile string
	// PublicURL is the externally reachable base (e.g. https://cloud.rapidnative.com)
	// used to build subroute endpoints (<PublicURL>/w/<key>) for display.
	PublicURL string
}

func Load() Config {
	home, _ := os.UserHomeDir()
	return Config{
		APIAddr:       env("ORCHD_API_ADDR", "127.0.0.1:8080"),
		GatewayAddr:   env("ORCHD_GATEWAY_ADDR", "127.0.0.1:8081"),
		BaseDomain:    env("ORCHD_BASE_DOMAIN", "lvh.me"),
		DataRoot:      env("ORCHD_DATA_ROOT", filepath.Join(home, ".tinbase-cloud")),
		Driver:        env("ORCHD_DRIVER", "local"),
		TinbaseBin:    env("ORCHD_TINBASE_BIN", "tinbase"),
		Engine:        env("ORCHD_ENGINE", ""),
		Image:         env("ORCHD_IMAGE", "tinbase:0.10.0"),
		DockerRuntime: env("ORCHD_DOCKER_RUNTIME", "runsc"),
		DockerHost:    env("ORCHD_DOCKER_HOST", ""),
		IdleTimeout:   envDuration("ORCHD_IDLE_TIMEOUT", 5*time.Minute),
		Region:        env("ORCHD_REGION", "local"),
		APIKeyFile:    env("ORCHD_API_KEY_FILE", ""),
		PublicURL:     env("ORCHD_PUBLIC_URL", ""),
	}
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
		if secs, err := strconv.Atoi(v); err == nil {
			return time.Duration(secs) * time.Second
		}
	}
	return def
}
