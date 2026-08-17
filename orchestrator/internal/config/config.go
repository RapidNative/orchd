// Package config holds orchestrator runtime configuration, sourced from
// environment variables with sensible local-development defaults.
package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	// APIAddr is where the control plane / platform API listens.
	APIAddr string
	// GatewayAddr is where the tenant-facing router listens. Requests arrive as
	// <ref>.<BaseDomain> and are proxied to the project's instance.
	GatewayAddr string
	// BaseDomain is the suffix stripped to extract a project ref from the Host
	// header, and the domain new workloads get their default subdomain under.
	// lvh.me resolves *.lvh.me to 127.0.0.1, ideal for local dev.
	BaseDomain string

	// TemplatesDir, when set, is scanned on startup: every subdir containing an
	// orchd.json is auto-registered as a template (by dir name) if not already
	// registered — so bundled example templates are available on a fresh clone.
	TemplatesDir string

	// PortBase, when > 0, enables port-per-workload addressing: each workload is
	// assigned a stable host port counting up from PortBase (8100, 8101, …) and
	// is reachable directly at http://localhost:<port>, no gateway/subdomain
	// needed. 0 = the gateway/subdomain model (prod). Set ORCHD_PORT_BASE.
	PortBase int

	// AltDomains are additional base domains this server also fronts (e.g. a
	// legacy domain kept alive alongside a new primary). admin.<d>/api.<d> for
	// each are permitted through the on-demand-TLS gate; workload hostnames are
	// permitted via the route table regardless. Comma-separated in ORCHD_ALT_DOMAINS.
	AltDomains []string

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
	// FCWorkloads routes template/workspace pairs (comma-separated, e.g.
	// "fullstack-supabase/mobile") to the firecracker runtime; empty disables
	// the microVM path entirely. Linux-only; requires FCBin/FCKernel and a
	// dm-thin pool named FCPool.
	FCWorkloads string
	FCBin       string
	FCKernel    string
	FCRoot      string
	FCPool      string
	// FCMaxLive caps concurrently running microVMs; 0 derives it from host RAM.
	FCMaxLive int

	// DockerRuntime is the Docker runtime for tenant containers. "runsc" selects
	// gVisor isolation; empty uses runc.
	DockerRuntime string
	// DockerHost points the docker CLI at a remote daemon; empty uses local.
	DockerHost string

	// IdleTimeout is how long an instance may sit without a request before the
	// orchestrator scales it to zero (suspends it).
	IdleTimeout time.Duration

	// WorkloadEnv is injected into every workload's environment at the lowest
	// precedence (project env, then per-workload env, override it). The place
	// for platform-wide plumbing like a registry mirror
	// (npm_config_registry=http://...) that every tenant should use without
	// any template knowing about it.
	WorkloadEnv map[string]string
	// BuildEnv is passed to image builds as docker build args, visible to RUN
	// steps (installs) but absent from the built image — runtime behaviour
	// stays governed by WorkloadEnv alone.
	BuildEnv map[string]string
	// HostAliases ("host=ip,host2=ip2") are injected into every instance:
	// /etc/hosts for microVMs, --add-host for containers. For steering a
	// well-known hostname to an on-box service (a cache, a mirror) for guests
	// only — public DNS and guest trust stores stay untouched.
	HostAliases map[string]string
	// Region is the single region this orchestrator serves for now.
	Region string

	// APIKeyFile is a path to a file holding the control-plane API key. When set
	// and non-empty, mutating control endpoints require it. Empty = open (local
	// dev). The key never lives in config or logs, only in this file.
	APIKeyFile string
	// PublicURL is the externally reachable base (e.g. https://cloud.rapidnative.com)
	// used to build subroute endpoints (<PublicURL>/w/<key>) for display.
	PublicURL string
	// PublicScheme is the scheme for subdomain endpoints ("https" in prod behind
	// Caddy, "http" locally). When "https", displayed endpoints omit the port.
	PublicScheme string

	// Per-workload resource caps (cgroups), applied by the DockerDriver. Defaults
	// by workload type; the API/preset can override per workload.
	TinbaseMemMB int
	TinbaseCPUs  float64
	DevMemMB     int
	DevCPUs      float64
	// PidsLimit is a global max-processes cap (fork-bomb backstop) on every
	// container regardless of type.
	PidsLimit int

	// BackupDir is the local backup store root.
	BackupDir string
	// BackupInterval is how often to auto-backup tinbase workloads. 0 disables
	// scheduled backups (on-demand still works).
	BackupInterval time.Duration
	// BackupRetain is how many backups to keep per workload.
	BackupRetain int

	// MetricsInterval is how often the platform metrics snapshot is published.
	MetricsInterval time.Duration

	// StateDSN, when set, stores control-plane state in Postgres instead of the
	// local JSON file (e.g. postgres://user:pass@host/db).
	StateDSN string

	// StateSQLite, when set (and StateDSN is empty), stores control-plane state
	// in a SQLite database at this path (WAL). Recommended over the JSON file for
	// a single-box control plane.
	StateSQLite string

	// RateLimitPerMin caps control-API requests per API key per minute (0 = off).
	RateLimitPerMin int
}

