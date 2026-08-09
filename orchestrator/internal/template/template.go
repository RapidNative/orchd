// Package template reads an ORCHD template manifest (orchd.json) at the root of
// a project template folder. It is ORCHD's own descriptor — independent of any
// framework's manifest (e.g. RapidNative's rapidnative.json) — and declares the
// workloads a project made from the template should run, plus which paths are
// derived (excluded from the user-files backup delta).
//
// One template = one monorepo. Locally a project is a copy of the template and
// each workload runs a subdir as a process; in prod each workload's `image`
// (built from the template's Dockerfiles) runs as a container. The manifest is
// the single source of truth shared by both paths.
package template

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// FileName is the manifest filename at a template root.
const FileName = "orchd.json"

// Manifest is the parsed orchd.json.
type Manifest struct {
	Name          string     `json:"name"`
	BackupExclude []string   `json:"backup_exclude"`
	Workloads     []Workload `json:"workloads"`
}

// Workload is one runnable unit of a template.
//
//	kind "tinbase" — a tinbase backend (no dir/run; the driver knows how).
//	kind "node"    — a Node app: `install` once, then `run` (with $PORT), in Dir.
//	kind "static"  — serve Dir over HTTP on the assigned port (no install/run).
type Workload struct {
	Name    string            `json:"name"`
	Kind    string            `json:"kind"`               // tinbase | node | static
	Dir     string            `json:"dir,omitempty"`      // subdir relative to the template root
	Install []string          `json:"install,omitempty"`  // one-time setup, e.g. ["npm","install"]
	Run     []string          `json:"run,omitempty"`      // run argv; "$PORT" is substituted
	PortEnv string            `json:"port_env,omitempty"` // if set, the port is passed via this env var
	Image   string            `json:"image,omitempty"`    // prod image tag (built from the template)
	Env     map[string]string `json:"env,omitempty"`      // default env vars for this workload
	// Build is a list of argv commands RUN at image build time, after install —
	// the seam for baking caches into the image (a dev server's transform
	// cache, a prebuilt bundle) so boots don't pay the cold cost. Docker-only:
	// the local/process driver ignores it.
	Build [][]string `json:"build,omitempty"`
	// Primary marks the workload that owns the project's bare route
	// (<ref>.<base>); every other workload gets <ref>-<name>.<base>. At most
	// one workload may be primary. Absent everywhere = legacy behavior: the
	// first tinbase workload is primary.
	Primary bool `json:"primary,omitempty"`
}

// PrimaryName returns the manifest name of the workload that should own the
// project's bare route: the one marked primary, else (legacy manifests) the
// first tinbase workload, else "" (no primary — every route is named).
func (m *Manifest) PrimaryName() string {
	for _, w := range m.Workloads {
		if w.Primary {
			return w.Name
		}
	}
	for _, w := range m.Workloads {
		if w.Kind == "tinbase" {
			return w.Name
		}
	}
	return ""
}

// Load reads and validates <dir>/orchd.json.
func Load(dir string) (*Manifest, error) {
	b, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("%s: %w", FileName, err)
	}
	if len(m.Workloads) == 0 {
		return nil, fmt.Errorf("%s: no workloads", FileName)
	}
	seen := map[string]bool{}
	primaries := 0
	for i, w := range m.Workloads {
		if w.Name == "" {
			return nil, fmt.Errorf("%s: workload %d has no name", FileName, i)
		}
		if w.Primary {
			if primaries++; primaries > 1 {
				return nil, fmt.Errorf("%s: more than one workload marked primary", FileName)
			}
		}
		if seen[w.Name] {
			return nil, fmt.Errorf("%s: duplicate workload name %q", FileName, w.Name)
		}
		seen[w.Name] = true
		switch w.Kind {
		case "tinbase", "node", "static":
		default:
			return nil, fmt.Errorf("%s: workload %q has unknown kind %q", FileName, w.Name, w.Kind)
		}
		if w.Kind == "node" && len(w.Run) == 0 {
			return nil, fmt.Errorf("%s: node workload %q needs a run command", FileName, w.Name)
		}
		if len(w.Build) > 0 && w.Kind == "tinbase" {
			return nil, fmt.Errorf("%s: workload %q: build steps are for node/static workspaces", FileName, w.Name)
		}
		for j, step := range w.Build {
			if len(step) == 0 {
				return nil, fmt.Errorf("%s: workload %q build step %d is empty", FileName, w.Name, j)
			}
		}
	}
	return &m, nil
}

// Find returns the workload with the given name.
func (m *Manifest) Find(name string) (Workload, bool) {
	for _, w := range m.Workloads {
		if w.Name == name {
			return w, true
		}
	}
	return Workload{}, false
}

// RunArgv substitutes the assigned port into the run command ("$PORT" tokens).
func (w Workload) RunArgv(port int) []string {
	out := make([]string, len(w.Run))
	for i, a := range w.Run {
		out[i] = strings.ReplaceAll(a, "$PORT", fmt.Sprint(port))
	}
	return out
}
