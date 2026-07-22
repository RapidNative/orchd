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
	Name    string   `json:"name"`
	Kind    string   `json:"kind"`               // tinbase | node | static
	Dir     string   `json:"dir,omitempty"`      // subdir relative to the template root
	Install []string `json:"install,omitempty"`  // one-time setup, e.g. ["npm","install"]
	Run     []string `json:"run,omitempty"`      // run argv; "$PORT" is substituted
	PortEnv string   `json:"port_env,omitempty"` // if set, the port is passed via this env var
	Image   string   `json:"image,omitempty"`    // prod image tag (built from the template)
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
	for i, w := range m.Workloads {
		if w.Name == "" {
			return nil, fmt.Errorf("%s: workload %d has no name", FileName, i)
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