func Load() Config {
	home, _ := os.UserHomeDir()
	gatewayAddr := env("ORCHD_GATEWAY_ADDR", "127.0.0.1:8081")

	// Local mode (ORCHD_LOCAL=1): turnkey, port-based defaults for running the
	// whole stack on localhost with no domain, TLS, or Caddy. Workloads are
	// reached by port through the gateway — http://localhost:<gw>/w/<key> and
	// http://<key>.localhost:<gw>. Explicit ORCHD_* vars still override these.
	baseDefault, schemeDefault, publicURLDefault := "lvh.me", "http", ""
	if envBool("ORCHD_LOCAL") {
		baseDefault = "localhost"
		publicURLDefault = "http://localhost" + portSuffix(gatewayAddr)
	}

	return Config{
		APIAddr:         env("ORCHD_API_ADDR", "127.0.0.1:8080"),
		GatewayAddr:     gatewayAddr,
		BaseDomain:      env("ORCHD_BASE_DOMAIN", baseDefault),
		AltDomains:      splitCSV(env("ORCHD_ALT_DOMAINS", "")),
		TemplatesDir:    env("ORCHD_TEMPLATES_DIR", ""),
		PortBase:        envInt("ORCHD_PORT_BASE", 0),
		DataRoot:        env("ORCHD_DATA_ROOT", filepath.Join(home, ".tinbase-cloud")),
		Driver:          env("ORCHD_DRIVER", "local"),
		TinbaseBin:      env("ORCHD_TINBASE_BIN", "tinbase"),
		Engine:          env("ORCHD_ENGINE", ""),
		Image:           env("ORCHD_IMAGE", "tinbase:0.14.0"),
		DockerRuntime:   env("ORCHD_DOCKER_RUNTIME", "runsc"),
		DockerHost:      env("ORCHD_DOCKER_HOST", ""),
		IdleTimeout:     envDuration("ORCHD_IDLE_TIMEOUT", 5*time.Minute),
		WorkloadEnv:     envMap("ORCHD_WORKLOAD_ENV"),
		BuildEnv:        envMap("ORCHD_BUILD_ENV"),
		HostAliases:     envMap("ORCHD_HOST_ALIASES"),
		Region:          env("ORCHD_REGION", "local"),
		APIKeyFile:      env("ORCHD_API_KEY_FILE", ""),
		PublicURL:       env("ORCHD_PUBLIC_URL", publicURLDefault),
		PublicScheme:    env("ORCHD_PUBLIC_SCHEME", schemeDefault),
		TinbaseMemMB:    envInt("ORCHD_TINBASE_MEM_MB", 384),
		TinbaseCPUs:     envFloat("ORCHD_TINBASE_CPUS", 0.5),
		DevMemMB:        envInt("ORCHD_DEV_MEM_MB", 3072),
		DevCPUs:         envFloat("ORCHD_DEV_CPUS", 2.0),
		PidsLimit:       envInt("ORCHD_PIDS_LIMIT", 512),
		BackupDir:       env("ORCHD_BACKUP_DIR", filepath.Join(env("ORCHD_DATA_ROOT", filepath.Join(home, ".tinbase-cloud")), "backups")),
		BackupInterval:  envDuration("ORCHD_BACKUP_INTERVAL", 0),
		BackupRetain:    envInt("ORCHD_BACKUP_RETAIN", 5),
		MetricsInterval: envDuration("ORCHD_METRICS_INTERVAL", 60*time.Second),
		StateDSN:        env("ORCHD_STATE_DSN", ""),
		StateSQLite:     env("ORCHD_STATE_SQLITE", ""),
		RateLimitPerMin: envInt("ORCHD_RATE_LIMIT", 0),
		FCWorkloads:     env("ORCHD_FC_WORKLOADS", ""),
		FCBin:           env("ORCHD_FC_BIN", "/opt/orchd/fc/firecracker"),
		FCKernel:        env("ORCHD_FC_KERNEL", "/opt/orchd/fc/vmlinux"),
		FCRoot:          env("ORCHD_FC_ROOT", "/opt/orchd/data/fc"),
		FCPool:          env("ORCHD_FC_POOL", "fcorchd"),
		FCMaxLive:       envInt("ORCHD_FC_MAX_LIVE", 0),
	}
}

// envMap parses "K=V,K2=V2" into a map. Pairs without '=' are skipped, so a
// typo degrades to a missing key rather than a broken boot. Values cannot
// contain commas; the intended payload (URLs, flags) never needs them.
func envMap(name string) map[string]string {
	pairs := splitCSV(os.Getenv(name))
	if len(pairs) == 0 {
		return nil
	}
	out := make(map[string]string, len(pairs))
	for _, p := range pairs {
		k, v, ok := strings.Cut(p, "=")
		if !ok || strings.TrimSpace(k) == "" {
			continue
		}
		out[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// splitCSV parses a comma-separated env value into a trimmed, non-empty list.
func splitCSV(v string) []string {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func envBool(key string) bool {
	switch strings.ToLower(os.Getenv(key)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// portSuffix returns the ":port" suffix of a host:port address (or "" if none).
func portSuffix(addr string) string {
	if i := strings.LastIndexByte(addr, ':'); i >= 0 {
		return addr[i:]
	}
	return ""
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envFloat(key string, def float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
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
