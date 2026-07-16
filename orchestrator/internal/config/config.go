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

	// TinbaseBin is the tinbase executable the LocalDriver spawns.
	TinbaseBin string
	// Engine selects the tinbase engine (empty = tinbase default).
	Engine string

	// IdleTimeout is how long an instance may sit without a request before the
	// orchestrator scales it to zero (suspends it).
	IdleTimeout time.Duration
	// Region is the single region this orchestrator serves for now.
	Region string
}

func Load() Config {
	home, _ := os.UserHomeDir()
	return Config{
		APIAddr:     env("ORCHD_API_ADDR", "127.0.0.1:8080"),
		GatewayAddr: env("ORCHD_GATEWAY_ADDR", "127.0.0.1:8081"),
		BaseDomain:  env("ORCHD_BASE_DOMAIN", "lvh.me"),
		DataRoot:    env("ORCHD_DATA_ROOT", filepath.Join(home, ".tinbase-cloud")),
		TinbaseBin:  env("ORCHD_TINBASE_BIN", "tinbase"),
		Engine:      env("ORCHD_ENGINE", ""),
		IdleTimeout: envDuration("ORCHD_IDLE_TIMEOUT", 5*time.Minute),
		Region:      env("ORCHD_REGION", "local"),
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
