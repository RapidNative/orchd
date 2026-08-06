package manager

import (
	"fmt"
	"log"
	"regexp"
	"strings"

	"github.com/tinbase/tinbase-cloud/orchestrator/internal/store"
)

// Env interpolation lets a template (or project/workload env) reference its
// sibling workloads without knowing the project ref or base domain up front:
//
//	"EXPO_PUBLIC_API_URL":           "${route.api}"
//	"EXPO_PUBLIC_SUPABASE_URL":      "${route.db}"
//	"EXPO_PUBLIC_SUPABASE_ANON_KEY": "${workload.db.anon_key}"
//
// Tokens are resolved in specFor on every boot (provision, wake, restart), so
// values stay current if routes or keys change. Unknown namespaces are left
// untouched so legitimate ${...} strings in user env survive verbatim.
//
// Supported tokens:
//
//	${route.<name>}                 public endpoint URL of the sibling workload
//	${host.<name>}                  bare default hostname of the sibling workload
//	${workload.<name>.anon_key}     sibling's minted anon key (tinbase kinds)
//	${workload.<name>.service_key}  sibling's minted service_role key
//	${workload.<name>.jwt_secret}   sibling's JWT secret
//	${project.ref}                  the project's ref (subdomain prefix)
//	${base_domain}                  the instance base domain
//
// <name> matches the sibling's manifest workspace or its workload name — the
// tinbase primary keeps Workspace "db" even though its Name is "" (primary).
var envTokenRe = regexp.MustCompile(`\$\{([A-Za-z0-9_][A-Za-z0-9_.-]*)\}`)

// PublicEndpoint returns the URL a workload's default route is reachable at:
// its stable local port in port-addressing mode, else its subdomain through
// the gateway. The single source of truth shared by the API views and env
// interpolation, so what the admin shows and what siblings dial agree.
func (m *Manager) PublicEndpoint(w *store.Workload) string {
	if w.HostPort > 0 {
		return fmt.Sprintf("http://localhost:%d", w.HostPort)
	}
	return m.EndpointForHost(m.defaultHost(w))
}

// EndpointForHost builds the public URL for a routed hostname.
func (m *Manager) EndpointForHost(host string) string {
	if m.cfg.PublicScheme == "https" {
		return "https://" + host
	}
	return "http://" + host + gatewaySuffix(m.cfg.GatewayAddr)
}

// defaultHost is the hostname minted for a workload's default route.
func (m *Manager) defaultHost(w *store.Workload) string {
	return m.keyFor(w.ProjectID, w.Name) + "." + m.cfg.BaseDomain
}

// gatewaySuffix extracts the ":port" of the gateway listen address, empty for
// the default ports (mirrors how routes are dialed in local http mode).
func gatewaySuffix(gatewayAddr string) string {
	for i := len(gatewayAddr) - 1; i >= 0; i-- {
		if gatewayAddr[i] == ':' {
			return gatewayAddr[i:]
		}
	}
	return ""
}

// interpolateEnv resolves ${...} tokens in a workload's merged env map.
// Missing siblings or keys resolve to "" with a warning; unknown namespaces
// are returned verbatim.
func (m *Manager) interpolateEnv(w *store.Workload, env map[string]string) map[string]string {
	needs := false
	for _, v := range env {
		if strings.Contains(v, "${") {
			needs = true
			break
		}
	}
	if !needs {
		return env
	}

	siblings := m.store.ListWorkloads(w.ProjectID)
	lookup := func(name string) *store.Workload {
		for _, s := range siblings {
			if s.Workspace == name || (s.Name != "" && s.Name == name) {
				return s
			}
		}
		return nil
	}

	// resolve returns the token's value and whether the token belongs to a
	// known namespace (false = leave the source text untouched).
	resolve := func(token string) (string, bool) {
		switch {
		case token == "project.ref":
			return w.ProjectID, true
		case token == "base_domain":
			return m.cfg.BaseDomain, true
		case strings.HasPrefix(token, "route."):
			if s := lookup(strings.TrimPrefix(token, "route.")); s != nil {
				return m.PublicEndpoint(s), true
			}
			return "", true
		case strings.HasPrefix(token, "host."):
			if s := lookup(strings.TrimPrefix(token, "host.")); s != nil {
				return m.defaultHost(s), true
			}
			return "", true
		case strings.HasPrefix(token, "workload."):
			rest := strings.TrimPrefix(token, "workload.")
			i := strings.LastIndex(rest, ".")
			if i <= 0 {
				return "", false
			}
			s := lookup(rest[:i])
			if s == nil {
				return "", true
			}
			switch rest[i+1:] {
			case "anon_key":
				return s.AnonKey, true
			case "service_key":
				return s.SvcKey, true
			case "jwt_secret":
				return s.JWTSecret, true
			}
			return "", false
		}
		return "", false
	}

	out := make(map[string]string, len(env))
	for k, v := range env {
		out[k] = envTokenRe.ReplaceAllStringFunc(v, func(src string) string {
			token := src[2 : len(src)-1]
			val, known := resolve(token)
			if !known {
				return src
			}
			if val == "" {
				log.Printf("env interpolate %s: %s=%s resolved empty (missing sibling or key)", w.ID, k, src)
			}
			return val
		})
	}
	return out
}
